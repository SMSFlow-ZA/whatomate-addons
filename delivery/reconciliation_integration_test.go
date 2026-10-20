//go:build integration

package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"os"
	"testing"
	"time"
)

// Signed local synthetic adapter evidence only; no carrier is configured here.
func signedReplyReceipt(r api.Receipt) (api.Receipt, api.ReceiptVerifier) {
	key := []byte("local-synthetic-receipt-only")
	sign := func(v api.Receipt) string {
		v.Evidence = ""
		b, _ := json.Marshal(v)
		h := hmac.New(sha256.New, key)
		h.Write(b)
		return hex.EncodeToString(h.Sum(nil))
	}
	r.Evidence = sign(r)
	return r, func(ctx context.Context, v api.Receipt) error {
		if !hmac.Equal([]byte(sign(v)), []byte(v.Evidence)) {
			return ErrInvalid
		}
		return nil
	}
}
func TestVerifiedUnknownReceiptSettlesAndQueuesOnlyKnownFailureFallback(t *testing.T) {
	dsn := os.Getenv("CONVERSATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONVERSATION_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	tenant := fmt.Sprintf("reconcile-%d", time.Now().UnixNano())
	enqueue := func(suffix string) {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = Enqueue(ctx, tx, Message{ID: tenant + suffix, Tenant: tenant, Account: "approved-account", Sender: "+12025550101", EmployeeID: "e", Text: "PRIVATE ORIGINAL CONTENT"}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	eligible := true
	calls := 0
	authorize := func(context.Context, Message) error {
		if !eligible {
			return ErrInvalid
		}
		return nil
	}
	w := Worker{DB: db, Tenant: tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: authorize, SendObserved: func(ctx context.Context, m Message) (Result, string, error) {
		calls++
		return Unknown, "provider-" + m.ID, nil
	}}
	enqueue("one")
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, verify := signedReplyReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: tenant + "one", ProviderID: "provider-" + tenant + "one", ReceiptID: "receipt-one", ObservedAt: time.Now().UTC(), Outcome: "delivered"})
	reconciler := Reconciler{DB: db, Verify: verify, SMSFallback: &SMSFallbackConfig{Account: "approved-sms"}, Authorize: authorize}
	if e = (Reconciler{DB: db}).Reconcile(ctx, receipt); e == nil {
		t.Fatal("no verifier")
	}
	tampered := receipt
	tampered.Account = "other"
	if e = reconciler.Reconcile(ctx, tampered); e == nil {
		t.Fatal("tamper")
	}
	if e = reconciler.Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	changed := receipt
	changed.ObservedAt = changed.ObservedAt.Add(time.Second)
	changed, changedVerify := signedReplyReceipt(changed)
	if e = (Reconciler{DB: db, Verify: changedVerify}).Reconcile(ctx, changed); e == nil {
		t.Fatal("changed signed replay accepted")
	}
	if e = reconciler.Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	status, e := ReadDeliveryStatus(ctx, db, tenant, tenant+"one")
	if e != nil || status.State != "delivered" {
		t.Fatal(status, e)
	}
	conflict, conflictVerify := signedReplyReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: tenant + "one", ProviderID: receipt.ProviderID, ReceiptID: "receipt-conflict", ObservedAt: time.Now().UTC(), Outcome: "failed"})
	reconciler.Verify = conflictVerify
	if e = reconciler.Reconcile(ctx, conflict); e == nil {
		t.Fatal("terminal conflict")
	}
	enqueue("two")
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, verify = signedReplyReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: tenant + "two", ProviderID: "provider-" + tenant + "two", ReceiptID: "receipt-two", ObservedAt: time.Now().UTC(), Outcome: "failed"})
	reconciler.Verify = verify
	if e = reconciler.Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	if e = reconciler.Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	messages, e := List(ctx, db, tenant, "+12025550101")
	if e != nil || len(messages) != 3 {
		t.Fatal(len(messages), e)
	}
	for _, m := range messages {
		if m.Channel == "sms" && (m.Text != MinimalSMSNotice || m.Account != "approved-sms" || m.ID != tenant+"two:sms-fallback") {
			t.Fatal("nonminimal fallback", m)
		}
	}
	eligible = false
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("revoked fallback dispatched", calls)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM addon_conversation_delivery_receipt WHERE tenant=$1`, tenant).Scan(&count); e != nil || count != 2 {
		t.Fatal("durable audit", count, e)
	}
}

func TestAcceptedAndExpiredAttemptReconciliationNeverResends(t *testing.T) {
	dsn := os.Getenv("CONVERSATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONVERSATION_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	tenant := fmt.Sprintf("accepted-%d", time.Now().UnixNano())
	enqueue := func(id string) {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = Enqueue(ctx, tx, Message{ID: id, Tenant: tenant, Account: "approved-account", Sender: "+12025550101", EmployeeID: "e"}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	calls := 0
	w := Worker{DB: db, Tenant: tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: func(context.Context, Message) error { return nil }, SendObserved: func(ctx context.Context, m Message) (Result, string, error) {
		calls++
		return Accepted, "provider-" + m.ID, nil
	}}
	enqueue(tenant + "accepted")
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	status, e := ReadDeliveryStatus(ctx, db, tenant, tenant+"accepted")
	if e != nil || status.State != "accepted" {
		t.Fatal(status, e)
	}
	receipt, verify := signedReplyReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: tenant + "accepted", ProviderID: status.ProviderID, ReceiptID: "accepted-receipt", ObservedAt: time.Now().UTC(), Outcome: "delivered"})
	if e = (Reconciler{DB: db, Verify: verify}).Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	enqueue(tenant + "expired")
	w.SendObserved = func(ctx context.Context, m Message) (Result, string, error) {
		calls++
		_, e := db.ExecContext(ctx, `UPDATE addon_conversation_delivery SET lease_until=now()-interval '1 second' WHERE id=$1`, m.ID)
		return Accepted, "provider-expired", e
	}
	if _, e = w.Dispatch(ctx); e == nil {
		t.Fatal("stale claim finished")
	}
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("expired send repeated", calls)
	}
	status, e = ReadDeliveryStatus(ctx, db, tenant, tenant+"expired")
	if e != nil || status.State != "held_unknown" {
		t.Fatal(status, e)
	}
	receipt, verify = signedReplyReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: tenant + "expired", ProviderID: "provider-expired", ReceiptID: "expired-receipt", ObservedAt: time.Now().UTC(), Outcome: "delivered"})
	if e = (Reconciler{DB: db, Verify: verify}).Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	status, e = ReadDeliveryStatus(ctx, db, tenant, tenant+"expired")
	if e != nil || status.State != "delivered" {
		t.Fatal(status, e)
	}
}
