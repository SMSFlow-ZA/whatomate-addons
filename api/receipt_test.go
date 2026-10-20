package api

import (
	"context"
	"testing"
	"time"
)

func TestReceiptRequiresCompleteVerifiedBindingBeforePersistence(t *testing.T) {
	r := Receipt{Tenant: "t", Account: "a", IntentID: "i", ProviderID: "p", ReceiptID: "r", Evidence: "server-evidence", ObservedAt: time.Now(), Outcome: "delivered"}
	if _, e := VerifyReceipt(context.Background(), r, nil); e == nil {
		t.Fatal("default verifier allowed")
	}
	calls := 0
	verify := func(context.Context, Receipt) error { calls++; return nil }
	for _, change := range []func(*Receipt){func(r *Receipt) { r.Tenant = "" }, func(r *Receipt) { r.Account = "" }, func(r *Receipt) { r.IntentID = "" }, func(r *Receipt) { r.ProviderID = "" }, func(r *Receipt) { r.ReceiptID = "" }, func(r *Receipt) { r.Evidence = "" }, func(r *Receipt) { r.Outcome = "accepted" }, func(r *Receipt) { r.ObservedAt = time.Time{} }} {
		bad := r
		change(&bad)
		if _, e := VerifyReceipt(context.Background(), bad, verify); e == nil {
			t.Fatal("invalid receipt")
		}
	}
	if calls != 0 {
		t.Fatal("invalid evidence verifier invoked")
	}
	fingerprint, e := VerifyReceipt(context.Background(), r, verify)
	if e != nil || len(fingerprint) != 64 || calls != 1 {
		t.Fatal(fingerprint, e, calls)
	}
}
