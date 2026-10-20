package directory

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPostgresDurableCASAndRevocation(t *testing.T) {
	dsn := os.Getenv("DIRECTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("DIRECTORY_DATABASE_URL not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	tenant := "directory-test-" + time.Now().UTC().Format("20060102150405.000000000")
	store, err := NewPostgresStore(db, tenant, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM directory_events WHERE tenant=$1", tenant)
		db.ExecContext(ctx, "DELETE FROM directory_snapshots WHERE tenant=$1", tenant)
	})
	p, _ := NewPreview(employees())
	now := time.Now().UTC()
	start, err := store.Load(ctx)
	if err != nil || start.Version != 0 {
		t.Fatal(start, err)
	}
	n, _ := Replace(start, p, p.Digest(), now)
	staleImport, _ := Replace(n, p, p.Digest(), now.Add(time.Minute))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- store.CompareAndSwap(ctx, 0, n) }()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	if err = UrgentRevoke(ctx, store, "e1"); err != nil {
		t.Fatal(err)
	}
	// A new connection-backed adapter must observe revocations after replacement.
	otherDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other, _ := NewPostgresStore(otherDB, tenant, "another-operator")
	if _, err = Apply(ctx, other, p, p.Digest(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, err := other.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 3 || !current.Revoked["e1"] {
		t.Fatal(current)
	}
	if _, err = Match(current, "+27820000001", now.Add(time.Minute), time.Hour); err == nil {
		t.Fatal("durable revocation lost")
	}
	// A stale import cannot overwrite the urgent revocation.
	if err = store.CompareAndSwap(ctx, 1, staleImport); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	isolated, _ := NewPostgresStore(db, tenant+"-other", "test-operator")
	empty, err := isolated.Load(ctx)
	if err != nil || len(empty.Employees) != 0 {
		t.Fatal(empty, err)
	}
	var auditCount int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM directory_events WHERE tenant=$1", tenant).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatal(auditCount, err)
	}
	// Force audit insertion to fail after the state update. The transaction must
	// roll back the entire snapshot, preserving the durable revocation.
	if _, err = db.ExecContext(ctx, "INSERT INTO directory_events(tenant,version,actor) VALUES($1,4,'collision')", tenant); err != nil {
		t.Fatal(err)
	}
	proposal, _ := Replace(current, p, p.Digest(), now.Add(2*time.Minute))
	if err = store.CompareAndSwap(ctx, 3, proposal); err == nil {
		t.Fatal("audit failure ignored")
	}
	preserved, err := store.Load(ctx)
	if err != nil || preserved.Version != 3 || !preserved.Revoked["e1"] {
		t.Fatal(preserved, err)
	}
}

func TestPostgresInvalidConfiguration(t *testing.T) {
	if _, err := NewPostgresStore(nil, "tenant", "actor"); err == nil {
		t.Fatal("nil database accepted")
	}
}
