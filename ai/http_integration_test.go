//go:build integration

package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSQLHTTPDurabilityRevocationCorrection(t *testing.T) {
	dsn := os.Getenv("AI_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = (SQLStore{db}).Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: "ai-" + time.Now().Format("150405.000000000"), UserID: "operator", SessionID: "s", MFA: true, Permissions: []string{"ai:assist", "ai:review", "knowledge:read"}, Grants: []api.Grant{{Permission: "knowledge:read", Audience: "staff"}}}
	active := true
	cfg := HTTPConfig{Enabled: true, Authorize: func(context.Context, api.Actor, string, string) bool { return active }, Cases: func(_ context.Context, _ api.Actor, id, permission string) bool {
		return id == "case-1" && permission == "cases:work.assigned"
	}, ServiceFor: func(context.Context, api.Actor) (Service, error) {
		return Service{Catalog: knowledge.NewCatalog(), Now: time.Now}, nil
	}}
	call := func(hbody, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/extensions/ai/"+path, strings.NewReader(hbody))
		r = r.WithContext(api.WithActor(r.Context(), a))
		NewHTTPHandler(db, cfg).ServeHTTP(w, r)
		return w
	}
	if w := call(`{"case_id":"case-1","audience":"staff","question":"help","sources":[]}`, "answers"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := call(`{"case_id":"case-1","audience":"staff","question":"help"}`, "answers")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct{ Data Record }
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Data.Status != "handoff_requested" {
		t.Fatal(out)
	}
	w = call(`{"expected_version":1,"reason":"Human reviewed; assigned reviewer requested"}`, "records/"+out.Data.ID+"/correct")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call(`{"expected_version":1,"reason":"stale"}`, "records/"+out.Data.ID+"/handoff"); w.Code != 409 {
		t.Fatal(w.Code)
	}
	active = false
	if w = call(`{"case_id":"case-1","audience":"staff","question":"help"}`, "answers"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	active = true
	cfg.ServiceFor = func(context.Context, api.Actor) (Service, error) {
		return Service{Catalog: fakeRetriever{[]knowledge.Source{{Reference: "server-reference", ManifestHash: "manifest", Page: knowledge.Page{Number: 1, Text: "Policy"}}}}, Now: time.Now, Provider: generatorFunc(func(context.Context, Request) (Completion, error) {
			active = false
			return Completion{Answer: "Policy", Citations: []string{"server-reference"}, Grounded: true}, nil
		})}, nil
	}
	if w = call(`{"case_id":"case-1","audience":"staff","question":"policy"}`, "answers"); w.Code != 403 {
		t.Fatal("revocation during inference must suppress response and persistence", w.Code)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM addon_ai_audit WHERE organization_id=$1`, a.OrganizationID).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
