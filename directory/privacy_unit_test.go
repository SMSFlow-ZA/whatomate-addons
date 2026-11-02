package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPrivacyTrustedUnitResolverPreservesReportingZone(t *testing.T) {
	dsn := os.Getenv("DIRECTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("DIRECTORY_DATABASE_URL required")
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := fmt.Sprintf("privacy_unit_%x", time.Now().UnixNano())
	if _, e = admin.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	ctx := context.Background()
	if e = MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	snapshot := Snapshot{Version: 1, ImportedAt: time.Now(), Employees: map[string]Employee{"employee": {ID: "employee", Phone: "+27820000001", Zone: "North", Active: true}}, Revoked: map[string]bool{}}
	raw, _ := json.Marshal(snapshot)
	if _, e = db.Exec(`INSERT INTO directory_snapshots(tenant,version,payload) VALUES('tenant',1,$1)`, raw); e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: "tenant", UserID: "officer", SessionID: "verified", MFA: true, Permissions: []string{"directory:privacy", "directory:read"}, Grants: []api.Grant{{Permission: "directory:privacy", UnitID: "opaque-hr-unit"}}}
	scope := governance.Scope{OrganizationID: "tenant", UnitID: "opaque-hr-unit"}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = (PrivacyAdapter{}).Discover(ctx, tx, a, scope, "employee"); e != governance.ErrDenied {
		t.Fatalf("legacy zone fence changed: %v", e)
	}
	deny := true
	adapter := PrivacyAdapter{AuthorizeSubjectUnit: func(ctx context.Context, got *sql.Tx, actor api.Actor, unit, subject string) error {
		if got != tx || actor.OrganizationID != "tenant" || unit != "opaque-hr-unit" || subject != "employee" {
			t.Fatal("resolver received wrong authority")
		}
		if deny {
			return governance.ErrDenied
		}
		return nil
	}}
	if _, e = adapter.Discover(ctx, tx, a, scope, "employee"); e != governance.ErrDenied {
		t.Fatal("resolver denial ignored")
	}
	deny = false
	records, e := adapter.Discover(ctx, tx, a, scope, "employee")
	if e != nil || len(records) != 1 {
		t.Fatalf("trusted unit denied: %v", e)
	}
	exported, e := adapter.Export(ctx, tx, a, records[0])
	if e != nil {
		t.Fatal(e)
	}
	var value struct{ Employee Employee }
	json.Unmarshal(exported, &value)
	if value.Employee.Zone != "North" {
		t.Fatal("reporting zone rewritten")
	}
	if e = adapter.Erase(ctx, tx, a, records[0]); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
