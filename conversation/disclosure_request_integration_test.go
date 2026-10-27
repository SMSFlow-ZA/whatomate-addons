//go:build integration

package conversation

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
)

func TestProcessingDisclosureExplicitRequestAfterUnknownRequiresNewDeliveredReceipt(t *testing.T) {
	ctx, s, tenant, calls := disclosureService(t)
	original, err := s.Receive(ctx, disclosureInbound(tenant, "original", "Synthetic SOP question"))
	if err != nil {
		t.Fatal(err)
	}
	observeNotice(t, ctx, s, tenant, original.ProcessingDisclosureIntentID, delivery.Unknown, "")
	before, err := delivery.ReadDeliveryStatus(ctx, s.DB, tenant, original.ProcessingDisclosureIntentID)
	if err != nil || before.State != "held_unknown" {
		t.Fatal("original not held unknown", before.State, err)
	}
	in := disclosureInbound(tenant, "explicit-request", "SEND PROCESSING NOTICE")
	fresh, err := s.Receive(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	wantID := digest(in.Tenant + "\x00" + in.Account + "\x00" + in.ProviderMessageID)
	if fresh.ProcessingDisclosureIntentID != wantID || fresh.ProcessingDisclosureIntentID == original.ProcessingDisclosureIntentID || fresh.ProcessingDisclosureKey != original.ProcessingDisclosureKey || fresh.Reply != original.Reply || calls.Load() != 0 {
		t.Fatal("explicit request must queue the same canonical notice as a distinct intent without advice")
	}
	replayed, err := s.Receive(ctx, in)
	if err != nil || !reflect.DeepEqual(replayed, fresh) {
		t.Fatal("duplicate request changed its result", err)
	}
	var queued int
	if err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_conversation_delivery WHERE tenant=$1 AND id=$2`, tenant, wantID).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("duplicate request created more than one intent", queued, err)
	}
	observeNotice(t, ctx, s, tenant, fresh.ProcessingDisclosureIntentID, delivery.Accepted, "")
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "before-delivery", "Synthetic question again")); err != nil || calls.Load() != 0 {
		t.Fatal("accepted notice unlocked advice", err)
	}
	status, err := delivery.ReadDeliveryStatus(ctx, s.DB, tenant, wantID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, verify := disclosureReceipt(api.Receipt{Tenant: tenant, Account: in.Account, IntentID: wantID, ProviderID: status.ProviderID, ReceiptID: "new-notice-delivered", ObservedAt: time.Now().UTC(), Outcome: "delivered"})
	forged := receipt
	forged.Evidence = "invalid"
	reconciler := delivery.Reconciler{DB: s.DB, Verify: verify, Authorize: s.AuthorizeDelivery}
	if err = reconciler.Reconcile(ctx, forged); err == nil {
		t.Fatal("unsigned notice receipt accepted")
	}
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "after-forged", "Synthetic question again")); err != nil || calls.Load() != 0 {
		t.Fatal("forged receipt unlocked advice", err)
	}
	if err = reconciler.Reconcile(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "after-delivery", "Synthetic question again")); err != nil || calls.Load() != 1 {
		t.Fatal("exact delivered new notice did not unlock advice", err)
	}
	after, err := delivery.ReadDeliveryStatus(ctx, s.DB, tenant, original.ProcessingDisclosureIntentID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("explicit request changed original unknown delivery", err)
	}
}

func TestProcessingDisclosureExplicitRequestPreservesCaseWorkflow(t *testing.T) {
	ctx, s, tenant, calls := disclosureService(t)
	receive := func(id, text string) Turn {
		t.Helper()
		turn, err := s.Receive(ctx, disclosureInbound(tenant, id, text))
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
	check := func(before, after Turn) {
		t.Helper()
		before.ProcessingDisclosureKey = after.ProcessingDisclosureKey
		before.ProcessingDisclosureIntentID = after.ProcessingDisclosureIntentID
		before.Reply = after.Reply
		before.Citations = after.Citations
		if !reflect.DeepEqual(before, after) {
			t.Fatal("notice request changed draft/case workflow state")
		}
	}
	narrative := receive("start", "LOG CASE")
	check(narrative, receive("notice-during-narrative", "send processing notice"))
	confirm := receive("narrative", "Synthetic employee concern for review")
	check(confirm, receive("notice-during-confirm", " SEND PROCESSING NOTICE "))
	submitted := receive("confirmed", "CONFIRM")
	check(submitted, receive("notice-after-case", "SEND PROCESSING NOTICE"))
	if submitted.Selected == "" || len(submitted.Cases) != 1 || calls.Load() != 0 {
		t.Fatal("case workflow or advice boundary changed")
	}
}

func TestProcessingDisclosureExplicitRequestRejectsMissingOrMalformedNotice(t *testing.T) {
	for _, notice := range []*ProcessingDisclosure{nil, {Text: "Missing revision"}, {Revision: "v", Text: ""}, {Revision: "\xff", Text: "Synthetic notice"}} {
		ctx, s, tenant, calls := disclosureService(t)
		s.Config.ProcessingDisclosure = notice
		before, err := s.Receive(ctx, disclosureInbound(tenant, "start", "LOG CASE"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Receive(ctx, disclosureInbound(tenant, "invalid-request", "SEND PROCESSING NOTICE")); err == nil {
			t.Fatal("invalid configured notice accepted")
		}
		var raw []byte
		if err = s.DB.QueryRowContext(ctx, `SELECT payload FROM addon_conversation_turn WHERE tenant=$1`, tenant).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var after Turn
		if err = json.Unmarshal(raw, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) || calls.Load() != 0 {
			t.Fatal("invalid notice changed turn or invoked advice")
		}
	}
}
