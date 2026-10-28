package notifications

import (
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"testing"
	"time"
)

func fixture() (cases.Case, Identity, directory.Snapshot, time.Time) {
	now := time.Now().UTC()
	c := cases.Case{ID: "case", OrganizationID: "org", UnitID: "unit", State: cases.Escalated, Version: 3, Assignments: []cases.Assignment{{ID: "handler", Role: cases.Handler}}}
	i := Identity{OrganizationID: "org", UserID: "handler", Active: true, EmployeeIDs: []string{"employee"}, Principal: cases.Principal{ID: "handler", Aliases: []string{"employee"}, Grants: []cases.Grant{{OrganizationID: "org", UnitID: "unit", Permission: cases.WorkAssigned}}}}
	d := directory.Snapshot{Version: 1, ImportedAt: now.Add(-time.Minute), Employees: map[string]directory.Employee{"employee": {ID: "employee", Phone: "+12025550101", Zone: "unit", Active: true}}}
	return c, i, d, now
}

func TestRecipientRequiresCurrentExactAuthorityAndUniqueApprovedIdentity(t *testing.T) {
	tests := []struct {
		name   string
		change func(*cases.Case, *Identity, *directory.Snapshot)
	}{
		{"inactive", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.Active = false }},
		{"wrong organization", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.OrganizationID = "other" }},
		{"wrong user", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.UserID = "other" }},
		{"revoked grant", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.Principal.Grants = nil }},
		{"other unit", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.Principal.Grants[0].UnitID = "other" }},
		{"reassigned", func(c *cases.Case, i *Identity, d *directory.Snapshot) { c.Assignments[0].ID = "other" }},
		{"subject alias", func(c *cases.Case, i *Identity, d *directory.Snapshot) {
			i.Principal.Aliases = append(i.Principal.Aliases, "old")
			c.Participants = []cases.Participant{{ID: "old", Role: cases.Subject}}
		}},
		{"missing alias", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.Principal.Aliases = nil }},
		{"missing link", func(c *cases.Case, i *Identity, d *directory.Snapshot) { i.EmployeeIDs = nil }},
		{"ambiguous links", func(c *cases.Case, i *Identity, d *directory.Snapshot) {
			i.EmployeeIDs = append(i.EmployeeIDs, "second")
			i.Principal.Aliases = append(i.Principal.Aliases, "second")
			d.Employees["second"] = directory.Employee{ID: "second", Phone: "+12025550102", Zone: "unit", Active: true}
		}},
		{"stale roster", func(c *cases.Case, i *Identity, d *directory.Snapshot) {
			d.ImportedAt = d.ImportedAt.Add(-48 * time.Hour)
		}},
		{"revoked employee", func(c *cases.Case, i *Identity, d *directory.Snapshot) { d.Revoked = map[string]bool{"employee": true} }},
		{"resolved", func(c *cases.Case, i *Identity, d *directory.Snapshot) { c.State = cases.Resolved }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, i, d, now := fixture()
			tt.change(&c, &i, &d)
			if _, err := recipient(c, "handler", i, d, now, time.Hour); err == nil {
				t.Fatal("unauthorized handler recipient accepted")
			}
		})
	}
	c, i, d, now := fixture()
	emp := d.Employees["employee"]
	emp.Zone = "home-unit"
	d.Employees["employee"] = emp // exact case grant authorizes cross-unit work
	got, err := recipient(c, "handler", i, d, now, time.Hour)
	if err != nil || got.ID != "employee" {
		t.Fatalf("valid recipient: %v %v", got, err)
	}
}
