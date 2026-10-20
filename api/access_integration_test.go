//go:build integration

package api

import (
	"context"
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

func TestSQLScopeIntersectionAndReplacement(t *testing.T) {
	dsn := os.Getenv("API_DATABASE_URL")
	if dsn == "" {
		t.Skip("API_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store := AccessStore{db}
	ctx := context.Background()
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	org := "api-" + time.Now().Format("150405.000000000")
	if e = store.Replace(ctx, org, "u", []Grant{{Permission: "cases:read", UnitID: "alpha"}, {Permission: "cases:delete", UnitID: "alpha"}}); e != nil {
		t.Fatal(e)
	}
	actor := Actor{OrganizationID: org, UserID: "u", MFA: true, SessionID: "s", Permissions: []string{"cases:read"}}
	loaded, e := store.Load(ctx, actor)
	if e != nil || len(loaded.Grants) != 1 || !loaded.Allows("cases:read", "alpha") {
		t.Fatalf("intersection failed: %+v %v", loaded, e)
	}
	other := actor
	other.OrganizationID += "-other"
	loaded, e = store.Load(ctx, other)
	if e != nil || len(loaded.Grants) != 0 {
		t.Fatal("cross tenant grant")
	}
	if e = store.Replace(ctx, org, "u", nil); e != nil {
		t.Fatal(e)
	}
	loaded, e = store.Load(ctx, actor)
	if e != nil || loaded.Allows("cases:read", "alpha") {
		t.Fatal("revoked scope survives")
	}
	if e = store.Replace(ctx, org, "u", []Grant{{Permission: "cases:read", UnitID: "*"}}); e == nil {
		t.Fatal("wildcard accepted")
	}
}
