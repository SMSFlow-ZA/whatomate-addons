//go:build integration

package directory

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func identityFixture(t *testing.T) (*sql.DB, *PostgresStore, IdentityBinding, time.Time) {
	t.Helper()
	dsn := os.Getenv("DIRECTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("DIRECTORY_DATABASE_URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "identity_" + fmt.Sprintf("%x", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("application_name", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	store, err := NewPostgresStore(db, "tenant", "approved-directory-operator")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	preview, err := NewPreview([]Employee{{ID: "employee-17", Phone: "+27820000017", Active: true, Zone: "north"}})
	if err != nil {
		t.Fatal(err)
	}
	next, err := Replace(Snapshot{}, preview, preview.Digest(), now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, 0, next); err != nil {
		t.Fatal(err)
	}
	return db, store, IdentityBinding{EmployeeID: "employee-17", Phone: "+27820000017", ExpectedRosterVersion: 1}, now
}

func TestIdentityTxRequiresCurrentApprovedExactBinding(t *testing.T) {
	db, _, binding, now := identityFixture(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ValidateIdentityTx(context.Background(), tx, "tenant", binding, time.Hour, now); err != nil {
		t.Fatalf("approved current binding denied: %v", err)
	}
	for _, mutate := range []func(*IdentityBinding){
		func(b *IdentityBinding) { b.EmployeeID = "unknown" },
		func(b *IdentityBinding) { b.Phone = "+27820000018" },
		func(b *IdentityBinding) { b.Phone = "0820000017" },
		func(b *IdentityBinding) { b.ExpectedRosterVersion = 0 },
		func(b *IdentityBinding) { b.ExpectedRosterVersion = 2 },
	} {
		bad := binding
		mutate(&bad)
		if err := ValidateIdentityTx(context.Background(), tx, "tenant", bad, time.Hour, now); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	if err := ValidateIdentityTx(context.Background(), tx, "other", binding, time.Hour, now); err == nil {
		t.Fatal("cross-tenant identity accepted")
	}
	if err := ValidateIdentityTx(context.Background(), tx, "tenant", binding, time.Minute, now); err == nil {
		t.Fatal("expired directory accepted")
	}
}

func TestIdentityTxHoldsApprovalAndRevocationUntilGrantCommit(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement-%t", replacement), func(t *testing.T) { testIdentityOwnerWriteFence(t, replacement) })
	}
}

func testIdentityOwnerWriteFence(t *testing.T, replacement bool) {
	db, store, binding, now := identityFixture(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ValidateIdentityTx(ctx, tx, "tenant", binding, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := Revoke(current, binding.EmployeeID)
	if replacement {
		preview, e := NewPreview([]Employee{{ID: binding.EmployeeID, Phone: "+27820000018", Active: true, Zone: "north"}})
		if e != nil {
			t.Fatal(e)
		}
		revoked, err = Replace(current, preview, preview.Digest(), now)
	}
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.CompareAndSwap(ctx, current.Version, revoked) }()
	// Verify PostgreSQL reports the actual owner UPDATE waiting on a lock,
	// rather than treating an arbitrary scheduling delay as lock evidence.
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=current_setting('application_name') AND wait_event_type='Lock' AND query LIKE 'UPDATE directory_snapshots%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("directory owner write did not wait on grant proof lock")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	check, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Rollback()
	binding.ExpectedRosterVersion = 2
	if err := ValidateIdentityTx(ctx, check, "tenant", binding, time.Hour, now); err == nil {
		t.Fatal("obsolete employee binding accepted after owner write")
	}
}

func TestIdentityTxRejectsInactiveUnauditedAndCorruptSnapshots(t *testing.T) {
	for _, name := range []string{"inactive", "missing-approval", "corrupt-version"} {
		t.Run(name, func(t *testing.T) {
			db, store, binding, now := identityFixture(t)
			switch name {
			case "inactive":
				snapshot, err := store.Load(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				employee := snapshot.Employees[binding.EmployeeID]
				employee.Active = false
				snapshot.Employees[binding.EmployeeID] = employee
				snapshot.Version++
				if err := store.CompareAndSwap(context.Background(), 1, snapshot); err != nil {
					t.Fatal(err)
				}
				binding.ExpectedRosterVersion = 2
			case "missing-approval":
				if _, err := db.Exec(`DELETE FROM directory_events WHERE tenant='tenant'`); err != nil {
					t.Fatal(err)
				}
			case "corrupt-version":
				if _, err := db.Exec(`UPDATE directory_snapshots SET version=3 WHERE tenant='tenant'`); err != nil {
					t.Fatal(err)
				}
				binding.ExpectedRosterVersion = 3
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := ValidateIdentityTx(context.Background(), tx, "tenant", binding, time.Hour, now); err == nil {
				t.Fatal(fmt.Sprintf("%s identity accepted", name))
			}
		})
	}
}
