//go:build integration

package cases

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

func TestSQLNewComplainantAliasRevokesOperatorDisclosureButPreservesOwnStatus(t *testing.T) {
	repository := integrationRepository(t)
	ctx := context.Background()
	service := Service{Repository: repository}
	org := "complainant-boundary-" + time.Now().Format("150405.000000000")
	created, err := service.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "unit", ContactID: "employee", Key: "fresh-intake", Narrative: "private internal narrative", Snapshot: Classification{Zone: "unit", Department: "private department", CaseType: "private-category"}})
	if err != nil {
		t.Fatal(err)
	}
	operator := Principal{ID: "operator", Grants: []Grant{{OrganizationID: org, UnitID: "unit", Permission: WorkAssigned}, {OrganizationID: org, UnitID: "unit", Permission: ReadTriage}, {OrganizationID: org, UnitID: "unit", Permission: AssignTriage}}}
	triage := Principal{ID: "independent-triage", Grants: operator.Grants}
	c, err := service.Escalate(ctx, triage, created.CaseID, 1, []Candidate{{Principal: operator, Role: Handler}}, "independent reviewed route", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope := NotificationScope{OrganizationID: org, UnitIDs: []string{"unit"}}
	if _, err = CurrentNotification(ctx, repository.DB, scope, operator, c.ID, c.Version); err != nil {
		t.Fatal("initial independent operator denied", err)
	}
	var before []byte
	if err = repository.DB.QueryRowContext(ctx, "SELECT payload FROM addon_cases_case WHERE organization_id=$1 AND id=$2", org, c.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// A newly verified historical identity makes the existing assignment unsafe;
	// no case or participant rewrite is required for immediate current denial.
	operator.Aliases = []string{"employee"}
	if _, err = CurrentNotification(ctx, repository.DB, scope, operator, c.ID, c.Version); !errors.Is(err, ErrDenied) {
		t.Fatal("complainant alias retained notification authority", err)
	}
	actor := api.Actor{OrganizationID: org, UserID: operator.ID, SessionID: "signed-session", MFA: true, Aliases: operator.Aliases, Permissions: []string{string(WorkAssigned), string(ReadTriage), string(AssignTriage)}, Grants: []api.Grant{{Permission: string(WorkAssigned), UnitID: "unit"}, {Permission: string(ReadTriage), UnitID: "unit"}, {Permission: string(AssignTriage), UnitID: "unit"}}}
	handler := NewHTTPHandler(repository.DB, HTTPConfig{Enabled: true})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/extensions/cases/"+path, strings.NewReader(body))
		req = req.WithContext(api.WithActor(req.Context(), actor))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	hidden := request("GET", c.ID, "")
	unknown := request("GET", "absent", "")
	if hidden.Code != 404 || hidden.Code != unknown.Code || hidden.Body.String() != unknown.Body.String() {
		t.Fatal("operator-only complainant existence leak", hidden.Code, unknown.Code)
	}
	actor.Permissions = append(actor.Permissions, string(ReadOwn))
	actor.Grants = append(actor.Grants, api.Grant{Permission: string(ReadOwn), UnitID: "unit"})
	own := request("GET", c.ID, "")
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if own.Code != 200 || json.Unmarshal(own.Body.Bytes(), &envelope) != nil || len(envelope.Data) != 3 || envelope.Data["id"] != c.ID || envelope.Data["state"] != string(Escalated) || envelope.Data["version"] != float64(c.Version) {
		t.Fatal("own status must be exactly id/state/version", own.Code, own.Body.String())
	}
	if hits, err := ListCurrent(ctx, repository.DB, actor, "private-category"); err != nil || len(hits) != 0 {
		t.Fatal("own operator category search leaked", err)
	}
	if facts, err := ReportFacts(ctx, repository.DB, actor); err != nil || len(facts) != 0 {
		t.Fatal("own report dimensions leaked", err)
	}
	tx, err := repository.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrivacyCase(ctx, tx, actor, c.ID)
	tx.Rollback()
	if !errors.Is(err, governance.ErrDenied) {
		t.Fatal("own operator privacy export bypass", err)
	}
	denied := request("POST", c.ID+"/category", `{"expected_version":2,"category":"changed","reason":"own complaint"}`)
	if denied.Code != 404 {
		t.Fatal("own category update allowed", denied.Code)
	}
	if _, err = service.ReadEmergency(ctx, operator, c.ID, func(context.Context, string) error { return nil }); !errors.Is(err, ErrDenied) {
		t.Fatal("own emergency disclosure bypass", err)
	}
	var after []byte
	if err = repository.DB.QueryRowContext(ctx, "SELECT payload FROM addon_cases_case WHERE organization_id=$1 AND id=$2", org, c.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("denials changed historical case proof")
	}
	actor.Permissions = actor.Permissions[:len(actor.Permissions)-1]
	actor.Grants = actor.Grants[:len(actor.Grants)-1]
	if w := request("GET", c.ID, ""); w.Code != 404 {
		t.Fatal("revoked own status retained operator fallback", w.Code)
	}
}
