package governance

import "testing"

func TestAppliedInventoryReceiptRequiresExactRequestScopeGeneration(t *testing.T) {
	scope := Scope{OrganizationID: "tenant", UnitID: "unit"}
	req := SubjectRequest{ID: "request", Scope: scope, SubjectID: "subject", Kind: Delete, Approved: true}
	r := Record{ID: "record", Scope: scope, SubjectID: "subject", Generation: 2}
	j := JournalEntry{Scope: scope, RequestID: req.ID, RecordID: r.ID, Generation: 2, State: RecordedApplied, ConfirmationReference: "sql-transaction:receipt"}
	if !appliedInventoryReceipt(req, r, j) {
		t.Fatal("exact applied receipt rejected")
	}
	for _, change := range []func(*JournalEntry){func(v *JournalEntry) { v.State = AwaitingExpiry }, func(v *JournalEntry) { v.RequestID = "other" }, func(v *JournalEntry) { v.RecordID = "other" }, func(v *JournalEntry) { v.Generation = 1 }, func(v *JournalEntry) { v.Scope.UnitID = "other" }, func(v *JournalEntry) { v.ConfirmationReference = "" }} {
		bad := j
		change(&bad)
		if appliedInventoryReceipt(req, r, bad) {
			t.Fatal("nonmatching receipt allowed", bad)
		}
	}
	req.Approved = false
	if appliedInventoryReceipt(req, r, j) {
		t.Fatal("unapproved request allowed")
	}
}
