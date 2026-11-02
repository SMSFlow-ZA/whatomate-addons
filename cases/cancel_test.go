package cases

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCancelDraftOwnAuthorityReplayAndTerminal(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	s := Service{Repository: repo}
	result, e := s.Intake(ctx, IntakeRequest{OrganizationID: "org", UnitID: "unit", ContactID: "employee", Key: "intake", Draft: true, Narrative: "private draft"})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "employee", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: SubmitOwn}, {OrganizationID: "org", UnitID: "unit", Permission: ReadOwn}}}
	_, outbox := repo.Snapshot()
	if len(outbox) != 0 {
		t.Fatal("draft generated acknowledgement")
	}
	other := p
	other.ID = "other"
	if _, e = s.CancelDraft(ctx, other, result.CaseID, 1, "cancel"); !errors.Is(e, ErrDenied) {
		t.Fatal("other owner", e)
	}
	if _, e = s.CancelDraft(ctx, p, result.CaseID, 2, "cancel"); !errors.Is(e, ErrConflict) {
		t.Fatal("stale", e)
	}
	got, e := s.CancelDraft(ctx, p, result.CaseID, 1, "cancel")
	if e != nil || got.State != Cancelled || got.Version != 2 {
		t.Fatal(got, e)
	}
	replay, e := s.CancelDraft(ctx, p, result.CaseID, 1, "cancel")
	if e != nil || len(replay.Events) != 2 || replay.Version != 2 {
		t.Fatal("replay changed history", e)
	}
	if _, e = s.CancelDraft(ctx, p, result.CaseID, 2, "cancel"); !errors.Is(e, ErrIdempotencyConflict) {
		t.Fatal("changed command", e)
	}
	revoked := p
	revoked.Grants = nil
	if _, e = s.CancelDraft(ctx, revoked, result.CaseID, 1, "cancel"); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked replay", e)
	}
	if _, e = s.ChangeState(ctx, p, result.CaseID, 2, Submitted, "", time.Now()); e == nil {
		t.Fatal("cancelled submitted")
	}
	_, outbox = repo.Snapshot()
	if len(outbox) != 0 {
		t.Fatal("cancellation generated acknowledgement")
	}
}

func TestDraftSubmissionEnqueuesAcknowledgementOnce(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	s := Service{Repository: repo}
	r, e := s.Intake(ctx, IntakeRequest{OrganizationID: "org", UnitID: "unit", ContactID: "employee", Key: "draft", Draft: true})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "employee", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: SubmitOwn}, {OrganizationID: "org", UnitID: "unit", Permission: ReadOwn}}}
	if _, e = s.ChangeState(ctx, p, r.CaseID, 1, Submitted, "", time.Now()); e != nil {
		t.Fatal(e)
	}
	_, out := repo.Snapshot()
	if len(out) != 1 || out[0].Kind != "case.intake.acknowledgement" {
		t.Fatal("missing submission acknowledgement", out)
	}
	if _, e = s.ChangeState(ctx, p, r.CaseID, 1, Submitted, "", time.Now()); e == nil {
		t.Fatal("stale repeated submit")
	}
	_, out = repo.Snapshot()
	if len(out) != 1 {
		t.Fatal("duplicate acknowledgement")
	}
}
