package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func vectorPolicyRole(t *testing.T) (*VectorRepository, *VectorRepository, *sql.DB) {
	t.Helper()
	owner, db, tenant := vectorFixture(t)
	var schema string
	if err := db.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("vector_reader_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE ROLE " + role + " LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'synthetic-policy-role'"); err != nil {
		t.Fatal(err)
	}
	var connectSQL string
	if err := db.QueryRow("SELECT format('GRANT CONNECT ON DATABASE %I TO %I',current_database(),$1::text)", role).Scan(&connectSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(connectSQL); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("VECTOR_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "synthetic-policy-role")
	q := u.Query()
	q.Set("search_path", schema+",public")
	// Runtime transactions must override this stricter server/session default:
	// a snapshot taken before the policy-fence wait cannot authorize old grants.
	q.Set("default_transaction_isolation", "repeatable read")
	u.RawQuery = q.Encode()
	runtime, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close(); db.Exec("DROP OWNED BY " + role); db.Exec("DROP ROLE " + role) })
	if _, err = db.Exec("GRANT USAGE ON SCHEMA " + schema + " TO " + role + ";GRANT SELECT ON ALL TABLES IN SCHEMA " + schema + " TO " + role); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"addon_knowledge_grants", "addon_knowledge_approvals", "addon_knowledge_publications", "addon_knowledge_pages", "addon_knowledge_vectors"} {
		if _, err = db.Exec("GRANT INSERT,UPDATE,DELETE ON " + table + " TO " + role); err != nil {
			t.Fatal(err)
		}
	}
	var policyWrites bool
	if err = runtime.QueryRow(`SELECT has_table_privilege(current_user,'addon_knowledge_vector_profiles','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER,REFERENCES') OR has_any_column_privilege(current_user,'addon_knowledge_vector_profiles','INSERT,UPDATE,REFERENCES') OR has_table_privilege(current_user,'addon_knowledge_vector_workers','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER,REFERENCES') OR has_any_column_privilege(current_user,'addon_knowledge_vector_workers','INSERT,UPDATE,REFERENCES')`).Scan(&policyWrites); err != nil || policyWrites {
		t.Fatalf("policy write capability: %v %v", policyWrites, err)
	}
	limited, err := NewVectorRepository(runtime, tenant, owner.pin)
	if err != nil {
		t.Fatal(err)
	}
	return owner, limited, db
}

func TestVectorPolicyReadOnlyRoleCanVerifyIndexAndRetrieve(t *testing.T) {
	owner, limited, db := vectorPolicyRole(t)
	ctx := context.Background()
	publishVectorFixture(t, db, owner.catalog.tenant, fixture())
	if err := owner.RegisterIndexer(ctx, "indexer"); err != nil {
		t.Fatal(err)
	}
	if err := limited.Verify(ctx); err != nil {
		t.Fatalf("SELECT-only policy verification failed: %v", err)
	}
	indexed, err := limited.IndexNext(ctx, "indexer", vectorEmbedFunc(func(context.Context, []string) ([][]float64, error) { return [][]float64{{1, 0}}, nil }))
	if err != nil || !indexed {
		t.Fatalf("SELECT-only policy indexing failed: %v %v", indexed, err)
	}
	got, err := limited.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0})
	if err != nil || len(got) != 1 {
		t.Fatalf("SELECT-only policy retrieval failed: %d %v", len(got), err)
	}
}

func TestVectorPolicyFenceSerializesRevocationAndRechecksAfterWait(t *testing.T) {
	owner, limited, db := vectorPolicyRole(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := owner.RegisterIndexer(ctx, "indexer"); err != nil {
		t.Fatal(err)
	}
	tx, err := limited.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = limited.verifyTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- owner.DisableIndexer(ctx, "indexer") }()
	awaitGrantLock(t, db, pid)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = owner.RegisterIndexer(ctx, "indexer"); err != nil {
		t.Fatal(err)
	} // never re-enables
	if _, err = limited.IndexNext(ctx, "indexer", vectorEmbedFunc(func(context.Context, []string) ([][]float64, error) {
		t.Error("disabled worker reached provider")
		return nil, nil
	})); !errors.Is(err, ErrDenied) {
		t.Fatalf("registration restored revoked grant: %v", err)
	}
	if err = owner.RegisterIndexer(ctx, "after-wait"); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_xact_lock(19631025)"); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.ExecContext(ctx, "UPDATE addon_knowledge_vector_workers SET enabled=false WHERE tenant=$1 AND principal='after-wait'", owner.catalog.tenant); err != nil {
		t.Fatal(err)
	}
	if err = blocker.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	calls := 0
	go func() {
		_, e := limited.IndexNext(ctx, "after-wait", vectorEmbedFunc(func(context.Context, []string) ([][]float64, error) { calls++; return [][]float64{{1, 0}}, nil }))
		done <- e
	}()
	awaitGrantLock(t, db, pid)
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrDenied) || calls != 0 {
		t.Fatalf("revocation lost after fence wait: %v calls=%d", err, calls)
	}
}
