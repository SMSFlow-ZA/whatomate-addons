//go:build integration

package conversation

import (
	"context"
	"database/sql"
	"fmt"
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

func fixture(t *testing.T) (context.Context, Service, string) {
	t.Helper()
	url := os.Getenv("CONVERSATION_DATABASE_URL")
	if url == "" {
		t.Skip("CONVERSATION_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", url)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, fn := range []func(context.Context) error{(&cases.SQLRepository{DB: db}).Migrate, func(c context.Context) error { return (api.AccessStore{DB: db}).Migrate(c) }, func(c context.Context) error { return Migrate(c, db) }} {
		if e = fn(ctx); e != nil {
			t.Fatal(e)
		}
	}
	tenant := fmt.Sprintf("conversation-%d", time.Now().UnixNano())
	store, e := directory.NewPostgresStore(db, tenant, "fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Init(ctx); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	preview, _ := directory.NewPreview([]directory.Employee{{ID: "employee", Phone: "+27820000001", Zone: "zone", Department: "department", Active: true}})
	if _, e = directory.Apply(ctx, store, preview, preview.Digest(), now); e != nil {
		t.Fatal(e)
	}
	return ctx, Service{DB: db, Config: HTTPConfig{Clock: func() time.Time { return now.Add(time.Second) }, Freshness: time.Hour, Aliases: (api.AccessStore{DB: db}).EmployeeAliases}}, tenant
}
func TestPostgresDuplicateCrashAndRevocation(t *testing.T) {
	ctx, s, tenant := fixture(t)
	receive := func(id, text string) (Turn, error) {
		return s.Receive(ctx, Inbound{Tenant: tenant, Account: "synthetic", Sender: "+27820000001", ProviderMessageID: id, Text: text})
	}
	if _, e := receive("one", "log case"); e != nil {
		t.Fatal(e)
	}
	if _, e := receive("two", "A synthetic concern needs review"); e != nil {
		t.Fatal(e)
	}
	var before []byte
	if e := s.DB.QueryRow(`SELECT payload FROM addon_conversation_turn WHERE tenant=$1`, tenant).Scan(&before); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan Turn, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); turn, e := receive("three", "confirm"); results <- turn; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for result := range results {
		if id != "" && id != result.Selected {
			t.Fatal("duplicate case")
		}
		id = result.Selected
	}
	if id == "" {
		t.Fatal("missing durable ID")
	}
	var count int
	if e := s.DB.QueryRow(`SELECT count(*) FROM addon_cases_case WHERE organization_id=$1`, tenant).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	// Simulate crash after case commit but before the conversation transaction commits.
	if _, e := s.DB.Exec(`UPDATE addon_conversation_turn SET payload=$2 WHERE tenant=$1`, tenant, before); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec(`DELETE FROM addon_conversation_inbound WHERE tenant=$1 AND message_id='three'`, tenant); e != nil {
		t.Fatal(e)
	}
	recovered, e := receive("three", "confirm")
	if e != nil || recovered.Selected != id {
		t.Fatal(recovered, e)
	}
	if _, e = receive("three", "different"); e != ErrConflict {
		t.Fatal(e)
	}
	messages, e := delivery.List(ctx, s.DB, tenant, "+27820000001")
	if e != nil {
		t.Fatal(e)
	}
	if len(messages) != 2 {
		t.Fatal("duplicate outbound", len(messages))
	}
	store, _ := directory.NewPostgresStore(s.DB, tenant, "revoker")
	if e = directory.UrgentRevoke(ctx, store, "employee"); e != nil {
		t.Fatal(e)
	}
	if _, e = receive("three", "confirm"); e == nil {
		t.Fatal("revoked duplicate response leaked")
	}
	if e = s.AuthorizeDelivery(ctx, messages[len(messages)-1]); e == nil {
		t.Fatal("revoked queued delivery")
	}
}
func TestPostgresEmployeeOperatorAliasCannotReadSubjectCase(t *testing.T) {
	ctx, s, tenant := fixture(t)
	access := api.AccessStore{DB: s.DB}
	if e := access.LinkIdentity(ctx, tenant, "operator", "employee"); e != nil {
		t.Fatal(e)
	}
	receive := func(id, text string) (Turn, error) {
		return s.Receive(ctx, Inbound{Tenant: tenant, Account: "synthetic", Sender: "+27820000001", ProviderMessageID: id, Text: text})
	}
	for _, v := range []struct{ id, text string }{{"one", "log case"}, {"two", "A synthetic concern for review"}, {"three", "confirm"}} {
		if _, e := receive(v.id, v.text); e != nil {
			t.Fatal(e)
		}
	}
	turn, e := receive("four", "status")
	if e != nil {
		t.Fatal(e)
	}
	id := turn.Selected
	svc := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	_, p, e := s.employee(ctx, Inbound{Tenant: tenant, Sender: "+27820000001"})
	if e != nil {
		t.Fatal(e)
	}
	c, e := svc.Read(ctx, p, id)
	if e != nil {
		t.Fatal(e)
	}
	triage := cases.Principal{ID: "triage", Grants: []cases.Grant{{OrganizationID: tenant, UnitID: "zone", Permission: cases.AssignTriage}}}
	if _, e = svc.LinkParticipant(ctx, triage, id, c.Version, cases.Principal{ID: "operator"}, cases.Subject, "verified synthetic same-person subject", time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e = receive("four", "status"); e != ErrDenied {
		t.Fatal("historical replay bypassed current subject denial", e)
	}
	fresh, e := receive("five", "status "+id)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Selected != "" || len(fresh.Cases) != 0 {
		t.Fatal("subject references leaked", fresh)
	}
	messages, e := delivery.List(ctx, s.DB, tenant, "+27820000001")
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range messages {
		if m.CaseID == id && s.AuthorizeDelivery(ctx, m) == nil {
			t.Fatal("subject queued status delivered")
		}
	}
}
func TestPostgresActivityFactsCurrentScopeAndConflicts(t *testing.T) {
	ctx, s, tenant := fixture(t)
	receive := func(id, text string) Turn {
		v, e := s.Receive(ctx, Inbound{Tenant: tenant, Account: "synthetic", Sender: "+27820000001", ProviderMessageID: id, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	receive("one", "log case")
	receive("two", "A synthetic activity concern")
	turn := receive("three", "confirm")
	receive("four", "status")
	a := api.Actor{OrganizationID: tenant, UserID: "reporter", SessionID: "session", MFA: true, Permissions: []string{"reports:aggregate"}, Grants: []api.Grant{{Permission: "reports:aggregate", UnitID: "zone"}}}
	facts, e := ActivityFacts(ctx, s.DB, a)
	if e != nil || len(facts) != 7 {
		t.Fatal(len(facts), e)
	}
	receive("four", "status")
	again, e := ActivityFacts(ctx, s.DB, a)
	if e != nil || len(again) != len(facts) {
		t.Fatal("duplicate usage", len(again), e)
	}
	for _, f := range facts {
		if f.Zone != "zone" || f.Department != "department" || f.OccurredAt.IsZero() {
			t.Fatal(f)
		}
	}
	a.Grants = nil
	none, e := ActivityFacts(ctx, s.DB, a)
	if e != nil || len(none) != 0 {
		t.Fatal("unscoped metrics", none, e)
	}
	a.Grants = []api.Grant{{Permission: "reports:aggregate", UnitID: "zone"}}
	svc := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	_, p, _ := s.employee(ctx, Inbound{Tenant: tenant, Sender: "+27820000001"})
	c, e := svc.Read(ctx, p, turn.Selected)
	if e != nil {
		t.Fatal(e)
	}
	triage := cases.Principal{ID: "triage", Grants: []cases.Grant{{OrganizationID: tenant, UnitID: "zone", Permission: cases.AssignTriage}}}
	if _, e = svc.LinkParticipant(ctx, triage, c.ID, c.Version, cases.Principal{ID: a.UserID}, cases.Subject, "synthetic report subject", time.Now()); e != nil {
		t.Fatal(e)
	}
	filtered, e := ActivityFacts(ctx, s.DB, a)
	if e != nil || len(filtered) != 4 {
		t.Fatal("linked subject usage leaked", len(filtered), e)
	}
	for _, f := range filtered {
		if f.CaseKey != "" {
			t.Fatal("subject linked metric leaked")
		}
	}
}
func TestPostgresTimerIndependentOfChatAndConflictAware(t *testing.T) {
	ctx, s, tenant := fixture(t)
	s.Config.EscalationAfter = time.Second
	receive := func(id, text string) Turn {
		v, e := s.Receive(ctx, Inbound{Tenant: tenant, Account: "synthetic", Sender: "+27820000001", ProviderMessageID: id, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	receive("one", "log case")
	receive("two", "A synthetic timer concern")
	turn := receive("three", "confirm")
	svc := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	triage := cases.Principal{ID: "triage", Grants: []cases.Grant{{OrganizationID: tenant, UnitID: "zone", Permission: cases.ReadTriage}, {OrganizationID: tenant, UnitID: "zone", Permission: cases.AssignTriage}}}
	c, e := svc.Read(ctx, triage, turn.Selected)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.LinkParticipant(ctx, triage, c.ID, c.Version, cases.Principal{ID: "subject"}, cases.Subject, "verified subject", time.Now()); e != nil {
		t.Fatal(e)
	}
	s.Config.LoadEscalationCase = func(ctx context.Context, org, unit, id string) (cases.Case, error) { return svc.Read(ctx, triage, id) }
	calls := 0
	s.Config.ResolveCandidates = func(ctx context.Context, c cases.Case) (cases.Principal, []cases.Candidate, error) {
		calls++
		grant := []cases.Grant{{OrganizationID: tenant, UnitID: "zone", Permission: cases.WorkAssigned}}
		return triage, []cases.Candidate{{Principal: cases.Principal{ID: "operator-subject", Aliases: []string{"subject"}, Grants: grant}, Role: cases.Handler}, {Principal: cases.Principal{ID: "backup", Grants: grant}, Role: cases.Handler, Backup: true}}, nil
	}
	now := s.Config.Clock()
	s.Config.Clock = func() time.Time { return now.Add(time.Hour) }
	store, _ := directory.NewPostgresStore(s.DB, tenant, "revoker")
	if e = directory.UrgentRevoke(ctx, store, "employee"); e != nil {
		t.Fatal(e)
	}
	count, e := s.EscalateDue(ctx)
	if e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if count, e = s.EscalateDue(ctx); e != nil || count != 0 || calls != 1 {
		t.Fatal("duplicate timer", count, calls, e)
	}
	backup := cases.Principal{ID: "backup", Grants: []cases.Grant{{OrganizationID: tenant, UnitID: "zone", Permission: cases.WorkAssigned}}}
	c, e = svc.Read(ctx, backup, c.ID)
	if e != nil || len(c.Assignments) != 1 || c.Assignments[0].ID != "backup" {
		t.Fatal(c, e)
	}
}
