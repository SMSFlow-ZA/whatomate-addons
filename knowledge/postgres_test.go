package knowledge

import (
	"context"
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

func postgresFixture(t *testing.T) (*PostgresRepository, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("KNOWLEDGE_DATABASE_URL")
	if dsn == "" {
		t.Skip("KNOWLEDGE_DATABASE_URL not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tenant := t.Name() + time.Now().Format("150405.000000000")
	r, err := NewPostgresRepository(db, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM addon_knowledge_publications WHERE tenant=$1", tenant)
		db.Exec("DELETE FROM addon_knowledge_approvals WHERE tenant=$1", tenant)
		db.Exec("DELETE FROM addon_knowledge_grants WHERE tenant=$1", tenant)
	})
	return r, db, tenant
}
func grant(t *testing.T, db *sql.DB, tenant, actor, action, scope string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO addon_knowledge_grants(tenant,principal,action,scope) VALUES($1,$2,$3,$4)", tenant, actor, action, scope); err != nil {
		t.Fatal(err)
	}
}
func awaitGrantLock(t *testing.T, db *sql.DB, blocker int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock')`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("repository did not wait on the permission-row lock")
}
func TestPostgresPublicationAuthorizationIsolationAndRetirement(t *testing.T) {
	r, db, tenant := postgresFixture(t)
	ctx := context.Background()
	d := fixture()
	if err := r.Publish(ctx, "publisher", d, verified(d), approval(d)); err == nil {
		t.Fatal("unauthorized publisher accepted")
	}
	grant(t, db, tenant, "publisher", "publish", "*")
	if err := r.Publish(ctx, "publisher", d, verified(d), approval(d)); err == nil {
		t.Fatal("unauthorized reviewer accepted")
	}
	grant(t, db, tenant, "reviewer", "review", "*")
	if err := r.Publish(ctx, "publisher", d, verified(d), approval(d)); err == nil {
		t.Fatal("forged human approval accepted")
	}
	if _, err := r.Approve(ctx, "reviewer", d); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, "publisher", d, verified(d), approval(d)); err != nil {
		t.Fatal(err)
	}
	// A fresh instance proves retrieval uses durable records rather than local memory.
	fresh, _ := NewPostgresRepository(db, tenant)
	q := Query{Principal: "u", Audience: "staff", Text: "policy", At: d.Effective.Add(time.Hour)}
	auth := AuthorizeFunc(func(context.Context, string, string) bool { return true })
	got, err := fresh.Retrieve(ctx, q, auth)
	if err != nil || len(got) != 0 {
		t.Fatal("read permission bypassed")
	}
	grant(t, db, tenant, "u", "read", "staff")
	got, err = fresh.Retrieve(ctx, q, auth)
	if err != nil || len(got) != 1 || got[0].ContentHash != d.ContentHash {
		t.Fatalf("durable provenance missing: %v %v", got, err)
	}
	other, _ := NewPostgresRepository(db, tenant+"-other")
	got, err = other.Retrieve(ctx, q, auth)
	if err != nil || len(got) != 0 {
		t.Fatal("cross tenant read")
	}
	if err := fresh.Retire(ctx, "publisher", d.ID, d.Version); err == nil {
		t.Fatal("retire authorization bypassed")
	}
	grant(t, db, tenant, "publisher", "retire", "*")
	if err := fresh.Retire(ctx, "publisher", d.ID, d.Version); err != nil {
		t.Fatal(err)
	}
	got, err = fresh.Retrieve(ctx, q, auth)
	if err != nil || len(got) != 0 {
		t.Fatal("retired durable source returned")
	}
}
func TestPostgresPermissionRevocationWinsBlockedPublish(t *testing.T) {
	r, db, tenant := postgresFixture(t)
	ctx := context.Background()
	grant(t, db, tenant, "publisher", "publish", "*")
	grant(t, db, tenant, "reviewer", "review", "*")
	if _, err := r.Approve(ctx, "reviewer", fixture()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM addon_knowledge_grants WHERE tenant=$1 AND principal='publisher'", tenant); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err = tx.QueryRow("SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		d := fixture()
		done <- r.Publish(ctx, "publisher", d, verified(d), approval(d))
	}()
	<-started
	awaitGrantLock(t, db, blocker)
	// Commit the revocation while a publisher may be waiting on its grant row.
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("publish survived revoked permission")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publish deadlocked")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM addon_knowledge_publications WHERE tenant=$1", tenant).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied publication persisted")
	}
}

func TestPostgresPermissionRevocationWinsBlockedRetire(t *testing.T) {
	r, db, tenant := postgresFixture(t)
	ctx := context.Background()
	grant(t, db, tenant, "publisher", "publish", "*")
	grant(t, db, tenant, "reviewer", "review", "*")
	grant(t, db, tenant, "publisher", "retire", "*")
	d := fixture()
	if _, err := r.Approve(ctx, "reviewer", d); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, "publisher", d, verified(d), approval(d)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM addon_knowledge_grants WHERE tenant=$1 AND principal='publisher' AND action='retire'", tenant); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err = tx.QueryRow("SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Retire(ctx, "publisher", d.ID, d.Version) }()
	awaitGrantLock(t, db, blocker)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("retirement survived revoked permission")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retire deadlocked")
	}
	var retired bool
	if err = db.QueryRow("SELECT retired FROM addon_knowledge_publications WHERE tenant=$1", tenant).Scan(&retired); err != nil || retired {
		t.Fatal("denied retirement changed publication")
	}
}

func TestPostgresFullTextRetrievesReorderedTermsAndSuppressesRetiredLatest(t *testing.T) {
	r, db, tenant := postgresFixture(t)
	ctx := context.Background()
	grant(t, db, tenant, "publisher", "publish", "*")
	grant(t, db, tenant, "reviewer", "review", "*")
	grant(t, db, tenant, "publisher", "retire", "*")
	grant(t, db, tenant, "u", "read", "staff")
	d := fixture()
	a, err := r.Approve(ctx, "reviewer", d)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Publish(ctx, "publisher", d, verified(d), a); err != nil {
		t.Fatal(err)
	}
	q := Query{Principal: "u", Audience: "staff", Text: "policy synthetic", At: d.Effective.Add(time.Hour)}
	auth := AuthorizeFunc(func(context.Context, string, string) bool { return true })
	got, err := r.Retrieve(ctx, q, auth)
	if err != nil || len(got) != 1 || got[0].Page.Number != 1 {
		t.Fatalf("full text terms failed: %v %v", got, err)
	}
	d.Version = "2"
	d.Effective = d.Effective.Add(time.Hour)
	a, err = r.Approve(ctx, "reviewer", d)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Publish(ctx, "publisher", d, verified(d), a); err != nil {
		t.Fatal(err)
	}
	if err = r.Retire(ctx, "publisher", d.ID, d.Version); err != nil {
		t.Fatal(err)
	}
	got, err = r.Retrieve(ctx, q, auth)
	if err != nil || len(got) != 0 {
		t.Fatal("full text resurrected retired superseded source")
	}
}
