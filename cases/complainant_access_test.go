package cases

import (
	"context"
	"errors"
	"testing"
	"time"
)

func complainantOperatorFixture(alias bool) (Principal, Case) {
	p := Principal{ID: "operator", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: WorkAssigned}, {OrganizationID: "org", UnitID: "unit", Permission: ReadTriage}, {OrganizationID: "org", UnitID: "unit", Permission: AssignTriage}}}
	id := p.ID
	if alias {
		id = "historic-employee"
		p.Aliases = []string{id}
	}
	c := Case{ID: "own-complaint", OrganizationID: "org", UnitID: "unit", ContactID: id, State: Assigned, Version: 1, Narrative: "private complaint", Participants: []Participant{{ID: id, Role: Complainant}}, Assignments: []Assignment{{ID: p.ID, Role: Handler}}}
	return p, c
}

func TestComplainantCannotAcquireOperatorAccessFromAssignmentOrTriage(t *testing.T) {
	for _, alias := range []bool{false, true} {
		for _, assignedCase := range []bool{false, true} {
			p, c := complainantOperatorFixture(alias)
			if !assignedCase {
				c.Assignments = nil
				c.State = Submitted
			}
			if CanRead(p, c) {
				t.Error("complainant gained operator read", alias, assignedCase)
			}
			if CanWork(p, c) {
				t.Error("complainant gained operator work", alias, assignedCase)
			}
			if CanNotifyHandler(p, c) {
				t.Error("complainant gained handler notification", alias, assignedCase)
			}
			if canManage(p, c) {
				t.Error("complainant gained triage management", alias, assignedCase)
			}
			if privacyOperatorRead(p, c) {
				t.Error("complainant gained operator privacy read", alias, assignedCase)
			}
			if _, ok := safeView(p, c).(EmployeeDTO); !ok {
				t.Error("complainant received full operator DTO", alias, assignedCase)
			}
			backup := Principal{ID: "independent", Grants: p.Grants}
			route, err := Route(c, []Candidate{{Principal: p, Role: Handler}, {Principal: backup, Role: Handler, Backup: true}})
			if err != nil || route.ID != backup.ID {
				t.Error("complainant bypassed independent backup", route, err)
			}
		}
	}
}

func TestComplainantKeepsOnlyExplicitOwnStatusAndDraftSubmission(t *testing.T) {
	p, c := complainantOperatorFixture(true)
	p.Grants = append(p.Grants, Grant{OrganizationID: "org", UnitID: "unit", Permission: ReadOwn}, Grant{OrganizationID: "org", UnitID: "unit", Permission: SubmitOwn})
	if !CanRead(p, c) {
		t.Fatal("explicit own status denied")
	}
	if _, ok := safeView(p, c).(EmployeeDTO); !ok {
		t.Fatal("own status leaked operator content")
	}
	now := time.Now()
	if _, err := Transition(c, p, c.Version, InProgress, "work own complaint", now, now); !errors.Is(err, ErrDenied) {
		t.Fatal("own complaint work transition allowed", err)
	}
	c.State = Draft
	c.Assignments = nil
	if _, err := Transition(c, p, c.Version, Submitted, "submit own draft", now, now); err != nil {
		t.Fatal("own submission denied", err)
	}
	repository := NewMemoryRepository()
	repository.Seed(c)
	service := Service{Repository: repository}
	if _, err := service.SaveDraft(context.Background(), p, c.ID, c.Version, "updated own draft", now); err != nil {
		t.Fatal("own draft update denied", err)
	}
	c.Participants = append(c.Participants, Participant{ID: p.Aliases[0], Role: Subject})
	if CanRead(p, c) {
		t.Fatal("explicit subject still must deny own status")
	}
}

func TestComplainantCannotManageEscalateOrEmergencyReadOwnComplaint(t *testing.T) {
	p, c := complainantOperatorFixture(true)
	repository := NewMemoryRepository()
	repository.Seed(c)
	service := Service{Repository: repository}
	ctx := context.Background()
	now := time.Now()
	backup := Principal{ID: "independent", Grants: p.Grants}
	mutations := []func() error{
		func() error { _, e := service.CorrectCategory(ctx, p, c.ID, 1, "changed", "reason", now); return e },
		func() error {
			_, e := service.Reassign(ctx, p, c.ID, 1, []Candidate{{Principal: backup, Role: Handler}}, "reason", now)
			return e
		},
		func() error {
			_, e := service.Escalate(ctx, p, c.ID, 1, []Candidate{{Principal: backup, Role: Handler}}, "reason", now)
			return e
		},
		func() error {
			_, e := service.LinkParticipant(ctx, p, c.ID, 1, backup, Subject, "reason", now)
			return e
		},
	}
	for i, mutation := range mutations {
		if err := mutation(); !errors.Is(err, ErrDenied) {
			t.Errorf("own operator mutation %d: %v", i, err)
		}
	}
	approved := false
	if _, err := service.ReadEmergency(ctx, p, c.ID, func(context.Context, string) error { approved = true; return nil }); !errors.Is(err, ErrDenied) || approved {
		t.Fatal("complainant emergency read bypass", err, approved)
	}
	c.Assignments = nil
	c.State = Submitted
	repository.Seed(c)
	if _, err := service.Assign(ctx, p, c.ID, 1, []Candidate{{Principal: backup, Role: Handler}}, now); !errors.Is(err, ErrDenied) {
		t.Fatal("complainant triage assignment allowed", err)
	}
}

func TestParticipantSupervisorAloneIsNotAnOwnComplaintConflict(t *testing.T) {
	p, c := complainantOperatorFixture(true)
	c.Participants = []Participant{{ID: "another-person", Role: Complainant}, {ID: p.Aliases[0], Role: ParticipantSupervisor}}
	if !CanRead(p, c) || !CanWork(p, c) || !CanNotifyHandler(p, c) || !canManage(p, c) {
		t.Fatal("separately scoped assigned supervisor denied")
	}
	if _, ok := safeView(p, c).(OperatorDTO); !ok {
		t.Fatal("legitimate operator view denied")
	}
	if _, err := Route(c, []Candidate{{Principal: p, Role: Handler}}); err != nil {
		t.Fatal(err)
	}
}
