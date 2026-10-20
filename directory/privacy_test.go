package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"os"
	"testing"
	"time"
)

func TestPrivacyErasureRemovesPersistedPreviewCopies(t *testing.T) {
	dsn := os.Getenv("DIRECTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("DIRECTORY_DATABASE_URL not configured")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	tenant := fmt.Sprintf("directory-privacy-%d", time.Now().UnixNano())
	defer func() {
		for _, table := range []string{"directory_previews", "directory_events", "directory_snapshots"} {
			db.ExecContext(ctx, "DELETE FROM "+table+" WHERE tenant=$1", tenant)
		}
	}()
	a := api.Actor{OrganizationID: tenant, UserID: "synthetic-privacy-operator", SessionID: "verified", MFA: true, Permissions: []string{"directory:import", "directory:approve", "directory:read", "directory:privacy"}, Grants: []api.Grant{{Permission: "directory:privacy", UnitID: "north"}}}
	h := NewHTTPHandler(db, HTTPConfig{Freshness: time.Hour, Clock: time.Now})
	create := func(id, phone string, version uint64) previewDTO {
		t.Helper()
		w := directoryRequest(h, a, "POST", "preview", map[string]any{"expected_version": version, "employees": []employeeDTO{{ID: id, Phone: phone, Zone: "north", Department: "synthetic", Active: true}}})
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Data previewDTO `json:"data"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out.Data
	}
	approved := create("erase-me", "+27820000001", 0)
	if w := directoryRequest(h, a, "POST", "approve", map[string]any{"preview_id": approved.PreviewID, "digest": approved.Digest, "expected_version": 0}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	pending := create("erase-me", "+27820000001", 1)
	unrelated := create("retain-me", "+27820000002", 1)
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	record := governance.Record{Reference: "erase-me", Scope: governance.Scope{OrganizationID: tenant, UnitID: "north"}}
	if e = (PrivacyAdapter{}).Erase(ctx, tx, a, record); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	for _, p := range []previewDTO{approved, pending} {
		if w := directoryRequest(h, a, "GET", "preview/"+p.PreviewID, nil); w.Code != 404 {
			t.Fatalf("personal-data preview retained after erasure: %d %s", w.Code, w.Body.String())
		}
	}
	if w := directoryRequest(h, a, "GET", "preview/"+unrelated.PreviewID, nil); w.Code != 200 {
		t.Fatal("unrelated preview removed", w.Code, w.Body.String())
	}
	store, _ := NewPostgresStore(db, tenant, a.UserID)
	snapshot, e := store.Load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, exists := snapshot.Employees["erase-me"]; exists || !snapshot.Revoked["erase-me"] {
		t.Fatal("snapshot erasure/revocation missing", snapshot)
	}
}
