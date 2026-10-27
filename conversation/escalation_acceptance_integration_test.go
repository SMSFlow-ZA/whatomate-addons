//go:build integration

package conversation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/cases"
)

func timerAcceptanceFixture(t *testing.T) (context.Context, Service, cases.Service, cases.Principal, cases.Case) {
	t.Helper()
	ctx, s, tenant := fixture(t)
	s.Config.EscalationAfter = time.Minute
	var turn Turn
	for _, in := range []struct{ id, text string }{{"begin", "LOG CASE"}, {"narrative", "A fictional concern requiring a human review"}, {"submit", "CONFIRM"}} {
		var err error
		turn, err = s.Receive(ctx, Inbound{Tenant: tenant, Account: "synthetic", Sender: "+27820000001", ProviderMessageID: in.id, Text: in.text})
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	p := cases.Principal{ID: "timer-triage", Grants: []cases.Grant{
		{OrganizationID: tenant, UnitID: "zone", Permission: cases.AssignTriage},
		{OrganizationID: tenant, UnitID: "zone", Permission: cases.ReadTriage},
		{OrganizationID: tenant, UnitID: "zone", Permission: cases.WorkAssigned},
	}}
	c, err := svc.Read(ctx, p, turn.Selected)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.LoadEscalationCase = func(ctx context.Context, org, unit, id string) (cases.Case, error) {
		if org != tenant || unit != "zone" {
			return cases.Case{}, cases.ErrDenied
		}
		return svc.Read(ctx, p, id)
	}
	return ctx, s, svc, p, c
}

func timerState(t *testing.T, ctx context.Context, s Service, c cases.Case) string {
	t.Helper()
	var state string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM addon_conversation_timer WHERE tenant=$1 AND case_id=$2`, c.OrganizationID, c.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestPostgresDueTimerConcurrentConflictBackupOnce(t *testing.T) {
	ctx, s, svc, p, c := timerAcceptanceFixture(t)
	var err error
	c, err = svc.LinkParticipant(ctx, p, c.ID, c.Version, cases.Principal{ID: "conflicted"}, cases.Subject, "verified fictional conflict", s.Config.Clock())
	if err != nil {
		t.Fatal(err)
	}
	var resolved atomic.Int32
	s.Config.ResolveCandidates = func(context.Context, cases.Case) (cases.Principal, []cases.Candidate, error) {
		resolved.Add(1)
		grant := []cases.Grant{{OrganizationID: c.OrganizationID, UnitID: c.UnitID, Permission: cases.WorkAssigned}}
		return p, []cases.Candidate{
			{Principal: cases.Principal{ID: "subject-alias", Aliases: []string{"conflicted"}, Grants: grant}, Role: cases.Handler},
			{Principal: cases.Principal{ID: "wrong-unit", Grants: []cases.Grant{{OrganizationID: c.OrganizationID, UnitID: "other", Permission: cases.WorkAssigned}}}, Role: cases.Handler, Backup: true},
			{Principal: cases.Principal{ID: "backup", Grants: grant}, Role: cases.Handler, Backup: true},
		}, nil
	}
	if n, err := s.EscalateDue(ctx); err != nil || n != 0 || resolved.Load() != 0 || timerState(t, ctx, s, c) != "pending" {
		t.Fatal("future timer ran", n, err)
	}
	now := s.Config.Clock()
	s.Config.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); n, e := s.EscalateDue(ctx); counts <- n; errs <- e }()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for n := range counts {
		total += n
	}
	if total != 1 || resolved.Load() != 1 || timerState(t, ctx, s, c) != "handed_off" {
		t.Fatal("timer duplicated", total, resolved.Load())
	}
	backup := cases.Principal{ID: "backup", Grants: []cases.Grant{{OrganizationID: c.OrganizationID, UnitID: c.UnitID, Permission: cases.WorkAssigned}}}
	after, err := svc.Read(ctx, backup, c.ID)
	if err != nil || after.State != cases.Escalated || after.Version != c.Version+1 || len(after.Assignments) != 1 || after.Assignments[0].ID != "backup" {
		t.Fatal("wrong conflict route", after, err)
	}
	var facts int
	if err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_conversation_activity WHERE tenant=$1 AND case_id=$2 AND payload->>'kind'='handoff'`, c.OrganizationID, c.ID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatal("handoff projection duplicated or absent", facts)
	}
	if n, err := s.EscalateDue(ctx); err != nil || n != 0 || resolved.Load() != 1 {
		t.Fatal("completed timer replayed", n, err)
	}
}

