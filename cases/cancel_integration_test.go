//go:build integration

package cases

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLDraftCancellationCASReplayAndLegacyAcknowledgement(t *testing.T) {
	repo := integrationRepository(t)
	ctx := context.Background()
	org := "cancel-" + time.Now().Format("150405.000000000")
	s := Service{Repository: repo}
	r, e := s.Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "unit", ContactID: "employee", Key: "draft", Draft: true})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "employee", Grants: []Grant{{OrganizationID: org, UnitID: "unit", Permission: SubmitOwn}, {OrganizationID: org, UnitID: "unit", Permission: ReadOwn}}}
	if e = repo.MigrateOutboxDelivery(ctx); e != nil {
		t.Fatal(e)
	}
	// Reproduce a historical pending draft intent; preserve it without sending.
	m := OutboxMessage{ID: r.CaseID + "-ack", OrganizationID: org, CaseID: r.CaseID, ContactID: "employee", Kind: "case.intake.acknowledgement", CreatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(m)
	if _, e = repo.DB.Exec(`INSERT INTO addon_cases_outbox(id,organization_id,case_id,contact_id,kind,created_at,payload)VALUES($1,$2,$3,$4,$5,$6,$7)`, m.ID, org, m.CaseID, m.ContactID, m.Kind, m.CreatedAt, string(raw)); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if _, ok, e := repo.ClaimOutbox(ctx, org, "worker", now, now.Add(time.Minute)); e != nil || ok {
		t.Fatal("legacy draft acknowledgement claimed", e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, e := s.CancelDraft(ctx, p, r.CaseID, 1, "same"); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	c, e := s.CancelDraft(ctx, p, r.CaseID, 1, "same")
	if e != nil || c.State != Cancelled || c.Version != 2 || len(c.Events) != 2 {
		t.Fatal("CAS replay", e)
	}
	var events int
	if e = repo.DB.QueryRow(`SELECT count(*) FROM addon_cases_event WHERE case_id=$1`, r.CaseID).Scan(&events); e != nil || events != 2 {
		t.Fatal("duplicate cancellation event", events, e)
	}
	now = time.Now().UTC()
	if _, ok, e := repo.ClaimOutbox(ctx, org, "worker", now, now.Add(time.Minute)); e != nil || ok {
		t.Fatal("cancelled acknowledgement claimed", e)
	}
	p.Grants = nil
	if _, e = s.CancelDraft(ctx, p, r.CaseID, 1, "same"); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked replay", e)
	}
}

func TestHTTPDraftCancellationStrictReplayAndRedactedReceipt(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	org := "cancel-http-" + time.Now().Format("150405.000000000")
	created, e := (Service{Repository: r}).Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "unit", ContactID: "employee", Key: "draft", Draft: true, Narrative: "private narrative"})
	if e != nil {
		t.Fatal(e)
	}
	actor := api.Actor{OrganizationID: org, UserID: "employee", SessionID: "session", PrimarySessionAuthorized: true, Permissions: []string{"cases:read.own", "cases:submit.own"}, Grants: []api.Grant{{Permission: "cases:read.own", UnitID: "unit"}, {Permission: "cases:submit.own", UnitID: "unit"}}}
	h := NewHTTPHandler(r.DB, HTTPConfig{Enabled: true})
	call := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/extensions/cases/"+id+"/cancel-draft", strings.NewReader(body))
		req = req.WithContext(api.WithActor(ctx, actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := call(created.CaseID, `{"expected_version":1,"idempotency_key":"cancel","narrative":"ignored extra"}`); w.Code != 400 {
		t.Fatal("irrelevant command field accepted", w.Code)
	}
	body := `{"expected_version":1,"idempotency_key":"cancel"}`
	first := call(created.CaseID, body)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	second := call(created.CaseID, body)
	if second.Code != 200 || first.Body.String() != second.Body.String() {
		t.Fatal("replay response changed")
	}
	var response struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(first.Body.Bytes(), &response) != nil || len(response.Data) != 3 || response.Data["state"] != "Cancelled" {
		t.Fatal("private receipt fields exposed", first.Body.String())
	}
	actor.UserID = "other"
	denied := call(created.CaseID, body)
	unknown := call("absent", body)
	if denied.Code != 404 || unknown.Code != 404 || denied.Body.String() != unknown.Body.String() {
		t.Fatal("unknown and unauthorized differ")
	}
}
