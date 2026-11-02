package cases

import (
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"testing"
)

func TestPrivacyProtectionPreservesCurrentAssignmentAndAliasConflictPolicy(t *testing.T) {
	p := PrivacyProtection{ID: "case", OrganizationID: "tenant", UnitID: "unit", Participants: []Participant{{ID: "employee", Role: Complainant}}, Assignments: []Assignment{{ID: "handler", Role: Handler}}}
	actor := api.Actor{OrganizationID: "tenant", UserID: "handler", Permissions: []string{string(WorkAssigned)}, Grants: []api.Grant{{Permission: string(WorkAssigned), UnitID: "unit"}}}
	if e := AuthorizePrivacyProtection(actor, p); e != nil {
		t.Fatal(e)
	}
	actor.Aliases = []string{"employee"}
	if AuthorizePrivacyProtection(actor, p) == nil {
		t.Fatal("historical conflict allowed")
	}
	actor.Aliases = nil
	actor.Grants = nil
	if AuthorizePrivacyProtection(actor, p) == nil {
		t.Fatal("revoked grant allowed")
	}
	actor.Grants = []api.Grant{{Permission: string(WorkAssigned), UnitID: "foreign"}}
	if AuthorizePrivacyProtection(actor, p) == nil {
		t.Fatal("foreign unit allowed")
	}
	actor.Grants = []api.Grant{{Permission: string(WorkAssigned), UnitID: "unit"}}
	actor.UserID = "other"
	if AuthorizePrivacyProtection(actor, p) == nil {
		t.Fatal("unassigned reader allowed")
	}
}
