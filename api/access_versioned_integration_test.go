//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func versionedAccessDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("API_DATABASE_URL")
	if dsn == "" {
		t.Skip("API_DATABASE_URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "access_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	if err := (AccessStore{DB: db}).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAccessVersionedPreviewCASAtomicAuditAndLegacyInvalidation(t *testing.T) {
	db := versionedAccessDB(t)
	store := AccessStore{DB: db}
	ctx := context.Background()
	org := "versioned-" + time.Now().Format("150405.000000000")
	c := AccessReplacement{OrganizationID: org, UserID: "u", OperatorID: "operator", Action: "replace", Reason: "review-17", Grants: []Grant{{Permission: "cases:read", UnitID: "north"}}}
	check := func(context.Context, *sql.Tx, bool) error { return nil }
	if got, err := store.ReplaceVersioned(ctx, c, false, check); err != nil || got.Version != 0 || len(got.Grants) != 0 {
		t.Fatalf("preview changed state: %+v %v", got, err)
	}
	if _, err := store.ReplaceVersioned(ctx, c, true, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing host validation accepted: %v", err)
	}
	if _, err := store.ReplaceVersioned(ctx, c, true, func(context.Context, *sql.Tx, bool) error { return ErrDenied }); !errors.Is(err, ErrDenied) {
		t.Fatal("host denial ignored")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { _, err := store.ReplaceVersioned(ctx, c, true, check); errs <- err })
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes=%d", success)
	}
	got, err := store.InspectVersioned(ctx, org, "u", check)
	if err != nil || got.Version != 1 || len(got.Grants) != 1 {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	c.ExpectedVersion = 1
	c.Action, c.Grants = "revoke", nil
	// A host write must roll back if the mandatory audit insert fails.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS addon_api_access_test_host (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	c.Metadata = []byte(`null`)
	if _, err := store.ReplaceVersioned(ctx, c, true, func(ctx context.Context, tx *sql.Tx, _ bool) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO addon_api_access_test_host(id) VALUES($1)`, org); err != nil {
			return err
		}
		// The temporary DDL and host write both roll back when the ensuing
		// mandatory audit insert violates this transaction-local constraint.
		_, err := tx.ExecContext(ctx, `ALTER TABLE addon_api_access_audit ADD CONSTRAINT addon_api_access_test_failure CHECK (new_version<0) NOT VALID`)
		return err
	}); err == nil {
		t.Fatal("failed host write committed")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_access_test_host WHERE id=$1`, org).Scan(&count); err != nil || count != 0 {
		t.Fatal("host mutation escaped rollback")
	}
	if err := store.Replace(ctx, org, "u", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceVersioned(ctx, c, true, check); !errors.Is(err, ErrAccessConflict) {
		t.Fatalf("legacy replacement failed to invalidate expected version: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_access_audit WHERE organization_id=$1`, org).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unexpected audit count %d %v", count, err)
	}
	for _, stmt := range []string{`UPDATE addon_api_access_audit SET reason='changed'`, `DELETE FROM addon_api_access_audit`, `TRUNCATE addon_api_access_audit`} {
		if _, err := db.Exec(stmt); err == nil {
			t.Fatalf("audit mutation accepted: %s", stmt)
		}
	}
}

func TestAccessReplacementAuditsLegacyMutations(t *testing.T) {
	db := versionedAccessDB(t)
	org := "legacy-audit-" + time.Now().Format("150405.000000000")
	if err := (AccessStore{DB: db}).Replace(context.Background(), org, "u", []Grant{{Permission: "cases:read", UnitID: "north"}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM addon_api_access_audit WHERE organization_id=$1 AND user_id='u' AND action='legacy-replace'`, org).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replacement audit absent: count=%d, err=%v", count, err)
	}
}
