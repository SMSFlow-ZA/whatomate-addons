package reports

import (
	"testing"
	"time"
)

func fixture() (Facts, Window) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	w := Window{Start: start, End: start.AddDate(0, 1, 0), AsOf: start.AddDate(0, 0, 17), MinGroup: 5}
	f := Facts{}
	for i := 0; i < 16; i++ {
		created := start.Add(time.Duration(i) * time.Hour)
		resolved := time.Time{}
		if i >= 12 {
			created = start.AddDate(0, -1, 0)
			resolved = start.Add(-time.Hour)
		} else if i < 3 {
			resolved = start.Add(24 * time.Hour)
		}
		f.Cases = append(f.Cases, CaseFact{OrganizationID: "org", ID: string(rune('a' + i)), CreatedAt: created, ResolvedAt: resolved, Zone: []string{"Alpha", "Beta", "Gamma"}[i%3], Department: []string{"Operations", "Administration"}[i%2], CaseType: []string{"Disciplinary", "Disciplinary", "Grievance", "Labour"}[i%4]})
	}
	return f, w
}

func TestSixteenCaseFixtureSeparatesPeriodAndBacklog(t *testing.T) {
	f, w := fixture()
	r, err := Operational(Actor{OrganizationID: "org", Permissions: []string{"reports:operational"}}, f, w, func(CaseFact) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if r.NewCases != 12 || r.ResolvedCreatedInPeriod != 3 || r.Backlog != 9 {
		t.Fatalf("wrong counts %+v", r)
	}
	if r.ByZone["Alpha"] != 4 || r.ByDepartment["Operations"] != 6 {
		t.Fatal("wrong dimensions")
	}
	if !r.Partial {
		t.Fatal("partial month hidden")
	}
}
func TestOperationalCannotUsePermissionToReadUnassignedCase(t *testing.T) {
	f, w := fixture()
	r, err := Operational(Actor{OrganizationID: "org", Permissions: []string{"reports:operational"}}, f, w, func(c CaseFact) bool { return c.ID == "a" })
	if err != nil {
		t.Fatal(err)
	}
	if r.NewCases != 1 {
		t.Fatal("case scope bypass")
	}
	if _, err = Operational(Actor{OrganizationID: "org", Permissions: []string{"reports:operational"}}, f, w, nil); err == nil {
		t.Fatal("nil case policy default allowed")
	}
}
func TestBoardSuppressionHidesComplementaryTotalsAndRawFacts(t *testing.T) {
	f, w := fixture()
	r, err := Board(Actor{OrganizationID: "org", Permissions: []string{"reports:aggregate"}}, f, w)
	if err != nil {
		t.Fatal(err)
	}
	if r.NewCases != nil || r.Backlog != nil {
		t.Fatal("suppressed groups recoverable from totals")
	}
	for _, c := range r.ByZone {
		if c.Value != nil {
			t.Fatal("tiny cell leaked")
		}
	}
	if _, err = Board(Actor{OrganizationID: "org", Permissions: []string{"admin"}}, f, w); err == nil {
		t.Fatal("admin implicitly granted board access")
	}
}
func TestDuplicateCaseAndUnconfiguredThresholdRejected(t *testing.T) {
	f, w := fixture()
	f.Cases = append(f.Cases, f.Cases[0])
	if _, err := Board(Actor{OrganizationID: "org", Permissions: []string{"reports:aggregate"}}, f, w); err == nil {
		t.Fatal("duplicated facts inflate metrics")
	}
	f, w = fixture()
	w.MinGroup = 0
	if _, err := Board(Actor{OrganizationID: "org", Permissions: []string{"reports:aggregate"}}, f, w); err == nil {
		t.Fatal("unapproved disclosure threshold")
	}
}
func TestMessagesAndEnquiriesAreDistinctAndSnapshotBound(t *testing.T) {
	f, w := fixture()
	f.Messages = []Activity{{OrganizationID: "org", ID: "m1", CaseID: "a", At: w.Start}, {OrganizationID: "org", ID: "m1", CaseID: "a", At: w.Start}, {OrganizationID: "org", ID: "unassigned", CaseID: "", At: w.Start}, {OrganizationID: "org", ID: "future", CaseID: "a", At: w.AsOf.Add(time.Hour)}}
	f.Enquiries = []Activity{{OrganizationID: "org", ID: "q1", CaseID: "a", At: w.Start}}
	r, err := Operational(Actor{OrganizationID: "org", Permissions: []string{"reports:operational"}}, f, w, func(CaseFact) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if r.Messages != 1 || r.Enquiries != 1 || r.NewCases != 12 {
		t.Fatalf("wrong activity %+v", r)
	}
}

func TestBoardSuppressesSmallHeadlineMetricsEvenWithoutNewCases(t *testing.T) {
	f, w := fixture()
	f.Cases = []CaseFact{{OrganizationID: "org", ID: "historic", CreatedAt: w.Start.Add(-time.Hour), Zone: "Alpha", Department: "Operations", CaseType: "Grievance"}}
	r, err := Board(Actor{OrganizationID: "org", Permissions: []string{"reports:aggregate"}}, f, w)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Suppressed || r.Backlog != nil {
		t.Fatal("singleton historic backlog disclosed")
	}
	f.Cases = nil
	for i := 0; i < 6; i++ {
		c := CaseFact{OrganizationID: "org", ID: string(rune('a' + i)), CreatedAt: w.Start, Zone: "Alpha", Department: "Operations", CaseType: "Grievance"}
		if i == 0 {
			c.ResolvedAt = w.Start.Add(time.Hour)
		}
		f.Cases = append(f.Cases, c)
	}
	r, err = Board(Actor{OrganizationID: "org", Permissions: []string{"reports:aggregate"}}, f, w)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Suppressed || r.Resolved != nil || r.Backlog != nil {
		t.Fatal("small resolution metric disclosed despite large dimensional groups")
	}
	for _, cell := range r.ByZone {
		if cell.Value != nil {
			t.Fatal("complementary table left visible")
		}
	}
}
