package cases

import "testing"

func TestCanReadInternalExcludesOwnComplainantAndHistoricalAliases(t *testing.T) {
	c := Case{OrganizationID: "tenant", UnitID: "unit", Participants: []Participant{{ID: "employee", Role: Complainant}}, Assignments: []Assignment{{ID: "handler", Role: Handler}}}
	p := Principal{ID: "handler", Grants: []Grant{{OrganizationID: "tenant", UnitID: "unit", Permission: WorkAssigned}, {OrganizationID: "tenant", UnitID: "unit", Permission: ReadOwn}}}
	if !CanReadInternal(p, c) {
		t.Fatal("assigned handler rejected")
	}
	for _, identity := range []Principal{{ID: "employee", Grants: p.Grants}, {ID: "handler", Aliases: []string{"employee"}, Grants: p.Grants}} {
		if !CanRead(identity, c) {
			t.Fatal("fixture must retain redacted own-case access")
		}
		if CanReadInternal(identity, c) {
			t.Fatal("redacted own status widened to internal read")
		}
	}
	subject := p
	subject.Aliases = []string{"subject-old"}
	c.Participants = append(c.Participants, Participant{ID: "subject-old", Role: Subject})
	if CanReadInternal(subject, c) {
		t.Fatal("historical subject gained internal read")
	}
	triage := Principal{ID: "triage", Grants: []Grant{{OrganizationID: "tenant", UnitID: "unit", Permission: ReadTriage}}}
	if CanReadInternal(triage, c) {
		t.Fatal("triage read assigned case")
	}
	c.Assignments = nil
	if !CanReadInternal(triage, c) {
		t.Fatal("unassigned triage rejected")
	}
}
