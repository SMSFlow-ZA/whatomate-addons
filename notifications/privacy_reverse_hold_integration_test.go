//go:build integration

package notifications

import (
	"context"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"testing"
)

func TestRecipientNoticeHoldAlsoProtectsReporterErasure(t *testing.T) {
	s, c, _ := setup(t)
	ctx := context.Background()
	if e := governance.MigrateHTTP(ctx, s.DB); e != nil {
		t.Fatal(e)
	}
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	v := escalate(t, s, c, "reverse-hold")
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	m := messages(t, s)[0]
	a := api.Actor{OrganizationID: s.Tenant, UserID: "handler", SessionID: "synthetic", MFA: true}
	for _, permission := range []string{"conversation:privacy", string(cases.WorkAssigned), "governance:inventory.write", "governance:hold.manage", "governance:policy.manage", "governance:request.delete", "governance:deletion.approve", "governance:deletion.record"} {
		a.Permissions = append(a.Permissions, permission)
		a.Grants = append(a.Grants, api.Grant{Permission: permission, UnitID: "unit"})
	}
	physical := governance.PhysicalService{DB: s.DB, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}, PrivacyAdapter{}}, SubjectAliases: func(_ context.Context, _ string, subject string) ([]string, error) {
		if subject == "employee" {
			return []string{"handler"}, nil
		}
		return nil, nil
	}}
	scope := governance.Scope{OrganizationID: s.Tenant, UnitID: "unit"}
	svc := governance.Service{Repository: &governance.SQLRepository{DB: s.DB}}
	p := governance.PrincipalForActor(a)
	if _, e := physical.Discover(ctx, a, scope, "reporter"); e != nil {
		t.Fatal(e)
	}
	records, e := physical.Discover(ctx, a, scope, "employee")
	if e != nil {
		t.Fatal(e)
	}
	var recipient governance.Record
	for _, r := range records {
		if r.Reference == m.ID && r.Source == governance.Conversation {
			recipient = r
		}
	}
	if recipient.ID == "" {
		t.Fatal("recipient inventory absent")
	}
	if e = svc.SetHold(ctx, p, governance.Hold{ID: "recipient-hold", Scope: scope, LineageID: recipient.LineageID, Reference: "synthetic:recipient-hold", Active: true}); !errors.Is(e, governance.ErrInvalid) {
		t.Fatalf("noncanonical shared-object hold accepted: %v", e)
	}
	var holds int
	if e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_governance_document WHERE organization_id=$1 AND unit_id='unit' AND kind='hold'`, s.Tenant).Scan(&holds); e != nil || holds != 0 {
		t.Fatalf("rejected recipient hold mutated storage: %d %v", holds, e)
	}
	if e = svc.SetHold(ctx, p, governance.Hold{ID: "case-hold", Scope: scope, LineageID: "cases:" + v.ID, Reference: "synthetic:explicit-case-hold", Active: true}); e != nil {
		t.Fatal(e)
	}
	for _, source := range []governance.Source{governance.Case, governance.Conversation} {
		if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
			t.Fatal(e)
		}
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "reporter-delete", Scope: scope, SubjectID: "reporter", Kind: governance.Delete, Reference: "synthetic:request"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, req.Version); e != nil {
		t.Fatal(e)
	}
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); !errors.Is(e, governance.ErrHold) {
		t.Fatalf("reporter erasure bypassed recipient hold: %v", e)
	}
	if len(messages(t, s)) != 1 {
		t.Fatal("recipient-held physical notice erased")
	}
	var n int
	if e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_cases_case WHERE organization_id=$1 AND id=$2`, s.Tenant, v.ID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("case unexpectedly erased: %d %v", n, e)
	}
}
