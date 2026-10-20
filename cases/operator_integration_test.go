//go:build integration

package cases

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLOperatorEditsRaceAndHistoricalFacts(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	s := Service{Repository: r}
	org := "operator-" + time.Now().Format("150405.000000000")
	req := IntakeRequest{OrganizationID: org, UnitID: "u", ContactID: "employee", Key: "m", Narrative: "private narrative", Snapshot: Classification{Zone: "original zone", Department: "original department", CaseType: "original type"}, Draft: true}
	result, e := s.Intake(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	own := Principal{ID: "employee", Grants: []Grant{{OrganizationID: org, UnitID: "u", Permission: SubmitOwn}}}
	c, e := s.SaveDraft(ctx, own, result.CaseID, 1, "edited narrative", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeState(ctx, own, c.ID, 2, Submitted, "", time.Now()); e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "operator", Grants: []Grant{{OrganizationID: org, UnitID: "u", Permission: AssignTriage}, {OrganizationID: org, UnitID: "u", Permission: ReadTriage}}}
	var wg sync.WaitGroup
	success := make(chan bool, 12)
	for range 12 {
		wg.Go(func() {
			_, e := s.CorrectCategory(ctx, p, c.ID, 3, "corrected type", "human review", time.Now())
			success <- e == nil
		})
	}
	wg.Wait()
	close(success)
	n := 0
	for v := range success {
		if v {
			n++
		}
	}
	if n != 1 {
		t.Fatal("CAS winners", n)
	}
	c, e = s.Read(ctx, p, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if c.Narrative != "edited narrative" || c.Snapshot != req.Snapshot || c.Category != "corrected type" {
		t.Fatal(c)
	}
	a := api.Actor{OrganizationID: org, UserID: "operator", SessionID: "s", MFA: true, Permissions: []string{string(ReadTriage)}, Grants: []api.Grant{{Permission: string(ReadTriage), UnitID: "u"}}}
	facts, e := ReportFacts(ctx, r.DB, a)
	if e != nil || len(facts) != 4 {
		t.Fatal(len(facts), e)
	}
	for _, f := range facts {
		if f.Snapshot != req.Snapshot {
			t.Fatal(f)
		}
	}
	tx, _ := r.Begin(ctx)
	defer tx.Rollback(ctx)
	c.Snapshot.Zone = "changed"
	c.Version++
	c.Events = append(c.Events, Event{Version: c.Version, ActorID: "operator", From: c.State, To: c.State, OrganizationID: org, UnitID: "u", RecordedAt: time.Now(), OccurredAt: time.Now()})
	if e = tx.Save(ctx, c, 4); e == nil {
		t.Fatal("snapshot changed")
	}
	b, _ := json.Marshal(EmployeeView(c))
	if string(b) == "" {
		t.Fatal("missing redaction")
	}
}

func TestSQLEmployeeHTTPRedactionAndRevocation(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	org := "redact-" + time.Now().Format("150405.000000000")
	s := Service{Repository: r}
	result, e := s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "u", ContactID: "employee", Key: "m", Narrative: "secret internal narrative", Snapshot: Classification{CaseType: "secret-category"}, Participants: []Participant{{ID: "secret subject", Role: Subject}}})
	if e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: org, UserID: "employee", SessionID: "s", MFA: true, Permissions: []string{string(ReadOwn)}, Grants: []api.Grant{{Permission: string(ReadOwn), UnitID: "u"}}}
	h := NewHTTPHandler(r.DB, HTTPConfig{Enabled: true})
	req := httptest.NewRequest("GET", "/api/extensions/cases/"+result.CaseID, nil)
	req = req.WithContext(api.WithActor(req.Context(), a))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "employee") || strings.Contains(w.Body.String(), "narrative") {
		t.Fatal(w.Code, w.Body.String())
	}
	hits, e := ListCurrent(ctx, r.DB, a, "secret-category")
	if e != nil || len(hits) != 0 {
		t.Fatal("own category search disclosed internal classification", e)
	}
	facts, e := ReportFacts(ctx, r.DB, a)
	if e != nil || len(facts) != 0 {
		t.Fatal("own case facts disclosed internal dimensions", e)
	}
	a.Grants = nil
	req = req.WithContext(api.WithActor(req.Context(), a))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	req = httptest.NewRequest("GET", "/api/extensions/cases/unknown", nil)
	req = req.WithContext(api.WithActor(req.Context(), a))
	other := httptest.NewRecorder()
	h.ServeHTTP(other, req)
	if other.Code != w.Code || other.Body.String() != w.Body.String() {
		t.Fatal("existence disclosed")
	}
}

