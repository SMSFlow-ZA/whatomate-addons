package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"os"
	"sync"
	"testing"
	"time"
)

func TestHTTPDurableApprovalRevocationAndConcurrency(t *testing.T) {
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
	tenant := fmt.Sprintf("directory-http-%d", time.Now().UnixNano())
	defer func() {
		for _, table := range []string{"directory_previews", "directory_events", "directory_snapshots"} {
			db.ExecContext(ctx, "DELETE FROM "+table+" WHERE tenant=$1", tenant)
		}
	}()
	a := api.Actor{OrganizationID: tenant, UserID: "reviewer", SessionID: "session", MFA: true, Permissions: []string{"directory:read", "directory:import", "directory:approve", "directory:revoke"}}
	now := time.Now().UTC()
	h := NewHTTPHandler(db, HTTPConfig{Freshness: time.Hour, Clock: func() time.Time { return now }})
	call := func(method, path string, body any, status int) json.RawMessage {
		t.Helper()
		w := directoryRequest(h, a, method, path, body)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out struct {
			Data json.RawMessage `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Data
	}
	rows := []employeeDTO{{ID: "e1", Phone: "+27820000001", Active: true}, {ID: "e2", Phone: "+27820000002", Active: false}}
	preview := func(version uint64) previewDTO {
		t.Helper()
		var p previewDTO
		json.Unmarshal(call("POST", "preview", map[string]any{"expected_version": version, "employees": rows}, 201), &p)
		return p
	}
	approve := func(p previewDTO, status int) {
		t.Helper()
		call("POST", "approve", map[string]any{"preview_id": p.PreviewID, "digest": p.Digest, "expected_version": p.ExpectedVersion}, status)
	}
	call("POST", "preview", map[string]any{"expected_version": 0, "employees": []employeeDTO{{ID: "a", Phone: "bad", Active: true}}}, 400)
	call("POST", "preview", map[string]any{"expected_version": 0, "employees": rows, "actor": "spoof"}, 400)
	call("POST", "preview", map[string]any{"employees": rows}, 400)
	p := preview(0)
	call("POST", "approve", map[string]any{"preview_id": p.PreviewID, "digest": "tampered", "expected_version": 0}, 409)
	other := a
	other.OrganizationID += "-other"
	if w := directoryRequest(h, other, "GET", "preview/"+p.PreviewID, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	// Fresh handler construction observes the persisted review artifact.
	h = NewHTTPHandler(db, HTTPConfig{Freshness: time.Hour, Clock: func() time.Time { return now }})
	var reviewed previewDTO
	json.Unmarshal(call("GET", "preview/"+p.PreviewID, nil, 200), &reviewed)
	if reviewed.Digest != p.Digest || len(reviewed.Employees) != 2 {
		t.Fatal(reviewed)
	}
	approve(p, 200)
	approve(p, 409)
	call("POST", "validate", map[string]string{"phone": rows[0].Phone}, 200)
	call("POST", "validate", map[string]string{"phone": rows[1].Phone}, 403)
	now = now.Add(time.Hour)
	call("POST", "validate", map[string]string{"phone": rows[0].Phone}, 403)
	now = now.Add(-2 * time.Hour)
	call("POST", "validate", map[string]string{"phone": rows[0].Phone}, 403)
	now = now.Add(time.Hour)
	stale := preview(1)
	call("POST", "revoke", map[string]any{"employee_id": "e1", "expected_version": 1}, 200)
	approve(stale, 409)
	p = preview(2)
	approve(p, 200)
	call("POST", "validate", map[string]string{"phone": rows[0].Phone}, 403)
	// Two different, reviewed artifacts for one version can commit only one replacement.
	p1, p2 := preview(3), preview(3)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, item := range []previewDTO{p1, p2} {
		wg.Add(1)
		go func(p previewDTO) {
			defer wg.Done()
			results <- directoryRequest(h, a, "POST", "approve", map[string]any{"preview_id": p.PreviewID, "digest": p.Digest, "expected_version": p.ExpectedVersion}).Code
		}(item)
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for code := range results {
		if code == 200 {
			ok++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatal(code)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	// Failure to insert the audit must roll back approval consumption and state together.
	p = preview(4)
	if _, e = db.ExecContext(ctx, `INSERT INTO directory_events(tenant,version,actor) VALUES($1,5,'collision')`, tenant); e != nil {
		t.Fatal(e)
	}
	approve(p, 503)
	var approved sql.NullString
	if e = db.QueryRowContext(ctx, `SELECT approved_by FROM directory_previews WHERE tenant=$1 AND id=$2`, tenant, p.PreviewID).Scan(&approved); e != nil || approved.Valid {
		t.Fatal(approved, e)
	}
	var version int64
	db.QueryRowContext(ctx, `SELECT version FROM directory_snapshots WHERE tenant=$1`, tenant).Scan(&version)
	if version != 4 {
		t.Fatal(version)
	}
	var auditActor string
	db.QueryRowContext(ctx, `SELECT actor FROM directory_events WHERE tenant=$1 AND version=1`, tenant).Scan(&auditActor)
	if auditActor != a.UserID {
		t.Fatal(auditActor)
	}
}
