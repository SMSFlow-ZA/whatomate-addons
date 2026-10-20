package cases

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDurableEditsRequireScopeAndVersionAndPreserveCreationFacts(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	created, e := s.Intake(context.Background(), IntakeRequest{OrganizationID: "o", UnitID: "u", ContactID: "employee", Key: "m", Narrative: "sensitive", Snapshot: Classification{Zone: "z", Department: "d", CaseType: "original"}})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "operator", Grants: []Grant{{OrganizationID: "o", UnitID: "u", Permission: AssignTriage}}}
	c, e := s.CorrectCategory(context.Background(), p, created.CaseID, 1, "corrected", "human correction", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if c.Snapshot.CaseType != "original" || c.Category != "corrected" || len(c.Events) != 2 {
		t.Fatal(c)
	}
	if _, e = s.CorrectCategory(context.Background(), p, c.ID, 1, "x", "reason", time.Now()); e != ErrConflict {
		t.Fatal(e)
	}
	p.ID = "employee"
	c.Participants = append(c.Participants, Participant{ID: p.ID, Role: Subject})
	r.Seed(c)
	if _, e = s.CorrectCategory(context.Background(), p, c.ID, 2, "x", "reason", time.Now()); e != ErrDenied {
		t.Fatal(e)
	}
}

func TestEmployeeViewContainsNoInternalIdentityOrNarrative(t *testing.T) {
	c := Case{ID: "safe", State: Submitted, Version: 1, ContactID: "private", Narrative: "secret", Participants: []Participant{{ID: "subject", Role: Subject}}, Assignments: []Assignment{{ID: "operator", Role: Handler}}, Events: []Event{{ActorID: "actor", Reason: "confidential"}}}
	v := EmployeeView(c)
	if v.ID != "safe" || v.Version != 1 {
		t.Fatal(v)
	}
}

func TestVerifiedSubjectLinkOverridesGrantAndRoutesBackup(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	ctx := context.Background()
	result, e := s.Intake(ctx, IntakeRequest{OrganizationID: "o", UnitID: "u", ContactID: "employee", Key: "m"})
	if e != nil {
		t.Fatal(e)
	}
	triage := Principal{ID: "triage", Grants: []Grant{{OrganizationID: "o", UnitID: "u", Permission: AssignTriage}, {OrganizationID: "o", UnitID: "u", Permission: ReadTriage}}}
	subjectPrincipal := Principal{ID: "subject", Grants: []Grant{{OrganizationID: "o", UnitID: "u", Permission: WorkAssigned}, {OrganizationID: "o", UnitID: "u", Permission: ReadTriage}}}
	c, e := s.LinkParticipant(ctx, triage, result.CaseID, 1, subjectPrincipal, Subject, "verified conflict", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if CanRead(subjectPrincipal, c) {
		t.Fatal("subject read")
	}
	backup := Principal{ID: "backup", Grants: []Grant{{OrganizationID: "o", UnitID: "u", Permission: WorkAssigned}}}
	c, e = s.Reassign(ctx, triage, c.ID, 2, []Candidate{{Principal: subjectPrincipal, Role: Handler}, {Principal: backup, Role: Handler, Backup: true}}, "conflict-free backup", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if c.Assignments[0].ID != "backup" {
		t.Fatal(c.Assignments)
	}
	if c.Events[len(c.Events)-1].Assignments[0].ID != "backup" || len(c.Events[1].ParticipantLinks) == 0 {
		t.Fatal("missing audit identity facts")
	}
}

func TestLegacyIntakeFingerprintAndDetachedAuditCompatibility(t *testing.T) {
	req := IntakeRequest{OrganizationID: "o", UnitID: "u", ContactID: "c", Key: "k"}
	data, e := json.Marshal(req)
	if e != nil {
		t.Fatal(e)
	}
	const legacy = `{"OrganizationID":"o","UnitID":"u","ContactID":"c","Key":"k","Participants":null}`
	if string(data) != legacy {
		t.Fatal("legacy zero-value intake fingerprint changed", string(data))
	}
	c := Case{Events: []Event{{ParticipantLinks: []Participant{{ID: "original", Role: Subject}}, Assignments: []Assignment{{ID: "handler", Role: Handler}}}}}
	copy := clone(c)
	copy.Events[0].ParticipantLinks[0].ID = "changed"
	copy.Events[0].Assignments[0].ID = "changed"
	if c.Events[0].ParticipantLinks[0].ID != "original" || c.Events[0].Assignments[0].ID != "handler" {
		t.Fatal("audit alias")
	}
}

func TestUnauthorizedMutationCannotProbeVersion(t *testing.T) {
	r := NewMemoryRepository()
	s := Service{Repository: r}
	result, _ := s.Intake(context.Background(), IntakeRequest{OrganizationID: "o", UnitID: "u", ContactID: "e", Key: "m"})
	for _, v := range []uint64{1, 99} {
		if _, e := s.CorrectCategory(context.Background(), Principal{ID: "outsider"}, result.CaseID, v, "new", "reason", time.Now()); e != ErrDenied {
			t.Fatal(e)
		}
		if _, e := s.SaveDraft(context.Background(), Principal{ID: "outsider"}, result.CaseID, v, "new", time.Now()); e != ErrDenied {
			t.Fatal(e)
		}
	}
}
