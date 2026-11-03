package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"os"
	"strings"
	"testing"
	"time"
)

func TestImportHTTPUpsertSelectionAcknowledgementAndPrivacy(t *testing.T) {
	dsn := os.Getenv("DIRECTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("DIRECTORY_DATABASE_URL required")
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
	tenant := fmt.Sprintf("import-intent-%d", time.Now().UnixNano())
	defer func() {
		for _, table := range []string{"directory_previews", "directory_events", "directory_snapshots"} {
			db.Exec("DELETE FROM "+table+" WHERE tenant=$1", tenant)
		}
	}()
	actor := api.Actor{OrganizationID: tenant, UserID: "reviewer", SessionID: "session", MFA: true, Permissions: []string{"directory:import", "directory:approve", "directory:read", "directory:revoke"}}
	first, last := "Lindiwe", "Dlamini"
	cfg := HTTPConfig{RejectUnsafeImportText: true, Freshness: time.Hour, Clock: time.Now, AllowPhoneLessEmployees: true, ImportDefaultMode: "upsert", ImportMaxRows: 2, ImportMaxFileBytes: 5 << 20}
	h := NewHTTPHandler(db, cfg)
	call := func(path string, body any, want int) json.RawMessage {
		t.Helper()
		w := directoryRequest(h, actor, "POST", path, body)
		if w.Code != want {
			t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
		}
		var out struct {
			Data json.RawMessage `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Data
	}
	preview := func(version int, rows []ImportRow, mode string, selected *[]int, key string) previewDTO {
		t.Helper()
		body := map[string]any{"expected_version": version, "rows": rows, "mode": mode, "request_id": key}
		if selected != nil {
			body["selected_rows"] = *selected
		}
		var v previewDTO
		json.Unmarshal(call("preview", body, 201), &v)
		return v
	}
	approve := func(v previewDTO, ack bool, want int) {
		t.Helper()
		call("approve", map[string]any{"preview_id": v.PreviewID, "digest": v.Digest, "expected_version": v.ExpectedVersion, "acknowledge_removals": ack}, want)
	}
	p := preview(0, []ImportRow{{ID: "old", Active: "true"}}, "", nil, "11111111111111111111111111111111")
	approve(p, false, 200)
	mixed := []ImportRow{{ID: "new", Zone: "unit", FirstName: &first, LastName: &last, Active: "true"}, {ID: "bad", Phone: "not-phone", Active: "true"}}
	call("preview", map[string]any{"expected_version": 1, "rows": mixed}, 200)
	for _, field := range []string{"id", "first_name", "last_name", "zone", "department"} {
		row := map[string]any{"id": "safe", "active": "true", "phone": "+27820000009"}
		row[field] = "=HYPERLINK(\"https://invalid\")"
		body := map[string]any{"expected_version": 1, "rows": []any{row}}
		call("preview", body, 200)
		body["selected_rows"] = []int{1}
		call("preview", body, 400)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM directory_previews WHERE tenant=$1`, tenant).Scan(&count)
	if count != 1 {
		t.Fatal("invalid rows saved", count)
	}
	selection := []int{1}
	p = preview(1, mixed, "", &selection, "22222222222222222222222222222222")
	if len(p.Employees) != 2 || p.Import.Mode != "upsert" || p.Import.SourceDigest == "" {
		t.Fatal(p)
	}
	replay := preview(1, mixed, "", &selection, "22222222222222222222222222222222")
	if replay.PreviewID != p.PreviewID || replay.Digest != p.Digest {
		t.Fatal("preview replay changed")
	}
	call("preview", map[string]any{"expected_version": 1, "rows": []ImportRow{{ID: "changed", Active: "true"}}, "request_id": p.PreviewID}, 409)
	approve(p, false, 200)
	staleBeforeRevoke := preview(2, []ImportRow{{ID: "new", Zone: "unit", FirstName: &first, LastName: &last, Active: "true"}}, "replace", nil, "")
	call("revoke", map[string]any{"employee_id": "old", "expected_version": 2}, 200)
	approve(staleBeforeRevoke, true, 409)
	p = preview(3, []ImportRow{{ID: "old", Active: "true"}}, "", nil, "")
	approve(p, false, 200)
	store, _ := NewPostgresStore(db, tenant, "reviewer")
	snap, e := store.Load(ctx)
	if e != nil || !snap.Revoked["old"] || !snap.Employees["new"].Active {
		t.Fatal(snap, e)
	}
	p = preview(4, []ImportRow{{ID: "new", Zone: "unit", FirstName: &first, LastName: &last, Active: "true"}}, "replace", nil, "")
	if p.Import.RemovalCount != 1 {
		t.Fatal(p.Import)
	}
	approve(p, false, 400)
	approve(p, true, 200)
	stale := p
	stale.ExpectedVersion = 4
	approve(stale, true, 409)
	// The only new persisted metadata is opaque source/removal hashes + counts/indexes.
	var raw string
	if e = db.QueryRow(`SELECT import_metadata::text FROM directory_previews WHERE tenant=$1 AND id=$2`, tenant, p.PreviewID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var meta map[string]any
	json.Unmarshal([]byte(raw), &meta)
	if _, exists := meta["removed_ids"]; exists {
		t.Fatal("raw removed IDs retained")
	}
	// Existing source erasure deletes the whole preview containing a person; metadata goes with it.
	actor.Permissions = append(actor.Permissions, "directory:privacy")
	actor.Grants = []api.Grant{{Permission: "directory:privacy", UnitID: "unit"}}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	exported, exportErr := (PrivacyAdapter{}).Export(ctx, tx, actor, governance.Record{Scope: governance.Scope{OrganizationID: tenant, UnitID: "unit"}, Reference: "new"})
	if exportErr != nil || !strings.Contains(string(exported), first) || !strings.Contains(string(exported), last) {
		t.Fatal("names not present in actual source export", exportErr)
	}
	if e = (PrivacyAdapter{}).Erase(ctx, tx, actor, governance.Record{Scope: governance.Scope{OrganizationID: tenant, UnitID: "unit"}, Reference: "new"}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}

	var remaining string
	if e = db.QueryRow(`SELECT payload::text FROM directory_snapshots WHERE tenant=$1`, tenant).Scan(&remaining); e != nil || strings.Contains(remaining, first) || strings.Contains(remaining, last) {
		t.Fatal("source names remain after erasure", e)
	}
	if w := directoryRequest(h, actor, "GET", "preview/"+p.PreviewID, nil); w.Code != 404 {
		t.Fatal("erased preview still visible", w.Code)
	}
}
