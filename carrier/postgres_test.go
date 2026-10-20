package carrier

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/url"
	"os"
	"testing"
	"time"
)

func carrierFixture(t *testing.T) (context.Context, Service) {
	t.Helper()
	dsn := os.Getenv("CARRIER_DATABASE_URL")
	if dsn == "" {
		t.Skip("CARRIER_DATABASE_URL required")
	}
	ctx := context.Background()
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("carrier_%d", time.Now().UnixNano())
	if _, e = base.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	for _, fn := range []func(context.Context) error{(&cases.SQLRepository{DB: db}).Migrate, (&cases.SQLRepository{DB: db}).MigrateOutboxDelivery, func(c context.Context) error { return conversation.Migrate(c, db) }, func(c context.Context) error { return Migrate(c, db) }, func(c context.Context) error { return (api.AccessStore{DB: db}).Migrate(c) }} {
		if e = fn(ctx); e != nil {
			t.Fatal(e)
		}
	}
	now := time.Now().UTC()
	store, e := directory.NewPostgresStore(db, "org", "fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Init(ctx); e != nil {
		t.Fatal(e)
	}
	preview, e := directory.NewPreview([]directory.Employee{{ID: "employee", Phone: "+27820000001", Zone: "zone", Department: "department", Active: true}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = directory.Apply(ctx, store, preview, preview.Digest(), now.Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	c := &conversation.Service{DB: db, Config: conversation.HTTPConfig{Clock: func() time.Time { return now }, Freshness: time.Hour, Aliases: (api.AccessStore{DB: db}).EmployeeAliases}}
	return ctx, Service{DB: db, Clock: func() time.Time { return now }, Conversation: c, Bindings: []Binding{{Tenant: "org", Account: "account", PhoneID: "phone", AppSecret: "secret"}}}
}
func TestDurableIngressReplayCaseAckAndUnknownOneShot(t *testing.T) {
	ctx, s := carrierFixture(t)
	accept := func(id, text string) {
		t.Helper()
		in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: id, Text: text, ProviderTimestamp: s.Clock().Add(-time.Second)}
		if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
			t.Fatal(e)
		}
		if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
			t.Fatal(e)
		}
		if more, e := s.ProcessOne(ctx, "org"); e != nil || !more {
			t.Fatal(more, e)
		}
	}
	accept("1", "log case")
	accept("2", "A synthetic workplace concern.")
	accept("3", "confirm")
	var count int
	if e := s.DB.QueryRow(`SELECT count(*) FROM addon_cases_outbox`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	calls := 0
	s.SendObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		calls++
		return delivery.Unknown, "", nil
	}
	if e := s.dispatchAcknowledgement(ctx, s.Bindings[0]); e != nil {
		t.Fatal(e)
	}
	if e := s.dispatchAcknowledgement(ctx, s.Bindings[0]); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("unknown acknowledgement retried", calls)
	}
	var state string
	if e := s.DB.QueryRow(`SELECT state FROM addon_cases_delivery`).Scan(&state); e != nil || state != "held_unknown" {
		t.Fatal(state, e)
	}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "3", Text: "changed", ProviderTimestamp: s.Clock().Add(-time.Second)}}, nil); e == nil {
		t.Fatal("changed replay accepted")
	}
}
func TestSignedWindowCannotBeRefreshedByDelayedProcessing(t *testing.T) {
	ctx, s := carrierFixture(t)
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "old", Text: "hello", ProviderTimestamp: s.Clock().Add(-25 * time.Hour)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	m := delivery.Message{Tenant: "org", Account: "account", Sender: "+27820000001"}
	if s.AuthorizeWindow(ctx, m) == nil {
		t.Fatal("old event opened window")
	}
	in.ProviderMessageID = "future"
	in.ProviderTimestamp = s.Clock().Add(time.Second)
	if s.AcceptBatch(ctx, []conversation.Inbound{in}, nil) == nil {
		t.Fatal("future event accepted")
	}
}
func TestAuthenticatedReceiptReplayCorrelationAndPrivacy(t *testing.T) {
	ctx, s := carrierFixture(t)
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "in", Text: "hello", ProviderTimestamp: s.Clock().Add(-time.Second)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProcessOne(ctx, "org"); e != nil {
		t.Fatal(e)
	}
	s.SendObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.Accepted, "provider", nil
	}
	w := delivery.Worker{DB: s.DB, Tenant: "org", Clock: s.Clock, LeaseDuration: time.Minute, Authorize: s.Conversation.AuthorizeDelivery, SendObserved: s.send}
	if _, e := w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	st := Status{Tenant: "org", Account: "account", ProviderID: "provider", ReceiptID: "receipt", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "signed-body-1"}
	if e := s.AcceptBatch(ctx, nil, []Status{st}); e != nil {
		t.Fatal(e)
	}
	st.Evidence = "signed-body-2"
	if e := s.AcceptBatch(ctx, nil, []Status{st}); e != nil {
		t.Fatal("same observation in another authenticated batch rejected", e)
	}
	if e := s.ReconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := s.DB.QueryRow(`SELECT state FROM addon_conversation_delivery`).Scan(&state); e != nil || state != "delivered" {
		t.Fatal(state, e)
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	actor := api.Actor{OrganizationID: "org", Permissions: []string{"conversation:privacy"}, Grants: []api.Grant{{Permission: "conversation:privacy", UnitID: "zone"}}}
	r := governance.Record{Scope: governance.Scope{OrganizationID: "org", UnitID: "zone"}, SubjectID: "employee"}
	if e = (PrivacyAdapter{}).Erase(ctx, tx, actor, r); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var payload string
	if e = s.DB.QueryRow(`SELECT payload::text FROM addon_carrier_receipt`).Scan(&payload); e != nil || payload != "{}" {
		t.Fatal("receipt escaped erasure", payload, e)
	}
	if e = s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	if more, e := s.ProcessOne(ctx, "org"); e != nil || more {
		t.Fatal("erased ingress replayed", more, e)
	}
}
func TestQueuedIngressUrgentRevocationAndReceiptConflictStayHeld(t *testing.T) {
	ctx, s := carrierFixture(t)
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "revoked", Text: "log case", ProviderTimestamp: s.Clock().Add(-time.Second)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
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
	if _, e = s.ProcessOne(ctx, "org"); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = s.DB.QueryRow(`SELECT state FROM addon_carrier_ingress`).Scan(&state); e != nil || state != "held" {
		t.Fatal(state, e)
	}
	var count int
	if e = s.DB.QueryRow(`SELECT count(*) FROM addon_conversation_inbound`).Scan(&count); e != nil || count != 0 {
		t.Fatal("revoked queued sender processed", count, e)
	}
	st := Status{Tenant: "org", Account: "account", ProviderID: "early", IntentID: "unmatched", ReceiptID: "early", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "signature"}
	if e = s.AcceptBatch(ctx, nil, []Status{st}); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcilePending(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.DB.QueryRow(`SELECT state FROM addon_carrier_receipt`).Scan(&state); e != nil || state != "pending" {
		t.Fatal("unmatched receipt invented success", state, e)
	}
	st.Outcome = "failed"
	if e = s.AcceptBatch(ctx, nil, []Status{st}); e == nil {
		t.Fatal("changed receipt observation accepted")
	}
}
func TestAcceptanceObservationIsDurableWithoutDelivery(t *testing.T) {
	ctx, s := carrierFixture(t)
	st := Status{Tenant: "org", Account: "account", ProviderID: "provider", ReceiptID: "accepted", Outcome: "accepted", ObservedAt: s.Clock(), Evidence: "authenticated"}
	if e := s.AcceptBatch(ctx, nil, []Status{st}); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := s.DB.QueryRow(`SELECT state FROM addon_carrier_receipt`).Scan(&state); e != nil || state != "observed" {
		t.Fatal("acceptance was not durable observation", state, e)
	}
}
func TestUnmatchedReceiptBacklogDoesNotStarveKnownReceipt(t *testing.T) {
	ctx, s := carrierFixture(t)
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "in", Text: "hello", ProviderTimestamp: s.Clock().Add(-time.Second)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProcessOne(ctx, "org"); e != nil {
		t.Fatal(e)
	}
	s.SendObserved = func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.Accepted, "known", nil
	}
	w := delivery.Worker{DB: s.DB, Tenant: "org", Clock: s.Clock, LeaseDuration: time.Minute, Authorize: s.Conversation.AuthorizeDelivery, SendObserved: s.send}
	if _, e := w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	var statuses []Status
	for i := range 100 {
		statuses = append(statuses, Status{Tenant: "org", Account: "account", ProviderID: fmt.Sprintf("unknown-%d", i), ReceiptID: fmt.Sprintf("a%03d", i), Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "signed"})
	}
	statuses = append(statuses, Status{Tenant: "org", Account: "account", ProviderID: "known", ReceiptID: "z-known", Outcome: "delivered", ObservedAt: s.Clock(), Evidence: "signed"})
	if e := s.AcceptBatch(ctx, nil, statuses); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := s.ReconcilePending(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var state string
	if e := s.DB.QueryRow(`SELECT state FROM addon_conversation_delivery`).Scan(&state); e != nil || state != "delivered" {
		t.Fatal("unmatched backlog starved known receipt", state, e)
	}
}
func TestQueuedEmployeeOwnershipIsImmutableAcrossPhoneReassignment(t *testing.T) {
	ctx, s := carrierFixture(t)
	in := conversation.Inbound{Tenant: "org", Account: "account", Sender: "27820000001", ProviderMessageID: "reassigned", Text: "log case", ProviderTimestamp: s.Clock().Add(-time.Second)}
	if e := s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	store, e := directory.NewPostgresStore(s.DB, "org", "reassign")
	if e != nil {
		t.Fatal(e)
	}
	snap, e := store.Load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	next := snap
	next.Version++
	next.Employees = map[string]directory.Employee{"replacement": {ID: "replacement", Phone: "+27820000001", Zone: "zone", Department: "department", Active: true}}
	if e = store.CompareAndSwap(ctx, snap.Version, next); e != nil {
		t.Fatal(e)
	}
	if e = s.AcceptBatch(ctx, []conversation.Inbound{in}, nil); e != nil {
		t.Fatal(e)
	}
	var employee string
	if e = s.DB.QueryRow(`SELECT employee_id FROM addon_carrier_ingress`).Scan(&employee); e != nil || employee != "employee" {
		t.Fatal("replay rewrote original queued employee", employee, e)
	}
	if _, e = s.ProcessOne(ctx, "org"); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = s.DB.QueryRow(`SELECT state FROM addon_carrier_ingress`).Scan(&state); e != nil || state != "held" {
		t.Fatal("reassigned sender processed original text", state, e)
	}
	var count int
	if e = s.DB.QueryRow(`SELECT count(*) FROM addon_conversation_inbound`).Scan(&count); e != nil || count != 0 {
		t.Fatal("replacement inherited old conversation", count, e)
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	actor := api.Actor{OrganizationID: "org", Permissions: []string{"conversation:privacy"}, Grants: []api.Grant{{Permission: "conversation:privacy", UnitID: "zone"}}}
	r := governance.Record{Scope: governance.Scope{OrganizationID: "org", UnitID: "zone"}, SubjectID: "employee"}
	if e = (PrivacyAdapter{}).Erase(ctx, tx, actor, r); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var payload string
	if e = s.DB.QueryRow(`SELECT payload::text FROM addon_carrier_ingress`).Scan(&payload); e != nil || payload != "{}" {
		t.Fatal("original employee could not erase ingress", payload, e)
	}
}
func TestConversationReceiveEnforcesCarrierBoundEmployee(t *testing.T) {
	ctx, s := carrierFixture(t)
	_, e := s.Conversation.Receive(ctx, conversation.Inbound{Tenant: "org", Account: "account", Sender: "+27820000001", ProviderMessageID: "mismatched-identity", Text: "log case", ExpectedEmployeeID: "another-employee", ExpectedUnitID: "zone"})
	if e == nil {
		t.Fatal("conversation bypassed carrier's immutable employee binding")
	}
}
