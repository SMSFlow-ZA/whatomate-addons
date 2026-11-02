package cases

import (
	"context"
	"testing"
	"time"
)

func TestCategoryRegistryBindingPersistsOnlyHumanConfirmedVersion(t *testing.T) {
	s := Service{Repository: NewMemoryRepository()}
	ctx := context.Background()
	created, e := s.Intake(ctx, IntakeRequest{OrganizationID: "tenant", UnitID: "unit", ContactID: "employee", Key: "intake", Narrative: "Concern", Snapshot: Classification{CaseType: "Original intake"}})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "handler", Grants: []Grant{{OrganizationID: "tenant", UnitID: "unit", Permission: AssignTriage}}}
	binding := CategoryBinding{Audience: "explicit-registry", Version: 7}
	c, e := s.correctCategory(ctx, p, created.CaseID, 1, "Reviewed concern", "Human reviewed", time.Now(), binding)
	if e != nil {
		t.Fatal(e)
	}
	if c.CategoryBinding != binding || c.Events[1].CategoryBinding != binding || c.Snapshot.CaseType != "Original intake" {
		t.Fatal(c)
	}
	if EmployeeView(c).Version != 2 {
		t.Fatal("own view changed shape")
	}
	// Legacy trusted integrations remain compatible and never falsely claim an
	// earlier registry authorized their subsequent free-text correction.
	c, e = s.CorrectCategory(ctx, p, c.ID, 2, "Legacy category", "Legacy review", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if c.CategoryBinding != (CategoryBinding{}) || c.Events[1].CategoryBinding != binding || c.Events[2].CategoryBinding != (CategoryBinding{}) {
		t.Fatal(c.Events)
	}
}