func TestSQLHTTPVerifiedConflictAndBackupAssignment(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	org := "http-routing-" + time.Now().Format("150405.000000000")
	result, e := (Service{Repository: r}).Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "u", ContactID: "employee", Key: "key"})
	if e != nil {
		t.Fatal(e)
	}
	triage := api.Actor{OrganizationID: org, UserID: "triage", SessionID: "s", MFA: true, Permissions: []string{string(ReadTriage), string(AssignTriage)}, Grants: []api.Grant{{Permission: string(ReadTriage), UnitID: "u"}, {Permission: string(AssignTriage), UnitID: "u"}}}
	h := NewHTTPHandler(r.DB, HTTPConfig{Enabled: true, ResolveCandidate: func(_ context.Context, a api.Actor, id, unit string) (Principal, error) {
		if a.OrganizationID != org || unit != "u" || (id != "subject" && id != "backup") {
			return Principal{}, ErrDenied
		}
		return Principal{ID: id, Grants: []Grant{{OrganizationID: org, UnitID: "u", Permission: WorkAssigned}}}, nil
	}})
	request := func(actor api.Actor, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/extensions/cases/"+path, strings.NewReader(body))
		req = req.WithContext(api.WithActor(req.Context(), actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := request(triage, "POST", result.CaseID+"/participants", `{"expected_version":1,"candidate_id":"subject","participant_role":"subject","reason":"verified conflict"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	conflicted := triage
	conflicted.UserID = "subject"
	w = request(conflicted, "GET", result.CaseID, "")
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(triage, "POST", result.CaseID+"/assignment", `{"expected_version":2,"candidate_id":"subject","backup_ids":["backup"],"role":"handler","reason":"route without subject"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	tx, _ := r.Begin(ctx)
	defer tx.Rollback(ctx)
	c, e := tx.Load(ctx, result.CaseID)
	if e != nil || len(c.Assignments) != 1 || c.Assignments[0].ID != "backup" || c.Version != 3 {
		t.Fatal(c, e)
	}
	w = request(conflicted, "GET", result.CaseID, "")
	if w.Code != 404 {
		t.Fatal("subject was allowed after routing")
	}
}

func TestSQLLegacyMissingSnapshotCanTransitionWithoutReclassification(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	s := Service{Repository: r}
	org := "legacy-snapshot-" + time.Now().Format("150405.000000000")
	result, e := s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "u", ContactID: "e", Key: "m", Draft: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.DB.ExecContext(ctx, `UPDATE addon_cases_case SET payload=payload-'Snapshot' WHERE id=$1`, result.CaseID); e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "e", Grants: []Grant{{OrganizationID: org, UnitID: "u", Permission: SubmitOwn}}}
	if _, e = s.ChangeState(ctx, p, result.CaseID, 1, Submitted, "", time.Now()); e != nil {
		t.Fatal("legacy transition blocked", e)
	}
}

func TestSQLAggregateOnlyBoundaryAndOpaqueKeys(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	s := Service{Repository: r}
	org := "aggregate-" + time.Now().Format("150405.000000000")
	result, e := s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "u", ContactID: "employee", Key: "m", Narrative: "private", Snapshot: Classification{Zone: "z", Department: "d", CaseType: "c"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "other", ContactID: "private", Key: "other"}); e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: org, UserID: "leader", SessionID: "s", MFA: true, Permissions: []string{"reports:aggregate"}, Grants: []api.Grant{{Permission: "reports:aggregate", UnitID: "u"}}}
	facts, e := AggregateFacts(ctx, r.DB, a)
	if e != nil || len(facts) != 1 {
		t.Fatal(len(facts), e)
	}
	if facts[0].CaseID == result.CaseID || facts[0].UnitID != "u" || facts[0].Snapshot.CaseType != "c" {
		t.Fatal(facts)
	}
	second, _ := AggregateFacts(ctx, r.DB, a)
	if second[0].CaseID != facts[0].CaseID {
		t.Fatal("unstable opaque case key")
	}
	if _, e = s.Read(ctx, PrincipalForActor(a), result.CaseID); e != ErrDenied {
		t.Fatal("aggregate granted case read", e)
	}
	direct, e := ListCurrent(ctx, r.DB, a, "")
	if e != nil || len(direct) != 0 {
		t.Fatal("aggregate granted inbox", e)
	}
	raw, e := ReportFacts(ctx, r.DB, a)
	if e != nil || len(raw) != 0 {
		t.Fatal("aggregate granted raw facts", e)
	}
	req := httptest.NewRequest("GET", "/api/extensions/cases/facts", nil)
	req = req.WithContext(api.WithActor(req.Context(), a))
	w := httptest.NewRecorder()
	NewHTTPHandler(r.DB, HTTPConfig{Enabled: true}).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("aggregate exposed raw HTTP facts", w.Code)
	}
	a.Grants = []api.Grant{{Permission: "reports:aggregate", UnitID: "*"}}
	facts, e = AggregateFacts(ctx, r.DB, a)
	if e != nil || len(facts) != 0 {
		t.Fatal("wildcard aggregate unit", e)
	}
}
