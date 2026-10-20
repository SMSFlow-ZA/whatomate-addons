package carrier

import (
	"context"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"testing"
	"time"
)

func TestKnownFailureSMSFallbackIsMinimalOneShotAndPolled(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	s.SendObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.VerifiedFailed, "wa-failed", nil
	}
	smsCalls := 0
	s.SendSMSObserved = func(_ context.Context, m delivery.Message) (delivery.Result, string, error) {
		smsCalls++
		if m.Channel != "sms" || m.Text != delivery.MinimalSMSNotice || m.ID == "" {
			t.Fatal("nonminimal SMS", m.Channel, m.Text)
		}
		return delivery.Accepted, "sms-provider", nil
	}
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "fallback", Text: "hello", ProviderTimestamp: s.Clock().Add(-25 * time.Hour)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProcessOne(ctx, "org"); e != nil {
		t.Fatal(e)
	}
	messages, e := delivery.List(ctx, s.DB, "org", "+27820000001")
	if e != nil || len(messages) != 1 {
		t.Fatal(len(messages), e)
	}
	id := messages[0].ID
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE addon_conversation_delivery SET state='accepted',provider_id='wa-failed' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	st := Status{Tenant: "org", Account: "account", ProviderID: "wa-failed", IntentID: id, ReceiptID: "wa-failure", Outcome: "failed", ObservedAt: s.Clock(), Evidence: "signed"}
	if e = s.AcceptBatch(ctx, nil, []Status{st}); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if smsCalls != 1 {
		t.Fatal("SMS fallback was not one-shot", smsCalls)
	}
	s.PollSMS = func(_ context.Context, tenant, account, cursor string) (ReceiptPage, error) {
		if cursor == "" {
			return ReceiptPage{Receipts: []Status{{Tenant: tenant, Account: account, ProviderID: "sms-provider", IntentID: id + ":sms-fallback", ReceiptID: "sms-delivered", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "authenticated-poll"}}, NextCursor: "next"}, nil
		}
		return ReceiptPage{NextCursor: cursor}, nil
	}
	if e = s.PollSMSReceipts(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	status, e := delivery.ReadDeliveryStatus(ctx, s.DB, "org", id+":sms-fallback")
	if e != nil || status.State != "delivered" {
		t.Fatal(status, e)
	}
	var cursor string
	if e = s.DB.QueryRow(`SELECT cursor FROM addon_carrier_sms_cursor WHERE tenant='org' AND account='sms'`).Scan(&cursor); e != nil || cursor != "next" {
		t.Fatal(cursor, e)
	}
}
func TestCaseAcknowledgementKnownFailureQueuesOneMinimalSMS(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	sms := 0
	s.SendObserved = func(_ context.Context, m delivery.Message) (delivery.Result, string, error) {
		if m.CaseID != "" {
			return delivery.VerifiedFailed, "ack-failed", nil
		}
		return delivery.Accepted, "reply:" + m.ID, nil
	}
	s.SendSMSObserved = func(_ context.Context, m delivery.Message) (delivery.Result, string, error) {
		sms++
		if m.Text != delivery.MinimalSMSNotice {
			t.Fatal("sensitive acknowledgement leaked to SMS")
		}
		return delivery.Unknown, "", nil
	}
	for i, text := range []string{"log case", "A synthetic workplace concern.", "confirm"} {
		if e := s.AcceptBatch(ctx, []conversation.Inbound{{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: fmt.Sprint(i), Text: text, ProviderTimestamp: s.Clock().Add(-time.Second)}}, nil); e != nil {
			t.Fatal(e)
		}
		if e := s.Tick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	for range 3 {
		if e := s.Tick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if sms != 1 {
		t.Fatal("ack fallback absent or retried", sms)
	}
	var count int
	if e := s.DB.QueryRow(`SELECT count(*) FROM addon_conversation_delivery WHERE payload->>'channel'='sms'`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestSMSPollInvalidPageCannotAdvanceCursorOrPartiallyPersist(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	s.PollSMS = func(context.Context, string, string, string) (ReceiptPage, error) {
		return ReceiptPage{Receipts: []Status{{Tenant: "org", Account: "sms", ProviderID: "valid", ReceiptID: "first", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "authenticated"}, {Tenant: "other", Account: "sms", ProviderID: "invalid", ReceiptID: "second", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "authenticated"}}, NextCursor: "must-not-commit"}, nil
	}
	if e := s.PollSMSReceipts(ctx); e == nil {
		t.Fatal("cross-tenant poll accepted")
	}
	for _, table := range []string{"addon_carrier_receipt", "addon_carrier_sms_cursor"} {
		var count int
		if e := s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); e != nil || count != 0 {
			t.Fatal("poll committed partial evidence or cursor", table, count, e)
		}
	}
}
func TestSMSRechecksCurrentEligibilityAndDoesNotChain(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	calls := 0
	s.SendSMSObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		calls++
		return delivery.VerifiedFailed, "sms-failed", nil
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	m := delivery.Message{ID: "notice:sms-fallback", Tenant: "org", Account: "sms", Channel: "sms", Sender: "+27820000001", EmployeeID: "employee", Text: delivery.MinimalSMSNotice}
	if e = delivery.Enqueue(ctx, tx, m); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("failed SMS retried or chained", calls)
	}
	var count int
	if e = s.DB.QueryRow(`SELECT count(*) FROM addon_conversation_delivery`).Scan(&count); e != nil || count != 1 {
		t.Fatal("SMS generated further fallback", count, e)
	}
	store, e := directory.NewPostgresStore(s.DB, "org", "revoker")
	if e != nil {
		t.Fatal(e)
	}
	snap, e := store.Load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	revoked, e := directory.Revoke(snap, "employee")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.CompareAndSwap(ctx, snap.Version, revoked); e != nil {
		t.Fatal(e)
	}
	tx, e = s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	m.ID = "revoked:sms-fallback"
	if e = delivery.Enqueue(ctx, tx, m); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("revoked employee dispatched SMS")
	}
	status, e := delivery.ReadDeliveryStatus(ctx, s.DB, "org", m.ID)
	if e != nil || status.State != "held" {
		t.Fatal(status, e)
	}
}
func TestUnknownWhatsAppNeverTriggersSMS(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	s.SendObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.Unknown, "", nil
	}
	calls := 0
	s.SendSMSObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		calls++
		return delivery.Accepted, "unwanted", nil
	}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "unknown", Text: "hello", ProviderTimestamp: s.Clock().Add(-time.Second)}}, nil); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if e := s.Tick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 0 {
		t.Fatal("unknown WA submitted SMS")
	}
	var count int
	if e := s.DB.QueryRow(`SELECT count(*) FROM addon_conversation_delivery WHERE payload->>'channel'='sms'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("unknown WA enqueued fallback", count, e)
	}
}
func TestSMSProviderUUIDCorrelationSurvivesUnknownAndDeniesRebinding(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	client := "11111111-1111-5111-8111-111111111111"
	m := delivery.Message{ID: "opaque:sms-fallback", Tenant: "org", Account: "sms", Channel: "sms", Sender: "+27820000001", EmployeeID: "employee", Text: delivery.MinimalSMSNotice}
	s.SendSMSObserved = func(ctx context.Context, m delivery.Message) (delivery.Result, string, error) {
		if e := s.BindSMSCorrelation(ctx, m, client); e != nil {
			t.Fatal(e)
		}
		return delivery.Unknown, "", nil
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = delivery.Enqueue(ctx, tx, m); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	id, e := s.ResolveSMSCorrelation(ctx, "org", "sms", "11111111111151118111111111111111")
	if e != nil || id != m.ID {
		t.Fatal(id, e)
	}
	if _, e = s.ResolveSMSCorrelation(ctx, "other", "sms", client); e == nil {
		t.Fatal("cross tenant correlation")
	}
	if _, e = s.ResolveSMSCorrelation(ctx, "org", "other", client); e == nil {
		t.Fatal("cross account correlation")
	}
	if e = s.BindSMSCorrelation(ctx, m, "22222222-2222-5222-8222-222222222222"); e == nil {
		t.Fatal("changed provider UUID accepted")
	}
}
func TestSMSPollOutageCannotBlockDurableWhatsAppReceipt(t *testing.T) {
	ctx, s := carrierFixture(t)
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "pending-wa", Text: "hello", ProviderTimestamp: s.Clock().Add(-time.Second)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProcessOne(ctx, "org"); e != nil {
		t.Fatal(e)
	}
	s.SendObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.Accepted, "wa-provider", nil
	}
	w := delivery.Worker{DB: s.DB, Tenant: "org", Clock: s.Clock, LeaseDuration: time.Minute, Authorize: s.Conversation.AuthorizeDelivery, SendObserved: s.send}
	if _, e := w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	st := Status{Tenant: "org", Account: "account", ProviderID: "wa-provider", ReceiptID: "wa-delivered", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "signed"}
	if e := s.AcceptBatch(ctx, nil, []Status{st}); e != nil {
		t.Fatal(e)
	}
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	s.PollSMS = func(context.Context, string, string, string) (ReceiptPage, error) {
		return ReceiptPage{}, fmt.Errorf("synthetic SMS outage")
	}
	if e := s.Tick(ctx); e == nil {
		t.Fatal("poll outage was hidden")
	}
	var state string
	if e := s.DB.QueryRow(`SELECT state FROM addon_conversation_delivery`).Scan(&state); e != nil || state != "delivered" {
		t.Fatal("SMS outage blocked durable WA receipt", state, e)
	}
}
func TestCaseSMSPrivacyIncludesQueuedDeliveryWithoutConversation(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	repo := &cases.SQLRepository{DB: s.DB}
	result, e := (cases.Service{Repository: repo}).Intake(ctx, cases.IntakeRequest{OrganizationID: "org", UnitID: "zone", ContactID: "employee", Key: "api-case", Narrative: "A synthetic case submitted through an API.", Snapshot: cases.Classification{Zone: "zone", Department: "department", CaseType: "employee_request"}})
	if e != nil || result.Outcome != cases.Committed {
		t.Fatal(result, e)
	}
	claim, ok, e := repo.ClaimOutbox(ctx, "org", "synthetic", s.Clock(), s.Clock().Add(time.Minute))
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e = repo.BindOutboxRoute(ctx, claim, "account"); e != nil {
		t.Fatal(e)
	}
	if e = repo.FinishOutbox(ctx, claim, "failed", "known-failure"); e != nil {
		t.Fatal(e)
	}
	if e = s.enqueueCaseFallbacks(ctx, s.Bindings[0]); e != nil {
		t.Fatal(e)
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	actor := api.Actor{OrganizationID: "org", UserID: "triage", Permissions: []string{"conversation:privacy", string(cases.ReadTriage)}, Grants: []api.Grant{{Permission: "conversation:privacy", UnitID: "zone"}, {Permission: string(cases.ReadTriage), UnitID: "zone"}}}
	scope := governance.Scope{OrganizationID: "org", UnitID: "zone"}
	records, e := (PrivacyAdapter{}).Discover(ctx, tx, actor, scope, "employee")
	if e != nil || len(records) == 0 {
		t.Fatal("queued case SMS omitted from privacy inventory", records, e)
	}
	noCase := actor
	noCase.Permissions = []string{"conversation:privacy"}
	if _, e = (PrivacyAdapter{}).Export(ctx, tx, noCase, records[0]); e == nil {
		t.Fatal("case-linked carrier privacy bypassed ordinary case access")
	}
	if e = (PrivacyAdapter{}).Erase(ctx, tx, actor, records[0]); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.DB.QueryRow(`SELECT count(*) FROM addon_conversation_delivery`).Scan(&count); e != nil || count != 0 {
		t.Fatal("queued fallback delivery escaped governed erasure", count, e)
	}
	for range 2 {
		if e = s.enqueueCaseFallbacks(ctx, s.Bindings[0]); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range []string{"addon_conversation_delivery", "addon_carrier_route"} {
		if e = s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); e != nil || count != 0 {
			t.Fatal("erased case SMS reappeared after scan wrap", table, count, e)
		}
	}
}
func TestErasedSMSReceiptReplayDoesNotStallNewPollEvidence(t *testing.T) {
	ctx, s := carrierFixture(t)
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	oldClient, newClient := "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"
	s.SendSMSObserved = func(ctx context.Context, m delivery.Message) (delivery.Result, string, error) {
		client := oldClient
		if m.ID == "new:sms-fallback" {
			client = newClient
		}
		if e := s.BindSMSCorrelation(ctx, m, client); e != nil {
			t.Fatal(e)
		}
		return delivery.Accepted, "provider:" + m.ID, nil
	}
	enqueue := func(id string) {
		t.Helper()
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = delivery.Enqueue(ctx, tx, delivery.Message{ID: id, Tenant: "org", Account: "sms", Channel: "sms", Sender: "+27820000001", EmployeeID: "employee", Text: delivery.MinimalSMSNotice}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = s.TickDispatch(ctx); e != nil {
			t.Fatal(e)
		}
	}
	enqueue("old:sms-fallback")
	status := func(receiptID, provider, client string) Status {
		intent, _ := s.ResolveSMSCorrelation(ctx, "org", "sms", client)
		return Status{Tenant: "org", Account: "sms", ProviderID: provider, ProviderClientID: client, IntentID: intent, ReceiptID: receiptID, Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "authenticated-poll:" + client}
	}
	s.PollSMS = func(_ context.Context, tenant, account, cursor string) (ReceiptPage, error) {
		if cursor == "" {
			return ReceiptPage{Receipts: []Status{status("old-event", "provider:old:sms-fallback", oldClient)}, NextCursor: "first"}, nil
		}
		return ReceiptPage{Receipts: []Status{status("old-event", "provider:old:sms-fallback", "11111111111151118111111111111111"), status("new-event", "provider:new:sms-fallback", newClient)}, NextCursor: "second"}, nil
	}
	if e := s.PollSMSReceipts(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.ReconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	actor := api.Actor{OrganizationID: "org", Permissions: []string{"conversation:privacy"}, Grants: []api.Grant{{Permission: "conversation:privacy", UnitID: "zone"}}}
	if e = (PrivacyAdapter{}).Erase(ctx, tx, actor, governance.Record{Scope: governance.Scope{OrganizationID: "org", UnitID: "zone"}, SubjectID: "employee", Source: governance.Conversation}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	enqueue("new:sms-fallback")
	if e = s.PollSMSReceipts(ctx); e != nil {
		t.Fatal("erased duplicate pinned the receipt cursor", e)
	}
	if e = s.ReconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	current, e := delivery.ReadDeliveryStatus(ctx, s.DB, "org", "new:sms-fallback")
	if e != nil || current.State != "delivered" {
		t.Fatal("new receipt lost behind erased duplicate", current, e)
	}
	var cursor, erasedState, erasedPayload string
	if e = s.DB.QueryRow(`SELECT cursor FROM addon_carrier_sms_cursor WHERE tenant='org' AND account='sms'`).Scan(&cursor); e != nil || cursor != "second" {
		t.Fatal(cursor, e)
	}
	if e = s.DB.QueryRow(`SELECT state,payload::text FROM addon_carrier_receipt WHERE id='old-event'`).Scan(&erasedState, &erasedPayload); e != nil || erasedState != "erased" || erasedPayload != "{}" {
		t.Fatal("receipt replay reconstructed erased evidence", erasedState, erasedPayload, e)
	}
	s.PollSMS = func(context.Context, string, string, string) (ReceiptPage, error) {
		return ReceiptPage{Receipts: []Status{status("old-event", "provider:old:sms-fallback", newClient)}, NextCursor: "third"}, nil
	}
	if e = s.PollSMSReceipts(ctx); e == nil {
		t.Fatal("changed immutable provider client UUID accepted")
	}
	if e = s.DB.QueryRow(`SELECT cursor FROM addon_carrier_sms_cursor WHERE tenant='org' AND account='sms'`).Scan(&cursor); e != nil || cursor != "second" {
		t.Fatal("conflicting provider proof advanced cursor", cursor, e)
	}
}
