//go:build integration

package cases

import (
	"context"
	"testing"
	"time"
)

func TestOutboxLeaseNeverBlindRetriesExpiredOrAccepted(t *testing.T) {
	r := integrationRepository(t)
	ctx := context.Background()
	if e := r.MigrateOutboxDelivery(ctx); e != nil {
		t.Fatal(e)
	}
	org := "outbox-" + time.Now().Format("150405.000000000")
	for _, key := range []string{"a", "b"} {
		if _, e := (Service{Repository: r}).Intake(ctx, IntakeRequest{OrganizationID: org, UnitID: "u", ContactID: "e", Key: key}); e != nil {
			t.Fatal(e)
		}
	}
	now := time.Now().UTC()
	claim, ok, e := r.ClaimOutbox(ctx, org, "worker", now, now.Add(time.Second))
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	if _, ok, e := r.ClaimOutbox(ctx, "other-org", "worker", now, now.Add(time.Second)); e != nil || ok {
		t.Fatal("cross org", ok, e)
	}
	next, ok, e := r.ClaimOutbox(ctx, org, "worker", now.Add(2*time.Second), now.Add(3*time.Second))
	if e != nil || !ok || next.Message.ID == claim.Message.ID {
		t.Fatal("expired reclaimed", next, e)
	}
	if e = r.FinishOutbox(ctx, claim, "delivered", "receipt"); e == nil {
		t.Fatal("expired lease completed")
	}
	forged := next
	forged.Token = "forged"
	if e = r.FinishOutbox(ctx, forged, "accepted", "provider"); e == nil {
		t.Fatal("forged lease completed")
	}
	if e = r.BindOutboxRoute(ctx, next, "synthetic-test-account"); e != nil {
		t.Fatal(e)
	}
	if e = r.FinishOutbox(ctx, next, "accepted", "provider"); e != nil {
		t.Fatal(e)
	}
	if _, ok, e = r.ClaimOutbox(ctx, org, "worker", now.Add(4*time.Second), now.Add(5*time.Second)); e != nil || ok {
		t.Fatal("accepted or unknown retried", ok, e)
	}
}
