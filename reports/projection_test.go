package reports

import (
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"testing"
	"time"
)

func TestReconstructUsesSubmissionAndEventTimeNotCurrentState(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fact := func(v uint64, from, to cases.State, at time.Time) cases.ReportFact {
		return cases.ReportFact{OrganizationID: "o", UnitID: "u", CaseID: "c", Version: v, From: from, To: to, OccurredAt: at, RecordedAt: at, Snapshot: cases.Classification{Zone: "original", Department: "d", CaseType: "t"}}
	}
	events := []cases.ReportFact{fact(1, cases.Draft, cases.Draft, base), fact(2, cases.Draft, cases.Submitted, base.Add(time.Hour)), fact(3, cases.Submitted, cases.Resolved, base.Add(2*time.Hour)), fact(4, cases.Resolved, cases.InProgress, base.Add(4*time.Hour))}
	events[3].Snapshot.Zone = "mutated"
	if _, err := Reconstruct(events, base.Add(3*time.Hour)); err == nil {
		t.Fatal("changed creation facts accepted")
	}
	events[3].Snapshot.Zone = "original"
	f, err := Reconstruct(events, base.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) != 1 || f.Cases[0].ResolvedAt != base.Add(2*time.Hour) || f.Cases[0].CreatedAt != base.Add(time.Hour) {
		t.Fatalf("wrong historical state %+v", f)
	}
	f, err = Reconstruct(events, base.Add(5*time.Hour))
	if err != nil || !f.Cases[0].ResolvedAt.IsZero() {
		t.Fatalf("reopening lost %+v %v", f, err)
	}
	f, err = Reconstruct(events, base)
	if err != nil || len(f.Cases) != 0 {
		t.Fatal("draft counted as case")
	}
}
func TestHistoricalWindowRejectsFutureAndBoardDifferencing(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if _, err := ParseWindow("2026-10-01", "2026-11-01", "", 5, true, now); err == nil {
		t.Fatal("partial board month allowed")
	}
	if _, err := ParseWindow("2026-09-02", "2026-10-01", "", 5, true, now); err == nil {
		t.Fatal("overlapping board cohort allowed")
	}
	w, err := ParseWindow("2026-09-01", "2026-10-01", "", 5, true, now)
	if err != nil || w.AsOf != w.End {
		t.Fatal(w, err)
	}
	if _, err := ParseWindow("2026-09-01", "2026-10-01", "2026-11-01T00:00:00Z", 5, false, now); err == nil {
		t.Fatal("future snapshot accepted")
	}
}

func TestPastMonthDefaultsToMonthEnd(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	w, e := ParseWindow("2026-09-01", "2026-10-01", "", 5, false, now)
	if e != nil || !w.AsOf.Equal(w.End) {
		t.Fatal("past backlog silently used current state", w, e)
	}
}
