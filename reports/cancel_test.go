package reports

import (
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"testing"
	"time"
)

func TestCancelledDraftNeverBecomesSubmittedReportFact(t *testing.T) {
	now := time.Now().UTC()
	events := []cases.ReportFact{
		{OrganizationID: "org", UnitID: "unit", CaseID: "draft", Version: 1, From: cases.Draft, To: cases.Draft, OccurredAt: now, RecordedAt: now},
		{OrganizationID: "org", UnitID: "unit", CaseID: "draft", Version: 2, From: cases.Draft, To: cases.Cancelled, OccurredAt: now, RecordedAt: now},
	}
	got, e := Reconstruct(events, now)
	if e != nil || len(got.Cases) != 0 {
		t.Fatal("cancelled draft counted as submitted", got, e)
	}
}
