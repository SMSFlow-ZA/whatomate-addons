//go:build integration

package cases

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"testing"
	"time"
)

// Local signed SYNTHETIC transport evidence only. This is not a carrier adapter.
func signedCaseReceipt(r api.Receipt) (api.Receipt, api.ReceiptVerifier) {
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
			return ErrDenied
		}
		return nil
	}
}
func TestOutboxVerifiedReceiptReconciliation(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	if e := r.MigrateOutboxDelivery(ctx); e != nil {
		t.Fatal(e)
	}
	tenant := "receipt-" + time.Now().Format("150405.000000000")
	svc := Service{Repository: r}
	if _, e := svc.Intake(ctx, IntakeRequest{OrganizationID: tenant, UnitID: "u", ContactID: "e", Key: "one"}); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	claim, ok, e := r.ClaimOutbox(ctx, tenant, "worker", now, now.Add(time.Minute))
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e = r.FinishOutbox(ctx, claim, "accepted", "provider-1"); e == nil {
		t.Fatal("unbound acceptance")
	}
	if e = r.BindOutboxRoute(ctx, claim, "approved-account"); e != nil {
		t.Fatal(e)
	}
	if e = r.BindOutboxRoute(ctx, claim, "different-account"); e == nil {
		t.Fatal("route changed")
	}
	if e = r.FinishOutbox(ctx, claim, "accepted", "provider-1"); e != nil {
		t.Fatal(e)
	}
	receipt, verify := signedCaseReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: claim.Message.ID, ProviderID: "provider-1", ReceiptID: "receipt-one", ObservedAt: now, Outcome: "delivered"})
	if e = r.ReconcileOutbox(ctx, receipt, nil); e == nil {
		t.Fatal("missing verifier")
	}
	tampered := receipt
	tampered.Outcome = "failed"
	if e = r.ReconcileOutbox(ctx, tampered, verify); e == nil {
		t.Fatal("tamper accepted")
	}
	wrong, wrongVerify := signedCaseReceipt(api.Receipt{Tenant: tenant, Account: "other-account", IntentID: claim.Message.ID, ProviderID: "provider-1", ReceiptID: "wrong", ObservedAt: now, Outcome: "delivered"})
	if e = r.ReconcileOutbox(ctx, wrong, wrongVerify); e == nil {
		t.Fatal("cross-account receipt")
	}
	if e = r.ReconcileOutbox(ctx, receipt, verify); e != nil {
		t.Fatal(e)
	}
	if e = r.ReconcileOutbox(ctx, receipt, verify); e != nil {
		t.Fatal("replay", e)
	}
	conflict, conflictVerify := signedCaseReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: claim.Message.ID, ProviderID: "provider-1", ReceiptID: "receipt-two", ObservedAt: now, Outcome: "failed"})
	if e = r.ReconcileOutbox(ctx, conflict, conflictVerify); e == nil {
		t.Fatal("terminal conflict")
	}
	status, e := r.OutboxDeliveryStatus(ctx, tenant, claim.Message.ID)
	if e != nil || status.State != "delivered" || status.Account != "approved-account" {
		t.Fatal(status, e)
	}
	var count int
	if e = r.DB.QueryRow(`SELECT count(*) FROM addon_cases_delivery_receipt WHERE tenant=$1`, tenant).Scan(&count); e != nil || count != 1 {
		t.Fatal("audit duplicate", count, e)
	}
	if _, ok, e = r.ClaimOutbox(ctx, tenant, "worker", now.Add(time.Hour), now.Add(time.Hour+time.Minute)); e != nil || ok {
		t.Fatal("receipt caused resend", ok, e)
	}
	if _, e = svc.Intake(ctx, IntakeRequest{OrganizationID: tenant, UnitID: "u", ContactID: "e", Key: "two"}); e != nil {
		t.Fatal(e)
	}
	claim, ok, e = r.ClaimOutbox(ctx, tenant, "worker", now, now.Add(time.Minute))
	if e != nil || !ok {
		t.Fatal(e)
	}
	if e = r.BindOutboxRoute(ctx, claim, "approved-account"); e != nil {
		t.Fatal(e)
	}
	if e = r.FinishOutbox(ctx, claim, "held_unknown", ""); e != nil {
		t.Fatal(e)
	}
	receipt, verify = signedCaseReceipt(api.Receipt{Tenant: tenant, Account: "approved-account", IntentID: claim.Message.ID, ProviderID: "recovered-provider", ReceiptID: "receipt-three", ObservedAt: now, Outcome: "failed"})
	if e = r.ReconcileOutbox(ctx, receipt, verify); e != nil {
		t.Fatal(e)
	}
	status, e = r.OutboxDeliveryStatus(ctx, tenant, claim.Message.ID)
	if e != nil || status.State != "failed" || status.ProviderID != "recovered-provider" {
		t.Fatal(status, e)
	}
}