func TestPostgresTimerNoEligibleRouteHeldWithoutAssignment(t *testing.T) {
	ctx, s, svc, p, c := timerAcceptanceFixture(t)
	var calls atomic.Int32
	s.Config.ResolveCandidates = func(context.Context, cases.Case) (cases.Principal, []cases.Candidate, error) {
		calls.Add(1)
		return p, []cases.Candidate{{Principal: p, Role: cases.Handler, Conflict: true}}, nil
	}
	now := s.Config.Clock()
	s.Config.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	if n, err := s.EscalateDue(ctx); err != nil || n != 0 || timerState(t, ctx, s, c) != "held" {
		t.Fatal(n, err)
	}
	after, err := svc.Read(ctx, p, c.ID)
	if err != nil || after.Version != c.Version || after.State != c.State || len(after.Assignments) != 0 {
		t.Fatal("unroutable case changed", after, err)
	}
	if n, err := s.EscalateDue(ctx); err != nil || n != 0 || calls.Load() != 1 {
		t.Fatal("held route blindly retried", n, err)
	}
}

func TestPostgresTimerResolvedCompletesWithoutRouting(t *testing.T) {
	ctx, s, svc, p, c := timerAcceptanceFixture(t)
	var err error
	c, err = svc.Assign(ctx, p, c.ID, c.Version, []cases.Candidate{{Principal: p, Role: cases.Handler}}, s.Config.Clock())
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []cases.State{cases.InProgress, cases.Resolved} {
		c, err = svc.ChangeState(ctx, p, c.ID, c.Version, state, "fictional acceptance resolution", s.Config.Clock())
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Config.ResolveCandidates = func(context.Context, cases.Case) (cases.Principal, []cases.Candidate, error) {
		t.Error("resolved case routed")
		return p, nil, nil
	}
	now := s.Config.Clock()
	s.Config.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	if n, err := s.EscalateDue(ctx); err != nil || n != 1 || timerState(t, ctx, s, c) != "complete" {
		t.Fatal(n, err)
	}
	after, err := svc.Read(ctx, p, c.ID)
	if err != nil || after.Version != c.Version || after.State != cases.Resolved {
		t.Fatal("resolved case mutated", after, err)
	}
}

func TestPostgresTimerUnknownOutcomeNeverReplays(t *testing.T) {
	ctx, s, svc, p, c := timerAcceptanceFixture(t)
	// Reproduce the retained state after intent persistence and a committed case
	// mutation but before completion bookkeeping, without pretending a real crash.
	if _, err := s.DB.ExecContext(ctx, `UPDATE addon_conversation_timer SET state='held_unknown' WHERE tenant=$1 AND case_id=$2`, c.OrganizationID, c.ID); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Escalate(ctx, p, c.ID, c.Version, []cases.Candidate{{Principal: p, Role: cases.Handler}}, "synthetic ambiguous completion", s.Config.Clock())
	if err != nil {
		t.Fatal(err)
	}
	s.Config.ResolveCandidates = func(context.Context, cases.Case) (cases.Principal, []cases.Candidate, error) {
		t.Error("unknown timer replayed")
		return p, nil, nil
	}
	now := s.Config.Clock()
	s.Config.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	if n, err := s.EscalateDue(ctx); err != nil || n != 0 || timerState(t, ctx, s, c) != "held_unknown" {
		t.Fatal(n, err)
	}
	current, err := svc.Read(ctx, p, c.ID)
	if err != nil || current.Version != after.Version || len(current.Events) != len(after.Events) {
		t.Fatal("unknown case replayed", current, err)
	}
}
