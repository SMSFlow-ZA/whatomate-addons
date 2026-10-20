package carrier

import (
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	"testing"
	"time"
)

func TestPhysicalGovernanceCaseBeforeCarrierOrderAndHold(t *testing.T) {
	ctx, s := carrierFixture(t)
	for _, migrate := range []func() error{func() error { return governance.MigrateHTTP(ctx, s.DB) }, func() error { return knowledge.MigrateHTTP(ctx, s.DB) }} {
		if e := migrate(); e != nil {
			t.Fatal(e)
		}
	}
	s.SMSRoutes = []SMSRoute{{Tenant: "org", Account: "sms", WhatsAppAccount: "account"}}
	repo := &cases.SQLRepository{DB: s.DB}
	result, e := (cases.Service{Repository: repo}).Intake(ctx, cases.IntakeRequest{OrganizationID: "org", UnitID: "zone", ContactID: "employee", Key: "physical-api-case", Narrative: "A synthetic case submitted through an API.", Snapshot: cases.Classification{Zone: "zone", Department: "department", CaseType: "employee_request"}})
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
	actor := api.Actor{OrganizationID: "org", UserID: "privacy-reviewer", SessionID: "current", MFA: true}
	for _, permission := range []string{"conversation:privacy", string(cases.ReadTriage), "governance:inventory.write", "governance:request.delete", "governance:deletion.approve", "governance:deletion.record", "governance:policy.manage", "governance:journal.read", "governance:request.export", string(governance.HoldManage)} {
		actor.Permissions = append(actor.Permissions, permission)
		actor.Grants = append(actor.Grants, api.Grant{Permission: permission, UnitID: "zone"})
	}
	scope := governance.Scope{OrganizationID: "org", UnitID: "zone"}
	physical := governance.PhysicalService{DB: s.DB, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}, knowledge.PrivacyAdapter{}, directory.PrivacyAdapter{}, conversation.PrivacyAdapter{}, reports.PrivacyAdapter{}, ai.PrivacyAdapter{}, PrivacyAdapter{}}}
	records, e := physical.Discover(ctx, actor, scope, "employee")
	if e != nil || len(records) != 3 {
		t.Fatal(records, e)
	}
	caseRecordID := "cases:" + result.CaseID
	expected := map[string]struct {
		source  governance.Source
		lineage string
	}{caseRecordID: {governance.Case, caseRecordID}, caseRecordID + ":transport": {governance.Provider, caseRecordID}, "carrier:employee": {governance.Conversation, "carrier:employee"}}
	for _, record := range records {
		want, ok := expected[record.ID]
		if !ok || record.Source != want.source || record.LineageID != want.lineage || record.Scope != scope {
			t.Fatal("unexpected governed lineage", record)
		}
	}
	service := governance.Service{Repository: &governance.SQLRepository{DB: s.DB}}
	p := governance.PrincipalForActor(actor)
	for _, source := range []governance.Source{governance.Case, governance.Conversation, governance.Provider} {
		if e = service.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
			t.Fatal(e)
		}
	}
	request, e := service.Request(ctx, p, governance.SubjectRequest{ID: "delete-api-case", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:verified-authority"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.ApproveRequest(ctx, p, scope, request.ID, request.Version); e != nil {
		t.Fatal(e)
	}
	hold := governance.Hold{ID: "synthetic-hold", Scope: scope, LineageID: "carrier:employee", Reference: "hold:synthetic", Active: true}
	if e = service.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	if _, e = physical.EraseRequest(ctx, actor, scope, request.ID); !errors.Is(e, governance.ErrHold) {
		t.Fatal("hold did not block physical erasure", e)
	}
	for _, table := range []string{"addon_cases_case", "addon_cases_delivery", "addon_conversation_delivery", "addon_carrier_route"} {
		var n int
		if e = s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 1 {
			t.Fatal("hold failed to preserve payload", table, n, e)
		}
	}
	hold.Active = false
	if e = service.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	entries, e := physical.EraseRequest(ctx, actor, scope, request.ID)
	if e != nil || len(entries) != 3 {
		t.Fatal("case-before-carrier order failed atomic erasure", entries, e)
	}
	for _, entry := range entries {
		want, ok := expected[entry.RecordID]
		if !ok || entry.Source != want.source {
			t.Fatal("unexpected physical deletion entry", entry)
		}
		if entry.Source == governance.Provider {
			if entry.State != governance.AwaitingExpiry || entry.ConfirmationReference != "" {
				t.Fatal("failed acknowledgement carrier retention falsely confirmed", entry)
			}
		} else if entry.State != governance.RecordedApplied || entry.ConfirmationReference == "" {
			t.Fatal("local payload erasure was not recorded", entry)
		}
	}
	journal, e := service.Journal(ctx, p, scope, request.ID)
	if e != nil || len(journal) != 3 {
		t.Fatal("durable erasure lineage missing", journal, e)
	}
	for _, entry := range journal {
		if entry.Source == governance.Provider && (entry.State != governance.AwaitingExpiry || entry.ConfirmationReference != "") {
			t.Fatal("journal falsely confirmed external expiry", entry)
		}
	}
	for _, table := range []string{"addon_cases_case", "addon_conversation_delivery", "addon_carrier_route"} {
		var n int
		if e = s.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("local payload escaped erasure", table, n, e)
		}
	}
}
