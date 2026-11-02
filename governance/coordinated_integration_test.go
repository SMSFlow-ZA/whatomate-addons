//go:build integration

package governance_test

import (
	"context"
	"testing"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

func TestCoordinatedEraseChecksEveryScopeBeforeMutation(t *testing.T) {
	db, a, first, svc := physicalFixture(t)
	ctx := context.Background()
	second := governance.Scope{OrganizationID: first.OrganizationID, UnitID: "second"}
	for _, g := range append([]api.Grant(nil), a.Grants...) {
		g.UnitID = second.UnitID
		a.Grants = append(a.Grants, g)
	}
	cs := cases.Service{Repository: &cases.SQLRepository{DB: db}}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}}}
	requests := []governance.ScopedDeletion{}
	ids := []string{}
	for _, scope := range []governance.Scope{first, second} {
		c, e := cs.Intake(ctx, cases.IntakeRequest{OrganizationID: scope.OrganizationID, UnitID: scope.UnitID, ContactID: "employee", Key: scope.UnitID, Narrative: "SYNTHETIC COORDINATED"})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, c.CaseID)
		if _, e = physical.Discover(ctx, a, scope, "employee"); e != nil {
			t.Fatal(e)
		}
		if e = svc.SetRule(ctx, governance.PrincipalForActor(a), scope, governance.Rule{Source: governance.Case, Approved: true}); e != nil {
			t.Fatal(e)
		}
		req, e := svc.Request(ctx, governance.PrincipalForActor(a), governance.SubjectRequest{ID: "coordinated", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:coordinated"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.ApproveRequest(ctx, governance.PrincipalForActor(a), scope, req.ID, req.Version); e != nil {
			t.Fatal(e)
		}
		requests = append(requests, governance.ScopedDeletion{Scope: scope, RequestID: req.ID})
	}
	if e := svc.SetHold(ctx, governance.PrincipalForActor(a), governance.Hold{ID: "hold", Scope: second, LineageID: "cases:" + ids[1], Active: true, Reference: "synthetic:hold"}); e != nil {
		t.Fatal(e)
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = physical.EraseRequestsTx(ctx, tx, a, requests); e != governance.ErrHold {
		tx.Rollback()
		t.Fatalf("other unit hold: %v", e)
	}
	// Even before rollback, preflight must leave the first unit untouched.
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM addon_cases_case WHERE organization_id=$1 AND payload->>'Narrative'='SYNTHETIC COORDINATED'`, a.OrganizationID).Scan(&count); e != nil || count != 2 {
		tx.Rollback()
		t.Fatalf("preflight changed source: %d %v", count, e)
	}
	tx.Rollback()
	if e = svc.SetHold(ctx, governance.PrincipalForActor(a), governance.Hold{ID: "hold", Scope: second, LineageID: "cases:" + ids[1], Active: false, Reference: "synthetic:release"}); e != nil {
		t.Fatal(e)
	}
	tx, e = db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := physical.EraseRequestsTx(ctx, tx, a, requests)
	if e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if len(entries) != 2 {
		tx.Rollback()
		t.Fatalf("journals: %+v", entries)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	for _, scope := range []governance.Scope{first, second} {
		journal, e := svc.Journal(ctx, governance.PrincipalForActor(a), scope, "coordinated")
		if e != nil || len(journal) != 1 || journal[0].State != governance.RecordedApplied {
			t.Fatalf("unit journal %+v %v", journal, e)
		}
	}
}
