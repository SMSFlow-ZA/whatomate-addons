//go:build integration

package governance_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"testing"
)

type omittedCaseReview struct{ cases.PrivacyAdapter }

func (omittedCaseReview) ReviewDiscoverTx(context.Context, *sql.Tx, api.Actor, governance.Scope, string) ([]governance.Record, error) {
	return nil, nil
}

func TestReviewMissingSourceRequiresSameRequestAppliedGeneration(t *testing.T) {
	db, a, scope, svc := physicalFixture(t)
	ctx := t.Context()
	p := governance.PrincipalForActor(a)
	_, e := (cases.Service{Repository: &cases.SQLRepository{DB: db}}).Intake(ctx, cases.IntakeRequest{OrganizationID: a.OrganizationID, UnitID: scope.UnitID, ContactID: "employee", Key: "actual-case", Narrative: "Synthetic case"})
	if e != nil {
		t.Fatal(e)
	}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}}}
	records, e := physical.Discover(ctx, a, scope, "employee")
	if e != nil || len(records) != 1 {
		t.Fatal(e)
	}
	var original []byte
	if e = db.QueryRow(`SELECT payload FROM addon_cases_case WHERE organization_id=$1 AND id=$2`, a.OrganizationID, records[0].Reference).Scan(&original); e != nil {
		t.Fatal(e)
	}
	if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: governance.Case, Approved: true}); e != nil {
		t.Fatal(e)
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "exact", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, req.Version); e != nil {
		t.Fatal(e)
	}
	reviewer := a
	reviewer.UserID = "independent"
	reviewer.Grants = []api.Grant{{Permission: string(governance.DeletionApprove), UnitID: scope.UnitID}, {Permission: string(governance.JournalRead), UnitID: scope.UnitID}}
	tx, _ := db.BeginTx(ctx, nil)
	if _, e = physical.ReviewInventoryTx(ctx, tx, reviewer, scope, "employee", req.ID); !errors.Is(e, governance.ErrConflict) {
		t.Fatal("missing live inaccessible source accepted", e)
	}
	tx.Rollback()
	requester := a
	requester.Grants = nil
	for _, g := range a.Grants {
		if g.Permission != string(cases.ReadTriage) {
			requester.Grants = append(requester.Grants, g)
		}
	}
	tx, _ = db.BeginTx(ctx, nil)
	if _, e = physical.DiscoverRequestInventoryTx(ctx, tx, requester, scope, "employee", req.ID); !errors.Is(e, governance.ErrConflict) {
		t.Fatal("requester lost-source omission accepted", e)
	}
	tx.Rollback()
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); e != nil {
		t.Fatal(e)
	}
	tx, _ = db.BeginTx(ctx, nil)
	if _, e = physical.ReviewInventoryTx(ctx, tx, reviewer, scope, "employee", req.ID); !errors.Is(e, governance.ErrDenied) {
		t.Fatal("lost source scope allowed", e)
	}
	tx.Rollback()
	reviewer.Grants = append(reviewer.Grants, api.Grant{Permission: string(cases.ReadTriage), UnitID: scope.UnitID})
	tx, _ = db.BeginTx(ctx, nil)
	got, e := physical.ReviewInventoryTx(ctx, tx, reviewer, scope, "employee", req.ID)
	if e != nil || len(got) != 1 {
		t.Fatal("completed minimal history unavailable", e)
	}
	tx.Rollback()
	tx, _ = db.BeginTx(ctx, nil)
	if _, e = tx.Exec(`INSERT INTO addon_cases_case(organization_id,id,unit_id,contact_id,version,payload)VALUES($1,$2,$3,'employee',1,$4)`, a.OrganizationID, records[0].Reference, scope.UnitID, original); e != nil {
		t.Fatal(e)
	}
	omitted := physical
	omitted.Adapters = []governance.PhysicalAdapter{omittedCaseReview{}}
	if _, e = omitted.ReviewInventoryTx(ctx, tx, reviewer, scope, "employee", req.ID); !errors.Is(e, governance.ErrConflict) {
		t.Fatal("restored source falsely absent", e)
	}
	tx.Rollback()
	tx, _ = db.BeginTx(ctx, nil)
	if _, e = tx.ExecContext(ctx, `UPDATE addon_governance_document SET payload=jsonb_set(payload,'{Generation}','99') WHERE organization_id=$1 AND unit_id=$2 AND kind='deletion_journal'`, scope.OrganizationID, scope.UnitID); e != nil {
		t.Fatal(e)
	}
	if _, e = physical.ReviewInventoryTx(ctx, tx, reviewer, scope, "employee", req.ID); !errors.Is(e, governance.ErrConflict) {
		t.Fatal("stale generation accepted", e)
	}
	tx.Rollback()
	other, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "other", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, other.ID, other.Version); e != nil {
		t.Fatal(e)
	}
	tx, _ = db.BeginTx(ctx, nil)
	defer tx.Rollback()
	if _, e = physical.ReviewInventoryTx(ctx, tx, reviewer, scope, "employee", other.ID); !errors.Is(e, governance.ErrConflict) {
		t.Fatal("other request reused completion", e)
	}
}
