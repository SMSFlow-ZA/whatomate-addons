//go:build integration

package conversation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
)

func disclosureService(t *testing.T) (context.Context, Service, string, *atomic.Int32) {
	t.Helper()
	ctx, s, tenant := fixture(t)
	calls := &atomic.Int32{}
	s.Config.ProcessingDisclosure = &ProcessingDisclosure{Revision: "test-v1", Text: "Synthetic notice: international model processing; stored data remains in ZA."}
	s.Config.Advisor = func(context.Context, api.Actor, string) (Answer, error) {
		calls.Add(1)
		return Answer{Text: "Synthetic approved policy", Citations: []string{"approved-ref"}}, nil
	}
	return ctx, s, tenant, calls
}
func disclosureInbound(tenant, id, text string) Inbound {
	return Inbound{Tenant: tenant, Account: "wa-account", Sender: "+27820000001", ProviderMessageID: id, Text: text}
}

// Genuine HMAC verification of entirely synthetic local transport observations;
// this is never evidence that an actual employee received or consented.
func disclosureReceipt(r api.Receipt) (api.Receipt, api.ReceiptVerifier) {
	key := []byte("synthetic-disclosure-test-receipt-key")
	sign := func(value api.Receipt) string {
		value.Evidence = ""
		data, _ := json.Marshal(value)
		mac := hmac.New(sha256.New, key)
		mac.Write(data)
		return hex.EncodeToString(mac.Sum(nil))
	}
	r.Evidence = sign(r)
	return r, func(_ context.Context, value api.Receipt) error {
		if !hmac.Equal([]byte(sign(value)), []byte(value.Evidence)) {
			return api.ErrDenied
		}
		return nil
	}
}
func observeNotice(t *testing.T, ctx context.Context, s Service, tenant, id string, submission delivery.Result, receiptOutcome string) {
	t.Helper()
	worker := delivery.Worker{DB: s.DB, Tenant: tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: s.AuthorizeDelivery, SendObserved: func(_ context.Context, m delivery.Message) (delivery.Result, string, error) {
		return submission, "synthetic-provider-" + m.ID, nil
	}}
	var status delivery.DeliveryStatus
	for range 100 {
		var err error
		status, err = delivery.ReadDeliveryStatus(ctx, s.DB, tenant, id)
		if err != nil {
			t.Fatal(err)
		}
		if status.ProviderID != "" {
			break
		}
		more, err := worker.Dispatch(ctx)
		if err != nil || !more {
			t.Fatal("synthetic notice not submitted", err)
		}
	}
	if status.ProviderID == "" {
		t.Fatal("notice submission missing")
	}
	if receiptOutcome == "" {
		return
	}
	receipt, verify := disclosureReceipt(api.Receipt{Tenant: tenant, Account: status.Account, IntentID: id, ProviderID: status.ProviderID, ReceiptID: id + ":" + receiptOutcome, ObservedAt: time.Now().UTC(), Outcome: receiptOutcome})
	if err := (delivery.Reconciler{DB: s.DB, Verify: verify, Authorize: s.AuthorizeDelivery}).Reconcile(ctx, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingDisclosureFirstQuestionQueuesNoticeWithoutAdvisor(t *testing.T) {
	ctx, s, tenant, calls := disclosureService(t)
	in := disclosureInbound(tenant, "notice-first", "What is the SOP?")
	notice, err := s.Receive(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || !strings.Contains(notice.Reply, s.Config.ProcessingDisclosure.Text) || notice.ProcessingDisclosureIntentID == "" || notice.ProcessingDisclosureKey == "" {
		t.Fatalf("first question bypassed disclosure: calls=%d turn=%+v", calls.Load(), notice)
	}
	repeated, err := s.Receive(ctx, in)
	if err != nil || calls.Load() != 0 || repeated.ProcessingDisclosureIntentID != notice.ProcessingDisclosureIntentID {
		t.Fatal("notice replay invoked advisor or duplicated intent", err)
	}
	s = Service{DB: s.DB, Config: s.Config}
	in.ProviderMessageID = "notice-pending"
	held, err := s.Receive(ctx, in)
	if err != nil || calls.Load() != 0 || held.ProcessingDisclosureIntentID != notice.ProcessingDisclosureIntentID {
		t.Fatal("queued notice was treated as delivered after restart", err)
	}
	observeNotice(t, ctx, s, tenant, notice.ProcessingDisclosureIntentID, delivery.Accepted, "delivered")
	status, err := delivery.ReadDeliveryStatus(ctx, s.DB, tenant, notice.ProcessingDisclosureIntentID)
	if err != nil || status.EmployeeID != "employee" || status.Sender != in.Sender {
		t.Fatal("owner identity port lost notice binding", err)
	}
	public, _ := json.Marshal(status)
	if strings.Contains(string(public), "employee") || strings.Contains(string(public), in.Sender) {
		t.Fatal("internal identity binding exposed through status JSON")
	}
	in.ProviderMessageID = "notice-delivered"
	answered, err := s.Receive(ctx, in)
	if err != nil || calls.Load() != 1 || len(answered.Citations) != 1 {
		t.Fatal("verified delivered notice did not release later advice", err)
	}
}
func TestProcessingDisclosureUnverifiedStatesAndFallbackNeverUnlock(t *testing.T) {
	for _, state := range []delivery.Result{delivery.Accepted, delivery.VerifiedSent, delivery.Unknown, delivery.VerifiedFailed} {
		t.Run(string(state), func(t *testing.T) {
			ctx, s, tenant, calls := disclosureService(t)
			notice, err := s.Receive(ctx, disclosureInbound(tenant, "first", "Policy question"))
			if err != nil {
				t.Fatal(err)
			}
			observeNotice(t, ctx, s, tenant, notice.ProcessingDisclosureIntentID, state, "")
			if _, err = s.Receive(ctx, disclosureInbound(tenant, "after-submission", "Policy question again")); err != nil || calls.Load() != 0 {
				t.Fatal("non-delivery state released model", state, err)
			}
		})
	}
	t.Run("fallback", func(t *testing.T) {
		ctx, s, tenant, calls := disclosureService(t)
		notice, err := s.Receive(ctx, disclosureInbound(tenant, "first", "Policy question"))
		if err != nil {
			t.Fatal(err)
		}
		observeNotice(t, ctx, s, tenant, notice.ProcessingDisclosureIntentID, delivery.Accepted, "failed")
		fallbackID := notice.ProcessingDisclosureIntentID + ":sms-fallback"
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = delivery.Enqueue(ctx, tx, delivery.Message{ID: fallbackID, Tenant: tenant, Account: "sms-account", Sender: "+27820000001", EmployeeID: "employee", Text: delivery.MinimalSMSNotice}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		observeNotice(t, ctx, s, tenant, fallbackID, delivery.Accepted, "delivered")
		if _, err = s.Receive(ctx, disclosureInbound(tenant, "after-fallback", "SOP question again")); err != nil || calls.Load() != 0 {
			t.Fatal("minimal SMS fallback counted as processing notice", err)
		}
	})
}
func TestProcessingDisclosureRevisionTextAndEmployeeChanges(t *testing.T) {
	ctx, s, tenant, calls := disclosureService(t)
	first, err := s.Receive(ctx, disclosureInbound(tenant, "first", "Policy question"))
	if err != nil {
		t.Fatal(err)
	}
	observeNotice(t, ctx, s, tenant, first.ProcessingDisclosureIntentID, delivery.Accepted, "delivered")
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "answered", "Policy question")); err != nil || calls.Load() != 1 {
		t.Fatal(err)
	}
	s.Config.ProcessingDisclosure = &ProcessingDisclosure{Revision: "test-v2", Text: s.Config.ProcessingDisclosure.Text}
	revision, err := s.Receive(ctx, disclosureInbound(tenant, "revision", "Policy question"))
	if err != nil || revision.ProcessingDisclosureKey == first.ProcessingDisclosureKey || calls.Load() != 1 {
		t.Fatal("revision did not renew disclosure", err)
	}
	s.Config.ProcessingDisclosure = &ProcessingDisclosure{Revision: "test-v2", Text: "Updated synthetic international processing disclosure."}
	text, err := s.Receive(ctx, disclosureInbound(tenant, "text", "Policy question"))
	if err != nil || text.ProcessingDisclosureKey == revision.ProcessingDisclosureKey || calls.Load() != 1 {
		t.Fatal("changed text reused prior notice", err)
	}
	store, err := directory.NewPostgresStore(s.DB, tenant, "synthetic-reassignment")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := directory.NewPreview([]directory.Employee{{ID: "employee", Phone: "+27820000002", Zone: "zone", Department: "department", Active: false}, {ID: "new-employee", Phone: "+27820000001", Zone: "zone", Department: "department", Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = directory.Apply(ctx, store, preview, preview.Digest(), s.Config.Clock()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "first", "Policy question")); err == nil {
		t.Fatal("old employee replay crossed identity reassignment")
	}
	reassigned, err := s.Receive(ctx, disclosureInbound(tenant, "reassigned", "Policy question"))
	if err != nil || reassigned.EmployeeID != "new-employee" || reassigned.ProcessingDisclosureIntentID == text.ProcessingDisclosureIntentID || calls.Load() != 1 {
		t.Fatal("new employee inherited old notice", err)
	}
	if err = directory.UrgentRevoke(ctx, store, "new-employee"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "revoked", "Policy question")); err == nil || calls.Load() != 1 {
		t.Fatal("revoked employee reached disclosure/advisor")
	}
}
func TestProcessingDisclosureNeverBlocksCaseStatusOrHuman(t *testing.T) {
	ctx, s, tenant, calls := disclosureService(t)
	var turn Turn
	for i, text := range []string{"log case", "A synthetic workplace concern needs human review", "confirm", "status", "human"} {
		var err error
		turn, err = s.Receive(ctx, disclosureInbound(tenant, fmt.Sprint(i), text))
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 0 {
			t.Fatal("case workflow reached model")
		}
	}
	if turn.Selected == "" || len(turn.Cases) != 1 {
		t.Fatal("case/status workflow failed behind notice")
	}
	notice, err := s.Receive(ctx, disclosureInbound(tenant, "notice-after-case", "Policy question"))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := delivery.List(ctx, s.DB, tenant, "+27820000001")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range messages {
		if m.ID == notice.ProcessingDisclosureIntentID {
			found = true
			if m.CaseID != "" {
				t.Fatal("processing notice unnecessarily depended on selected case")
			}
		}
	}
	if !found {
		t.Fatal("processing notice not queued")
	}
	if _, err = s.Receive(ctx, disclosureInbound(tenant, "status-after-notice", "status")); err != nil || calls.Load() != 0 {
		t.Fatal("pending notice blocked case status", err)
	}
}
func TestProcessingDisclosureConcurrentFirstQueriesAndRollback(t *testing.T) {
	t.Run("concurrent", func(t *testing.T) {
		ctx, s, tenant, calls := disclosureService(t)
		var workers sync.WaitGroup
		results := make(chan Turn, 2)
		failures := make(chan error, 2)
		for i := range 2 {
			workers.Add(1)
			go func(i int) {
				defer workers.Done()
				turn, err := s.Receive(ctx, disclosureInbound(tenant, fmt.Sprintf("first-%d", i), "Policy question"))
				results <- turn
				failures <- err
			}(i)
		}
		workers.Wait()
		close(results)
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		var intent string
		for turn := range results {
			if intent == "" {
				intent = turn.ProcessingDisclosureIntentID
			}
			if turn.ProcessingDisclosureIntentID != intent {
				t.Fatal("concurrent queries generated multiple notices")
			}
		}
		if calls.Load() != 0 || intent == "" {
			t.Fatal("concurrent first query invoked model")
		}
	})
	t.Run("rollback", func(t *testing.T) {
		ctx, s, tenant, calls := disclosureService(t)
		name := fmt.Sprintf("notice_failure_%d", time.Now().UnixNano())
		if _, err := s.DB.ExecContext(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic notice transaction failure'; END $$; CREATE TRIGGER `+name+` BEFORE INSERT ON addon_conversation_inbound FOR EACH ROW WHEN (NEW.tenant='`+tenant+`') EXECUTE FUNCTION `+name+`()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			s.DB.Exec(`DROP TRIGGER IF EXISTS ` + name + ` ON addon_conversation_inbound;DROP FUNCTION IF EXISTS ` + name + `()`)
		})
		in := disclosureInbound(tenant, "retry-stable", "Policy question")
		if _, err := s.Receive(ctx, in); err == nil {
			t.Fatal("forced transaction failure ignored")
		}
		var count int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM addon_conversation_turn WHERE tenant=$1`, tenant).Scan(&count); err != nil || count != 0 {
			t.Fatal("notice state escaped rollback", err)
		}
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM addon_conversation_delivery WHERE tenant=$1`, tenant).Scan(&count); err != nil || count != 0 {
			t.Fatal("notice queue escaped rollback", err)
		}
		if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER `+name+` ON addon_conversation_inbound`); err != nil {
			t.Fatal(err)
		}
		notice, err := s.Receive(ctx, in)
		if err != nil || notice.ProcessingDisclosureIntentID == "" || calls.Load() != 0 {
			t.Fatal("failed notice retry invoked model or lost durable intent", err)
		}
	})
}

func TestProcessingDisclosureRejectsDifferentDeliveryBindings(t *testing.T) {
	ctx, s, tenant, _ := disclosureService(t)
	in := disclosureInbound(tenant, "initial", "Policy question")
	notice, err := s.Receive(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	observeNotice(t, ctx, s, tenant, notice.ProcessingDisclosureIntentID, delivery.Accepted, "delivered")
	for _, field := range []string{"account", "employee", "sender", "tenant"} {
		t.Run(field, func(t *testing.T) {
			wrong := delivery.Message{ID: tenant + ":" + field, Tenant: tenant, Account: in.Account, Sender: in.Sender, EmployeeID: "employee", Text: "Synthetic notice fixture"}
			switch field {
			case "account":
				wrong.Account = "other-account"
			case "employee":
				wrong.EmployeeID = "other-employee"
			case "sender":
				wrong.Sender = "+27820000002"
			case "tenant":
				wrong.Tenant = tenant + "-other"
			}
			tx, e := s.DB.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = delivery.Enqueue(ctx, tx, wrong); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			// Trusted local synthetic transport only, allowing the deliberately mismatched
			// owner fixture so the conversation boundary must independently reject it.
			authorize := func(context.Context, delivery.Message) error { return nil }
			worker := delivery.Worker{DB: s.DB, Tenant: wrong.Tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: authorize, SendObserved: func(_ context.Context, m delivery.Message) (delivery.Result, string, error) {
				return delivery.Accepted, "synthetic-provider-" + m.ID, nil
			}}
			for range 100 {
				status, e := delivery.ReadDeliveryStatus(ctx, s.DB, wrong.Tenant, wrong.ID)
				if e != nil {
					t.Fatal(e)
				}
				if status.ProviderID != "" {
					break
				}
				if _, e = worker.Dispatch(ctx); e != nil {
					t.Fatal(e)
				}
			}
			receipt, verify := disclosureReceipt(api.Receipt{Tenant: wrong.Tenant, Account: wrong.Account, IntentID: wrong.ID, ProviderID: "synthetic-provider-" + wrong.ID, ReceiptID: wrong.ID + ":delivered", ObservedAt: time.Now(), Outcome: "delivered"})
			if e = (delivery.Reconciler{DB: s.DB, Verify: verify, Authorize: authorize}).Reconcile(ctx, receipt); e != nil {
				t.Fatal(e)
			}
			state := notice
			state.ProcessingDisclosureIntentID = wrong.ID
			ready, e := s.advisoryDisclosureReady(ctx, in, &state)
			if e != nil || ready {
				t.Fatal("different notice identity unlocked advisor", field, e)
			}
		})
	}
}

func TestProcessingDisclosureOptionalAndInvalidConfiguration(t *testing.T) {
	ctx, s, tenant, calls := disclosureService(t)
	s.Config.ProcessingDisclosure = nil
	if _, err := s.Receive(ctx, disclosureInbound(tenant, "legacy", "Policy question")); err != nil || calls.Load() != 1 {
		t.Fatal("optional notice changed legacy composition", err)
	}
	s.Config.ProcessingDisclosure = &ProcessingDisclosure{Text: "Missing revision"}
	if _, err := s.Receive(ctx, disclosureInbound(tenant, "invalid", "Policy question")); err == nil || calls.Load() != 1 {
		t.Fatal("incomplete notice reached advisor")
	}
	if _, err := s.Receive(ctx, disclosureInbound(tenant, "human", "human")); err != nil || calls.Load() != 1 {
		t.Fatal("invalid notice blocked human route", err)
	}
}
