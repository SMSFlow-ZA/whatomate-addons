package cases

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func fixture() Case {
	return Case{ID: "case-1", OrganizationID: "org", UnitID: "unit", ContactID: "contact", State: Submitted, Version: 1, Participants: []Participant{{ID: "contact", Role: Complainant}, {ID: "subject", Role: Subject}}, Assignments: []Assignment{{ID: "worker", Role: Handler}}}
}
func worker() Principal {
	return Principal{ID: "worker", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: WorkAssigned}}}
}

func TestAuthorizationDefaultDenyAndSubjectConflict(t *testing.T) {
	c := fixture()
	for _, p := range []Principal{{ID: "admin"}, {ID: "worker", Grants: []Grant{{OrganizationID: "other", UnitID: "unit", Permission: WorkAssigned}}}, {ID: "subject", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: ReadTriage}}}} {
		if CanRead(p, c) {
			t.Fatalf("unexpected access for %s", p.ID)
		}
	}
	if !CanRead(worker(), c) {
		t.Fatal("assigned scoped worker denied")
	}
	c.Assignments = append(c.Assignments, Assignment{ID: "subject", Role: Supervisor})
	p := worker()
	p.ID = "subject"
	if CanRead(p, c) {
		t.Fatal("subject gained access by assignment")
	}
	c.Assignments = nil
	if CanRead(worker(), c) {
		t.Fatal("unassigned case gained worker access")
	}
	p = Principal{ID: "triage", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: ReadTriage}}}
	if !CanRead(p, c) {
		t.Fatal("explicit triage denied")
	}
	c.Assignments = []Assignment{{ID: "different", Role: Handler}}
	if CanRead(p, c) {
		t.Fatal("triage grant opened assigned case")
	}
}

func TestLifecycleVersionAuditAndReopenReason(t *testing.T) {
	c := fixture()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, target := range []State{Assigned, InProgress, AwaitingInfo, InProgress, Escalated, Resolved, Closed} {
		next, err := Transition(c, worker(), c.Version, target, "", now, now.Add(-time.Hour))
		if err != nil {
			t.Fatalf("%s -> %s: %v", c.State, target, err)
		}
		if next.Version != c.Version+1 || next.Events[len(next.Events)-1].RecordedAt != now {
			t.Fatal("missing audit version/time")
		}
		c = next
	}
	if _, err := Transition(c, worker(), c.Version, InProgress, "", now, now); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("reopen without reason: %v", err)
	}
	next, err := Transition(c, worker(), c.Version, InProgress, "new evidence", now, now)
	if err != nil || next.ReopenReason != "new evidence" {
		t.Fatalf("reopen: %+v %v", next, err)
	}
	if _, err := Transition(c, worker(), 1, InProgress, "reason", now, now); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted")
	}
}

func TestRoutingSkipsSubjectsConflictsAndUsesBackup(t *testing.T) {
	c := fixture()
	c.Assignments = nil
	candidates := []Candidate{{Principal: Principal{ID: "subject", Grants: worker().Grants}, Role: Handler}, {Principal: worker(), Role: Handler, Conflict: true}, {Principal: Principal{ID: "backup", Grants: worker().Grants}, Role: Handler, Backup: true}}
	a, err := Route(c, candidates)
	if err != nil || a.ID != "backup" {
		t.Fatalf("routing: %+v %v", a, err)
	}
	if _, err := Route(c, candidates[:2]); !errors.Is(err, ErrNoRoute) {
		t.Fatal("failed to stop at manual routing boundary")
	}
}

func TestIntakeIdempotencyMultipleCasesAndAtomicOutbox(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	req := IntakeRequest{OrganizationID: "org", UnitID: "unit", ContactID: "contact", Key: "message-1"}
	first, err := s.Intake(context.Background(), req)
	if err != nil || first.CaseID == "" {
		t.Fatal(first, err)
	}
	again, err := s.Intake(context.Background(), req)
	if err != nil || first.CaseID != again.CaseID {
		t.Fatal("duplicate intake", again, err)
	}
	req.Key = "message-2"
	other, err := s.Intake(context.Background(), req)
	if err != nil || other.CaseID == first.CaseID {
		t.Fatal("one-case-per-contact restriction", other, err)
	}
	cases, outbox := r.Snapshot()
	if len(cases) != 2 || len(outbox) != 2 {
		t.Fatalf("atomic dedupe/outbox: %d %d", len(cases), len(outbox))
	}
	req.Key = "message-1"
	req.ContactID = "different"
	if _, err := s.Intake(context.Background(), req); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("reused key changed request: %v", err)
	}
}

func TestConcurrentIntakeAndCAS(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	req := IntakeRequest{OrganizationID: "org", UnitID: "unit", ContactID: "contact", Key: "message"}
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for range 20 {
		wg.Go(func() {
			v, e := s.Intake(context.Background(), req)
			if e != nil {
				t.Error(e)
			}
			ids <- v.CaseID
		})
	}
	wg.Wait()
	close(ids)
	var id string
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("concurrent duplicate")
		}
		id = v
	}
	c := fixture()
	r.Seed(c)
	successes := make(chan bool, 20)
	for range 20 {
		wg.Go(func() {
			_, e := s.ChangeState(context.Background(), worker(), c.ID, 1, Assigned, "", time.Now())
			successes <- e == nil
		})
	}
	wg.Wait()
	close(successes)
	n := 0
	for ok := range successes {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("CAS winners %d", n)
	}
}

