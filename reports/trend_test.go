package reports

import (
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"testing"
	"time"
)

func TestTrendReconcilesHistoricalSnapshotsAndCreationDimensions(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	sep := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	ev := []cases.ReportFact{{OrganizationID: "o", UnitID: "u", CaseID: "old", Version: 1, From: cases.Draft, To: cases.Submitted, OccurredAt: sep, RecordedAt: sep, Snapshot: cases.Classification{Zone: "original", Department: "d", CaseType: "t"}}, {OrganizationID: "o", UnitID: "u", CaseID: "old", Version: 2, From: cases.Submitted, To: cases.Resolved, OccurredAt: oct, RecordedAt: oct, Snapshot: cases.Classification{Zone: "original", Department: "d", CaseType: "t"}}, {OrganizationID: "o", UnitID: "u", CaseID: "new", Version: 1, From: cases.Draft, To: cases.Submitted, OccurredAt: oct, RecordedAt: oct, Snapshot: cases.Classification{Zone: "other", Department: "d", CaseType: "t"}}}
	q := TrendQuery{EndMonth: "2026-10", Months: 3, Dimension: "zone"}
	r, e := BuildTrend("o", ev, q, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Periods) != 3 || r.Periods[0].NewCases != 0 || r.Periods[1].NewCases != 1 || r.Periods[1].Backlog != 1 || r.Periods[2].NewCases != 1 || r.Periods[2].Backlog != 1 || !r.Periods[2].Partial {
		t.Fatal("wrong historical trend", r)
	}
	if r.Periods[1].Groups[0].Label != "original" || *r.Periods[1].Groups[0].Value != 1 {
		t.Fatal("creation dimensions lost", r)
	}
	ev[1].Snapshot.Zone = "roster changed"
	if _, e = BuildTrend("o", ev, q, now); e == nil {
		t.Fatal("rewritten historical dimensions accepted")
	}
}
func TestTrendBoundsAndFilteredCohorts(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, q := range []TrendQuery{{EndMonth: "2026-11", Months: 6, Dimension: "zone"}, {EndMonth: "2026-10", Months: 13, Dimension: "zone"}, {EndMonth: "2026-10", Months: 0, Dimension: "zone"}, {EndMonth: "2026-10", Months: 6, Dimension: "person"}} {
		if _, e := BuildTrend("o", nil, q, now); e == nil {
			t.Fatal("invalid trend accepted", q)
		}
	}
}
