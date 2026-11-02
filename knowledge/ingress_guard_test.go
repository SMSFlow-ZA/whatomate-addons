package knowledge

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

func TestHTTPIngressGuardSharesEnqueueTransaction(t *testing.T) {
	db, _ := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if e := MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`CREATE TABLE guard_probe(value text PRIMARY KEY)`); e != nil {
		t.Fatal(e)
	}
	actor := api.Actor{OrganizationID: "tenant", UserID: "editor", SessionID: "session", MFA: true, Permissions: []string{"knowledge:upload"}, Grants: []api.Grant{{Permission: "knowledge:upload", Audience: "staff"}}}
	m := UploadMetadata{DocumentID: "synthetic", Version: "1", Filename: "fixture.pdf", Audiences: []string{"staff"}, Effective: time.Now().UTC()}
	deny := true
	handler := NewHTTPHandler(db, HTTPConfig{Enabled: true, Authorize: func(context.Context, string, string, string, string) bool { return true }, AuthorizeIngress: func(ctx context.Context, tx *sql.Tx, a api.Actor, metadata UploadMetadata) error {
		if a.OrganizationID != "tenant" || metadata.DocumentID != "synthetic" {
			t.Fatal("untrusted context")
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO guard_probe(value) VALUES('guard') ON CONFLICT DO NOTHING`); e != nil {
			return e
		}
		if deny {
			return ErrDenied
		}
		return nil
	}})
	if w := uploadRequest(handler, actor, m); w.Code != 403 {
		t.Fatalf("denied ingress %d", w.Code)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM guard_probe`).Scan(&count)
	if count != 0 {
		t.Fatal("guard mutation escaped rollback")
	}
	db.QueryRow(`SELECT count(*) FROM addon_knowledge_jobs`).Scan(&count)
	if count != 0 {
		t.Fatal("denied upload persisted")
	}
	deny = false
	if w := uploadRequest(handler, actor, m); w.Code != 202 {
		t.Fatalf("authorized ingress %d", w.Code)
	}
	db.QueryRow(`SELECT count(*) FROM guard_probe`).Scan(&count)
	if count != 1 {
		t.Fatal("guard did not commit with job")
	}
	db.QueryRow(`SELECT count(*) FROM addon_knowledge_jobs`).Scan(&count)
	if count != 1 {
		t.Fatal("authorized job missing")
	}
	var id string
	if e := db.QueryRow(`SELECT id FROM addon_knowledge_jobs`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	deny = true
	for _, action := range []string{"approve", "publish", "retire"} {
		r := httptest.NewRequest("POST", "/api/extensions/knowledge/jobs/"+id+"/"+action, nil)
		r = r.WithContext(api.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s bypassed ingress guard: %d", action, w.Code)
		}
	}
}
