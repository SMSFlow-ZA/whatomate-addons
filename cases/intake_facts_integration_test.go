//go:build integration

package cases

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

func TestHTTPIntakeCannotForgeCreationDimensions(t *testing.T) {
	repo := integrationRepository(t)
	org := "intake-facts-" + time.Now().Format("150405.000000000")
	actor := api.Actor{OrganizationID: org, UserID: "employee", SessionID: "s", MFA: true, Permissions: []string{string(SubmitOwn)}, Grants: []api.Grant{{Permission: string(SubmitOwn), UnitID: "approved-zone"}}}
	cfg := HTTPConfig{Enabled: true, ResolveIntake: func(context.Context, api.Actor) (Classification, error) {
		return Classification{Zone: "approved-zone", Department: "approved-department", CaseType: "employee_request"}, nil
	}}
	body := `{"unit_id":"approved-zone","key":"one","narrative":"synthetic concern","snapshot":{"zone":"FORGED-ZONE","department":"FORGED-DEPARTMENT","case_type":"FORGED-TYPE"}}`
	request := func(cfg HTTPConfig) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/extensions/cases", strings.NewReader(body))
		r = r.WithContext(api.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		NewHTTPHandler(repo.DB, cfg).ServeHTTP(w, r)
		return w
	}
	if w := request(HTTPConfig{Enabled: true}); w.Code != 503 {
		t.Fatal("missing directory policy", w.Code, w.Body.String())
	}
	w := request(cfg)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		}
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	tx, e := repo.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	c, e := tx.Load(context.Background(), response.Data.ID)
	if e != nil || c.Snapshot.Zone != "approved-zone" || c.Snapshot.Department != "approved-department" || c.Snapshot.CaseType != "employee_request" {
		t.Fatal("forged dimensions persisted", c.Snapshot, e)
	}
}
