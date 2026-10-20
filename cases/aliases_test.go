package cases

import "testing"

func TestVerifiedEmployeeAliasCannotEvadeSubjectConflict(t *testing.T) {
	c := Case{ID: "case", OrganizationID: "org", UnitID: "alpha", Participants: []Participant{{ID: "employee-7", Role: Subject}}, Assignments: []Assignment{{ID: "operator-7", Role: Handler}}}
	p := Principal{ID: "operator-7", Aliases: []string{"employee-7"}, Grants: []Grant{{OrganizationID: "org", UnitID: "alpha", Permission: WorkAssigned}}}
	if CanRead(p, c) {
		t.Fatal("operator account bypassed subject link through employee identity")
	}
}

func TestVerifiedAliasCanReadOnlyOwnPermittedCase(t *testing.T) {
	c := Case{ID: "case", OrganizationID: "org", UnitID: "alpha", Participants: []Participant{{ID: "employee-7", Role: Complainant}}}
	p := Principal{ID: "operator-7", Aliases: []string{"employee-7"}, Grants: []Grant{{OrganizationID: "org", UnitID: "alpha", Permission: ReadOwn}}}
	if !CanRead(p, c) {
		t.Fatal("verified complainant alias not recognized")
	}
	c.UnitID = "beta"
	if CanRead(p, c) {
		t.Fatal("alias granted another unit")
	}
}
