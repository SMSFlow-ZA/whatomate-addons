//go:build integration

package notifications

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) (Service, cases.Service, Identity) {
	t.Helper()
	dsn := os.Getenv("CASES_DATABASE_URL")
	if dsn == "" {
		t.Skip("CASES_DATABASE_URL not supplied")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	repo := &cases.SQLRepository{DB: db}
	for _, fn := range []func() error{func() error { return repo.Migrate(ctx) }, func() error { return delivery.Migrate(ctx, db) }, func() error { return Migrate(ctx, db) }} {
		if e = fn(); e != nil {
			t.Fatal(e)
		}
	}
	org := "notifications-" + time.Now().Format("150405.000000000")
	_, identity, snapshot, _ := fixture()
	identity.OrganizationID = org
	identity.Principal.Grants[0].OrganizationID = org
	store, _ := directory.NewPostgresStore(db, org, "test")
	if e = store.Init(ctx); e != nil {
		t.Fatal(e)
	}
	if e = store.CompareAndSwap(ctx, 0, snapshot); e != nil {
		t.Fatal(e)
	}
	s := Service{DB: db, Tenant: org, Account: "account", Units: []string{"unit"}, Clock: time.Now, Freshness: time.Hour, Resolve: func(_ context.Context, org, user string) (Identity, error) { return identity, nil }}
	return s, cases.Service{Repository: repo}, identity
}
func escalate(t *testing.T, s Service, c cases.Service, key string) cases.Case {
	t.Helper()
	ctx := context.Background()
	result, e := c.Intake(ctx, cases.IntakeRequest{OrganizationID: s.Tenant, UnitID: "unit", ContactID: "reporter", Key: key})
	if e != nil {
		t.Fatal(e)
	}
	p := cases.Principal{ID: "triage", Grants: []cases.Grant{{OrganizationID: s.Tenant, UnitID: "unit", Permission: cases.AssignTriage}}}
	i, e := s.Resolve(ctx, s.Tenant, "handler")
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.Escalate(ctx, p, result.CaseID, 1, []cases.Candidate{{Principal: i.Principal, Role: cases.Handler}}, "synthetic", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func messages(t *testing.T, s Service) []delivery.Message {
	t.Helper()
	out, e := delivery.List(context.Background(), s.DB, s.Tenant, "+12025550101")
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestProjectionActivationConcurrentReplayAndUnknownNoFallback(t *testing.T) {
	ctx := context.Background()
	s, c, _ := setup(t)
	escalate(t, s, c, "historical")
	if _, e := s.Project(ctx); e == nil {
		t.Fatal("projection auto-activated")
	}
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	changed := s
	changed.Account = "other"
	if e := changed.Activate(ctx); e == nil {
		t.Fatal("route activation silently changed")
	}
	fresh := escalate(t, s, c, "fresh")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, e := s.Project(ctx); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	got := messages(t, s)
	if len(got) != 1 || got[0].CaseID != fresh.ID || got[0].Text != Notice || got[0].Purpose != delivery.HandlerEscalation {
		t.Fatalf("unexpected notice: %+v", got)
	}
	if e := s.Authorize(ctx, got[0]); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*delivery.Message){func(m *delivery.Message) { m.Text = "private detail" }, func(m *delivery.Message) { m.Sender = "+12025550102" }, func(m *delivery.Message) { m.RecipientUserID = "other" }, func(m *delivery.Message) { m.EventVersion++ }, func(m *delivery.Message) { m.Purpose = "unknown" }} {
		bad := got[0]
		change(&bad)
		if s.Authorize(ctx, bad) == nil {
			t.Fatal("tampered intent authorized")
		}
	}
	calls := 0
	worker := delivery.Worker{DB: s.DB, Tenant: s.Tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: s.Authorize, SMSFallback: &delivery.SMSFallbackConfig{Account: "sms"}, SendObserved: func(context.Context, delivery.Message) (delivery.Result, string, error) {
		calls++
		return delivery.Unknown, "", errors.New("timeout")
	}}
	if more, e := worker.Dispatch(ctx); e != nil || !more {
		t.Fatalf("dispatch: %v %v", more, e)
	}
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	if more, e := worker.Dispatch(ctx); e != nil || more {
		t.Fatalf("unknown resent: %v %v", more, e)
	}
	if got = messages(t, s); len(got) != 1 || got[0].State != "held_unknown" || calls != 1 {
		t.Fatalf("unknown fallback/replay: %d %+v", calls, got)
	}
}

func TestProjectionRevocationAndReceiptRequireCurrentAuthority(t *testing.T) {
	ctx := context.Background()
	s, c, i := setup(t)
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	escalate(t, s, c, "fresh")
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	m := messages(t, s)[0]
	worker := delivery.Worker{DB: s.DB, Tenant: s.Tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: s.Authorize, SendObserved: func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.Accepted, "provider", nil
	}}
	if _, e := worker.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	receipt := api.Receipt{Tenant: s.Tenant, Account: s.Account, IntentID: m.ID, ProviderID: "provider", ReceiptID: "receipt", Outcome: "delivered", ObservedAt: time.Now(), Evidence: "signed"}
	r := delivery.Reconciler{DB: s.DB, Authorize: s.Authorize, Verify: func(context.Context, api.Receipt) error { return nil }}
	s.Resolve = func(context.Context, string, string) (Identity, error) { bad := i; bad.Active = false; return bad, nil }
	r.Authorize = s.Authorize
	if e := r.Reconcile(ctx, receipt); e == nil {
		t.Fatal("revoked receipt authorized")
	}
	if got := messages(t, s); got[0].State != "accepted" {
		t.Fatal("revocation overwrote honest accepted state")
	}
	s.Resolve = func(context.Context, string, string) (Identity, error) { return i, nil }
	r.Authorize = s.Authorize
	wrong := receipt
	wrong.Account = "other"
	if r.Reconcile(ctx, wrong) == nil {
		t.Fatal("wrong account receipt accepted")
	}
	wrong = receipt
	wrong.ProviderID = "other"
	if r.Reconcile(ctx, wrong) == nil {
		t.Fatal("wrong provider receipt accepted")
	}
	if e := r.Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
	if e := r.Reconcile(ctx, receipt); e != nil {
		t.Fatal(e)
	}
}

func TestDeliveryIntentConflictAndLegacyReplay(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	m := delivery.Message{ID: s.Tenant + "legacy", Tenant: s.Tenant, Account: "account", Sender: "+12025550101", EmployeeID: "employee", Text: "reply"}
	enqueue := func(m delivery.Message) error {
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = delivery.Enqueue(ctx, tx, m); e != nil {
			return e
		}
		return tx.Commit()
	}
	if e := enqueue(m); e != nil {
		t.Fatal(e)
	}
	m.State = "delivered"
	m.Lease = "transient"
	if e := enqueue(m); e != nil {
		t.Fatal(e)
	}
	m.Text = "different"
	if e := enqueue(m); e == nil {
		t.Fatal("different duplicate payload accepted")
	}
	m.Text = "reply"
	m.Tenant = "other"
	if e := enqueue(m); e == nil {
		t.Fatal("cross-tenant ID conflict accepted")
	}
}

func TestGovernedTombstonePreventsProjectionReplay(t *testing.T) {
	s, c, _ := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	escalate(t, s, c, "fresh")
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	m := messages(t, s)[0]
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = EraseIntent(ctx, tx, s.Tenant, m.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	if len(messages(t, s)) != 0 {
		t.Fatal("erased notice replayed")
	}
	if s.Authorize(ctx, m) == nil {
		t.Fatal("erased notice authorized")
	}
}

func TestProjectionLateCommitAfterCursorWrapIsNotLost(t *testing.T) {
	s, c, identity := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	result, e := c.Intake(ctx, cases.IntakeRequest{OrganizationID: s.Tenant, UnitID: "unit", ContactID: "reporter", Key: "late"})
	if e != nil {
		t.Fatal(e)
	}
	pending, e := c.Repository.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer pending.Rollback(ctx)
	v, e := pending.Load(ctx, result.CaseID)
	if e != nil {
		t.Fatal(e)
	}
	v.Version++
	v.State = cases.Escalated
	v.Assignments = []cases.Assignment{{ID: identity.UserID, Role: cases.Handler}}
	v.Events = append(v.Events, cases.Event{Kind: "escalation", Version: v.Version, ActorID: "triage", OrganizationID: s.Tenant, UnitID: "unit", Assignments: v.Assignments, RecordedAt: time.Now(), OccurredAt: time.Now(), From: cases.Submitted, To: cases.Escalated})
	if e = pending.Save(ctx, v, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.ExecContext(ctx, `UPDATE addon_notifications_activation SET last_case='zzzz',last_version=1 WHERE tenant=$1`, s.Tenant); e != nil {
		t.Fatal(e)
	}
	if n, e := s.Project(ctx); e != nil || n != 0 {
		t.Fatalf("uncommitted event escaped: %d %v", n, e)
	}
	if outcome, e := pending.Commit(ctx); e != nil || outcome != cases.Committed {
		t.Fatalf("late commit: %s %v", outcome, e)
	}
	if n, e := s.Project(ctx); e != nil || n != 1 {
		t.Fatalf("late event lost: %d %v", n, e)
	}
	if len(messages(t, s)) != 1 {
		t.Fatal("late committed event missing")
	}
}

func TestProjectionConflictingEnqueueRollsBackIntentAndCursor(t *testing.T) {
	s, c, _ := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	v := escalate(t, s, c, "conflict")
	id := intentID(s.Tenant, v.ID, v.Version, "handler")
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = delivery.Enqueue(ctx, tx, delivery.Message{ID: id, Tenant: s.Tenant, Sender: "+12025550101", Text: "different meaning"}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Project(ctx); e == nil {
		t.Fatal("conflicting durable payload silently projected")
	}
	var count int
	var after string
	if e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_notifications_intent WHERE tenant=$1`, s.Tenant).Scan(&count); e != nil || count != 0 {
		t.Fatalf("partial projection: %d %v", count, e)
	}
	if e = s.DB.QueryRowContext(ctx, `SELECT last_case FROM addon_notifications_activation WHERE tenant=$1`, s.Tenant).Scan(&after); e != nil || after != "" {
		t.Fatalf("cursor consumed failed event: %q %v", after, e)
	}
}

func TestHandlerDefiniteFailureDoesNotUseEmployeeSMSFallback(t *testing.T) {
	s, c, _ := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	escalate(t, s, c, "failure")
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	w := delivery.Worker{DB: s.DB, Tenant: s.Tenant, Clock: time.Now, LeaseDuration: time.Minute, Authorize: s.Authorize, SMSFallback: &delivery.SMSFallbackConfig{Account: "sms"}, SendObserved: func(context.Context, delivery.Message) (delivery.Result, string, error) {
		return delivery.VerifiedFailed, "", nil
	}}
	if _, e := w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if got := messages(t, s); len(got) != 1 || got[0].State != "failed" {
		t.Fatalf("implicit handler fallback: %+v", got)
	}
}

func TestRestoredIntentCannotOverrideIndependentNotificationTombstone(t *testing.T) {
	s, c, _ := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	v := escalate(t, s, c, "restore")
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	m := messages(t, s)[0]
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = EraseIntent(ctx, tx, s.Tenant, m.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	// An old business-data snapshot can contain a queued projection while the
	// independently preserved deletion journal/tombstone is newer.
	if _, e = s.DB.ExecContext(ctx, `INSERT INTO addon_notifications_intent(tenant,id,case_id,unit_id,event_version,recipient_user,state) VALUES($1,$2,$3,'unit',$4,'handler','queued')`, s.Tenant, m.ID, v.ID, v.Version); e != nil {
		t.Fatal(e)
	}
	if s.Authorize(ctx, m) == nil {
		t.Fatal("restored intent bypassed independent tombstone")
	}
}

func TestTransientIdentityFailureDoesNotConsumeCommittedEvent(t *testing.T) {
	s, c, identity := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	escalate(t, s, c, "transient")
	s.Resolve = func(context.Context, string, string) (Identity, error) {
		return Identity{}, errors.New("temporary identity database unavailable")
	}
	if _, e := s.Project(ctx); e == nil {
		t.Fatal("temporary resolver failure permanently consumed event")
	}
	s.Resolve = func(context.Context, string, string) (Identity, error) { return identity, nil }
	if n, e := s.Project(ctx); e != nil || n != 1 {
		t.Fatalf("recovered projection: %d %v", n, e)
	}
	if len(messages(t, s)) != 1 {
		t.Fatal("recovered identity did not enqueue notice")
	}
}
