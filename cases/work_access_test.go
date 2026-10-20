package cases

import "testing"

func TestCanWorkRequiresAssignmentAndRejectsSubjectAlias(t *testing.T) {
	c := Case{OrganizationID: "o", UnitID: "u", Assignments: []Assignment{{ID: "worker", Role: Handler}}}
	p := Principal{ID: "worker", Grants: []Grant{{OrganizationID: "o", UnitID: "u", Permission: WorkAssigned}}}
	if !CanWork(p, c) {
		t.Fatal("assigned worker denied")
	}
	c.Participants = []Participant{{ID: "employee", Role: Subject}}
	p.Aliases = []string{"employee"}
	if CanWork(p, c) {
		t.Fatal("subject alias gained work access")
	}
	p.Aliases = nil
	p.ID = "triage"
	p.Grants = append(p.Grants, Grant{OrganizationID: "o", UnitID: "u", Permission: ReadTriage})
	if CanWork(p, c) {
		t.Fatal("unassigned triage gained work access")
	}
}