type failedRepository struct{ outcome CommitOutcome }

func (f failedRepository) Begin(context.Context) (Transaction, error) {
	return &failedTx{Transaction: &memoryTx{cases: map[string]Case{}, intakes: map[string]intakeRecord{}, outbox: map[string]OutboxMessage{}}, outcome: f.outcome}, nil
}

type failedTx struct {
	Transaction
	outcome CommitOutcome
}

func (f *failedTx) Commit(context.Context) (CommitOutcome, error) {
	return f.outcome, errors.New("connection lost")
}
func (f *failedTx) Rollback(context.Context) error { return nil }
func TestCommitFailureNeverReturnsCaseID(t *testing.T) {
	for _, outcome := range []CommitOutcome{RolledBack, Unknown} {
		s := Service{Repository: failedRepository{outcome}}
		v, e := s.Intake(context.Background(), IntakeRequest{OrganizationID: "o", UnitID: "u", ContactID: "c", Key: "k"})
		if e == nil || v.CaseID != "" || v.Outcome != outcome {
			t.Fatalf("false success: %+v %v", v, e)
		}
	}
}

func TestAssignmentAndReadBoundaries(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	c := fixture()
	c.Assignments = nil
	r.Seed(c)
	if _, e := s.Read(context.Background(), Principal{ID: "admin"}, c.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("read bypass", e)
	}
	triage := Principal{ID: "triage", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: AssignTriage}}}
	if _, e := s.Assign(context.Background(), triage, c.ID, 1, []Candidate{{Principal: worker(), Role: Handler}}, time.Now()); e != nil {
		t.Fatal(e)
	}
	got, e := s.Read(context.Background(), worker(), c.ID)
	if e != nil || got.State != Assigned || got.Version != 2 || got.Events[0].ActorID != "triage" {
		t.Fatalf("assigned read/audit %+v %v", got, e)
	}
	if _, e := s.Assign(context.Background(), triage, c.ID, 2, []Candidate{{Principal: worker(), Role: Handler}}, time.Now()); !errors.Is(e, ErrDenied) {
		t.Fatalf("triage reassigned protected case: %v", e)
	}
	got.Participants[0].ID = "mutated"
	fresh, e := s.Read(context.Background(), worker(), c.ID)
	if e != nil || fresh.Participants[0].ID != "contact" {
		t.Fatal("aggregate mutation escaped transaction")
	}
}
func TestIntakeIncludesSubjectAndRejectsUnknownParticipantRole(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	req := IntakeRequest{OrganizationID: "o", UnitID: "u", ContactID: "c", Key: "k", Participants: []Participant{{ID: "person", Role: Subject}}}
	v, e := s.Intake(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	list, _ := r.Snapshot()
	if len(list) != 1 || len(list[0].Participants) != 2 || v.CaseID == "" {
		t.Fatal("missing subject link")
	}
	req.Key = "k2"
	req.Participants[0].Role = "owner"
	if _, e = s.Intake(context.Background(), req); !errors.Is(e, ErrInvalid) {
		t.Fatal("unknown role accepted", e)
	}
}
func TestRejectedTransitionNeverMutatesCase(t *testing.T) {
	c := fixture()
	before := c.Version
	if _, e := Transition(c, worker(), c.Version, Closed, "", time.Now(), time.Now()); !errors.Is(e, ErrInvalidTransition) {
		t.Fatal(e)
	}
	if c.Version != before || len(c.Events) != 0 || c.State != Submitted {
		t.Fatal("mutation on rejection")
	}
	c.State = Draft
	c.Assignments = nil
	p := Principal{ID: "contact", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: SubmitOwn}}}
	if _, e := Transition(c, p, c.Version, Submitted, "", time.Now(), time.Now()); e != nil {
		t.Fatal("own draft submit", e)
	}
}

func TestMemoryTransactionRollbackAndCancellation(t *testing.T) {
	r := NewMemoryRepository()
	tx, e := r.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	c := fixture()
	if _, e = tx.Intake(context.Background(), "org", "key", "fp", c, OutboxMessage{ID: "ack", CaseID: c.ID}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	list, outbox := r.Snapshot()
	if len(list) != 0 || len(outbox) != 0 {
		t.Fatal("rollback left partial writes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	tx, e = r.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := r.Begin(ctx); done <- e }()
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(100 * time.Millisecond):
		tx.Rollback(context.Background())
		t.Fatal("canceled transaction waited for unrelated lock")
	}
	tx.Rollback(context.Background())
}
func TestIntakeKeyComponentsCannotCollide(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	for _, req := range []IntakeRequest{{OrganizationID: "a", UnitID: "u", ContactID: "c", Key: "b\x00c"}, {OrganizationID: "a\x00b", UnitID: "u", ContactID: "c", Key: "c"}} {
		if _, e := s.Intake(context.Background(), req); e != nil {
			t.Fatal(e)
		}
	}
	list, _ := r.Snapshot()
	if len(list) != 2 {
		t.Fatal("composite key collided")
	}
}
