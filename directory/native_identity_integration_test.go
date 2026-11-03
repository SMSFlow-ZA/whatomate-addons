//go:build integration

package directory

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"testing"
	"time"
)

func TestNativeIdentityTxPhoneLessCurrentApprovalAndRevocation(t *testing.T) {
	if os.Getenv("DIRECTORY_DATABASE_URL") == "" {
		t.Fatal("owned isolated PostgreSQL required")
	}
	db, store, _, now := identityFixture(t)
	ctx := context.Background()
	p, e := NewPreviewWithOptions([]Employee{{ID: "employee-17", Active: true}, {ID: "employee-18", Active: true}}, PreviewOptions{AllowPhoneLessEmployees: true})
	if e != nil {
		t.Fatal(e)
	}
	original, e := store.Load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	next, e := Replace(original, p, p.Digest(), now.Add(-time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if e = store.CompareAndSwap(ctx, original.Version, next); e != nil {
		t.Fatal(e)
	}
	validate := func(tenant, id string, version uint64, clock time.Time) error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		return ValidateNativeIdentityTx(ctx, tx, tenant, NativeIdentityBinding{EmployeeID: id, ExpectedRosterVersion: version}, time.Hour, clock)
	}
	for _, id := range []string{"employee-17", "employee-18"} {
		if e = validate("tenant", id, next.Version, now); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []struct {
		tenant, id string
		version    uint64
		clock      time.Time
	}{{"other", "employee-17", next.Version, now}, {"tenant", "", next.Version, now}, {"tenant", "unknown", next.Version, now}, {"tenant", "employee-17", 1, now}, {"tenant", "employee-17", next.Version, now.Add(time.Hour)}, {"tenant", "employee-17", next.Version, now.Add(-time.Hour)}} {
		if e = validate(v.tenant, v.id, v.version, v.clock); e == nil {
			t.Fatalf("invalid binding accepted: %+v", v)
		}
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = ValidateNativeIdentityTx(ctx, tx, "tenant", NativeIdentityBinding{EmployeeID: "employee-17", ExpectedRosterVersion: next.Version}, time.Hour, now); e != nil {
		t.Fatal(e)
	}
	revoked, e := Revoke(next, "employee-17")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(revoked)
	writer, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Rollback()
	if _, e = writer.ExecContext(ctx, "SET LOCAL lock_timeout='100ms'"); e != nil {
		t.Fatal(e)
	}
	if _, e = writer.ExecContext(ctx, "UPDATE directory_snapshots SET payload=$2,version=$3 WHERE tenant=$1", "tenant", raw, revoked.Version); e == nil {
		t.Fatal("revocation bypassed retained source lock")
	}
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "55P03" {
		t.Fatalf("expected server-confirmed lock denial, got %v", e)
	}
	writer.Rollback()
	tx.Rollback()
	if e = store.CompareAndSwap(ctx, next.Version, revoked); e != nil {
		t.Fatal(e)
	}
	if e = validate("tenant", "employee-17", revoked.Version, now); e == nil {
		t.Fatal("revoked identity accepted")
	}
	if e = validate("tenant", "employee-18", revoked.Version, now); e != nil {
		t.Fatal("unrelated employee denied", e)
	}
	tx, e = db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = ValidateIdentityTx(ctx, tx, "tenant", IdentityBinding{EmployeeID: "employee-18", ExpectedRosterVersion: revoked.Version}, time.Hour, now); e == nil {
		t.Fatal("phone connector accepted blank")
	}
}
