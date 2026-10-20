package reports

import (
	"testing"
	"time"
)

func TestServiceSmallCellSuppressesAllRelatedMarginsAndTotals(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	w := Window{Start: at, End: at.AddDate(0, 1, 0), AsOf: at.AddDate(0, 1, 0), MinGroup: 5}
	events := []ActivityFact{}
	for i := 0; i < 8; i++ {
		events = append(events, ActivityFact{ID: string(rune('a' + i)), Kind: "inbound", Outcome: "received", Zone: "z", Department: "d", CaseType: "t", OccurredAt: at})
	}
	events[0].Zone = "small"
	r := serviceReport(events, w, true, "", "", "")
	if !r.Suppressed || r.Inbound != nil || r.OutboundQueued != nil {
		t.Fatal("tiny group disclosed", r)
	}
	for _, c := range r.ByDepartment {
		if c.Value != nil {
			t.Fatal("complement disclosed")
		}
	}
	r = serviceReport(events, w, false, "z", "", "")
	if r.Inbound == nil || *r.Inbound != 7 || r.Suppressed {
		t.Fatal("operational activity filter", r)
	}
}
func TestQueuedMessagesNeverBecomeDeliveredMetrics(t *testing.T) {
	f := ActivityFact{Kind: "outbound", Outcome: "delivered"}
	if validActivity(f) {
		t.Fatal("unsupported receipt claim accepted")
	}
	f.Outcome = "queued"
	if !validActivity(f) {
		t.Fatal("queued event rejected")
	}
}
