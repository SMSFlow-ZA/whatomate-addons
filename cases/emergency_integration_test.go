//go:build integration

package cases

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSQLEmergencyHTTPApprovalBindingAndReadOnly(t *testing.T) {
	r := integrationRepository(t)
	s := Service{Repository: r}
	ctx := context.Background()
	org := "emergency-" + time.Now().Format("150405.000000000")
	result, e := s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "unit", ContactID: "employee", Key: "intake", Narrative: "protected emergency narrative", Participants: []Participant{{ID: "employee-subject", Role: Subject}}})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "triage", Grants: []Grant{{OrganizationID: org, UnitID: "unit", Permission: AssignTriage}, {OrganizationID: org, UnitID: "unit", Permission: ReadTriage}}}
	worker := Principal{ID: "other-worker", Grants: []Grant{{OrganizationID: org, UnitID: "unit", Permission: WorkAssigned}}}
	assigned, e := s.Assign(ctx, p, result.CaseID, 1, []Candidate{{Principal: worker, Role: Handler}}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	before, _ := json.Marshal(assigned)
	actor := api.Actor{OrganizationID: org, UserID: "triage", SessionID: "signed-session", MFA: true, Permissions: []string{string(ReadTriage)}, Grants: []api.Grant{{Permission: string(ReadTriage), UnitID: "unit"}}}
	calls := 0
	handler := NewHTTPHandler(r.DB, HTTPConfig{Enabled: true, AuthorizeEmergency: func(_ context.Context, a api.Actor, caseID, approvalID string) error {
		calls++
		if a.UserID != actor.UserID || caseID != result.CaseID || approvalID != "durable-approval-for-exact-case" {
			return ErrDenied
		}
		return nil
	}})
	request := func(a api.Actor, id, body string) httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/extensions/cases/"+id+"/emergency-read", strings.NewReader(body))
		req = req.WithContext(api.WithActor(ctx, a))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return *rec
	}
	normal := httptest.NewRequest("GET", "/api/extensions/cases/"+result.CaseID, nil).WithContext(api.WithActor(ctx, actor))
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, normal)
	if denied.Code != 404 {
		t.Fatal("ordinary triage read leaked assigned case", denied.Code)
	}
	approved := request(actor, result.CaseID, `{"approval_id":"durable-approval-for-exact-case"}`)
	if approved.Code != 200 || !strings.Contains(approved.Body.String(), "protected emergency narrative") || calls != 1 {
		t.Fatalf("approved read: %d %s", approved.Code, approved.Body)
	}
	for _, body := range []string{`{}`, `{"approval_id":""}`, `{"approval_id":"wrong"}`, `{"approval_id":"durable-approval-for-exact-case","permissions":["cases:read.triage"]}`, `{"approval_id":"durable-approval-for-exact-case","expected_version":2}`} {
		rec := request(actor, result.CaseID, body)
		if rec.Code != 404 {
			t.Fatalf("invalid approval %d %s", rec.Code, rec.Body)
		}
	}
	missing := request(actor, "unknown-case", `{"approval_id":"durable-approval-for-exact-case"}`)
	if missing.Code != 404 {
		t.Fatal("unknown case disclosed", missing.Code)
	}
	for _, a := range []api.Actor{func() api.Actor { x := actor; x.OrganizationID = "other"; return x }(), func() api.Actor {
		x := actor
		x.Grants = []api.Grant{{Permission: string(ReadTriage), UnitID: "other"}}
		return x
	}(), func() api.Actor { x := actor; x.Aliases = []string{"employee-subject"}; return x }()} {
		rec := request(a, result.CaseID, `{"approval_id":"durable-approval-for-exact-case"}`)
		if rec.Code != 404 {
			t.Fatalf("scope/subject leaked %d %s", rec.Code, rec.Body)
		}
	}
	tx, e := r.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	current, e := tx.Load(ctx, result.CaseID)
	tx.Rollback(ctx)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := json.Marshal(current)
	if string(before) != string(after) {
		t.Fatal("emergency read mutated assignment/version/events")
	}
	if _, e = s.ReadEmergency(ctx, p, result.CaseID, func(context.Context, string) error { return errors.New("revoked approval") }); !errors.Is(e, ErrDenied) {
		t.Fatal("revocation ignored", e)
	}
	// A durable approval for the first case cannot authorize another real case.
	other, e := s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "unit", ContactID: "second-employee", Key: "second", Narrative: "second protected narrative"})
	if e != nil {
		t.Fatal(e)
	}
	wrongCase := request(actor, other.CaseID, `{"approval_id":"durable-approval-for-exact-case"}`)
	if wrongCase.Code != 404 {
		t.Fatal("approval crossed case boundary", wrongCase.Code)
	}
	withoutCallback := NewHTTPHandler(r.DB, HTTPConfig{Enabled: true})
	missingApproval := httptest.NewRequest("POST", "/api/extensions/cases/"+result.CaseID+"/emergency-read", strings.NewReader(`{"approval_id":"durable-approval-for-exact-case"}`)).WithContext(api.WithActor(ctx, actor))
	missingRec := httptest.NewRecorder()
	withoutCallback.ServeHTTP(missingRec, missingApproval)
	if missingRec.Code != 404 {
		t.Fatal("unconfigured approval bypass", missingRec.Code)
	}
	// A normal owner transaction can link a subject while the independent
	// approval is checked. The second read must reject the changed case.
	linked := false
	_, e = s.ReadEmergency(ctx, p, result.CaseID, func(context.Context, string) error {
		_, err := s.LinkParticipant(ctx, worker, result.CaseID, assigned.Version, Principal{ID: p.ID}, Subject, "independent subject link during approval", time.Now())
		linked = err == nil
		return err
	})
	if !linked || !errors.Is(e, ErrDenied) {
		t.Fatal("concurrent owner subject link escaped recheck", linked, e)
	}
}
