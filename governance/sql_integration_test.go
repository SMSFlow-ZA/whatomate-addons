//go:build integration

package governance

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"sync"
	"testing"
	"time"
)

func integrationService(t *testing.T) (Service, *sql.DB, Scope, Principal) {
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
	r := &SQLRepository{DB: db}
	if e = r.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	scope := Scope{OrganizationID: "gov-" + time.Now().Format("150405.000000000"), UnitID: "unit"}
	p := Principal{ID: "operator"}
	for _, perm := range []Permission{InventoryWrite, HoldManage, PolicyManage, AccessRequest, CorrectionRequest, ExportRequest, DeleteRequest, DeletionApprove, DeletionRecord, RestoreReconcile, JournalRead} {
		p.Grants = append(p.Grants, Grant{Scope: scope, Permission: perm})
	}
	return Service{Repository: r}, db, scope, p
}
func TestSQLApprovedDeletionHoldAndLateRestoreReconciliation(t *testing.T) {
	s, db, scope, p := integrationService(t)
	ctx := context.Background()
	r := record("root", Case)
	r.Scope = scope
	r.LineageID = r.ID
	if e := s.Register(ctx, p, r); e != nil {
		t.Fatal(e)
	}
	derived := record("vector", Vector)
	derived.Scope = scope
	if e := s.Register(ctx, p, derived); e != nil {
		t.Fatal(e)
	}
	for _, source := range []Source{Case, Vector, Backup} {
		if e := s.SetRule(ctx, p, scope, Rule{Source: source, Approved: true}); e != nil {
			t.Fatal(e)
		}
	}
	req, e := s.Request(ctx, p, SubjectRequest{ID: "delete-person", Scope: scope, SubjectID: "person", Kind: Delete, Reference: "support:incident-1"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PlanDeletion(ctx, p, scope, req.ID); !errors.Is(e, ErrApproval) {
		t.Fatal("unapproved request planned", e)
	}
	req, e = s.ApproveRequest(ctx, p, scope, req.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	hold := Hold{ID: "legal", Scope: scope, LineageID: "root", Reference: "hold:synthetic", Active: true}
	if e = s.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	plan, e := s.PlanDeletion(ctx, p, scope, req.ID)
	if e != nil || len(plan) != 2 {
		t.Fatalf("plan %+v %v", plan, e)
	}
	for _, entry := range plan {
		if entry.State != BlockedHold {
			t.Fatal("derived hold escaped", entry)
		}
	}
	if _, e = s.RecordDeletion(ctx, p, scope, plan[0].ID, plan[0].Version, "receipt:1"); !errors.Is(e, ErrHold) {
		t.Fatal("hold permitted receipt", e)
	}
	hold.Active = false
	if e = s.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	plan, e = s.PlanDeletion(ctx, p, scope, req.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range plan {
		if entry.State != Pending {
			t.Fatal(entry)
		}
		if _, e = s.RecordDeletion(ctx, p, scope, entry.ID, entry.Version, "receipt:"+entry.RecordID); e != nil {
			t.Fatal(e)
		}
	}
	restored, e := s.ReconcileRestore(ctx, p, scope, "root", "restore-key")
	if e != nil || len(restored) != 1 || restored[0].State != Pending || restored[0].Generation != 2 {
		t.Fatalf("restore %+v %v", restored, e)
	}
	again, e := s.ReconcileRestore(ctx, p, scope, "root", "restore-key")
	if e != nil || again[0].Generation != 2 {
		t.Fatal("restore replay incremented generation", again, e)
	}
	backup := record("late-backup", Backup)
	backup.Scope = scope
	if e = s.Register(ctx, p, backup); e != nil {
		t.Fatal(e)
	}
	plan, e = s.Journal(ctx, p, scope, req.ID)
	if e != nil || len(plan) != 3 {
		t.Fatal("late copy omitted", plan, e)
	}
	found := false
	for _, entry := range plan {
		if entry.RecordID == backup.ID {
			found = true
			if entry.State != AwaitingExpiry {
				t.Fatal("backup falsely erased", entry)
			}
			if _, e = s.RecordDeletion(ctx, p, scope, entry.ID, entry.Version, "receipt:backup"); !errors.Is(e, ErrExternalPending) {
				t.Fatal(e)
			}
		}
	}
	if !found {
		t.Fatal("late backup missing")
	}
	var events int
	if e = db.QueryRow("SELECT count(*) FROM addon_governance_event WHERE organization_id=$1", scope.OrganizationID).Scan(&events); e != nil || events < 10 {
		t.Fatalf("audit events %d %v", events, e)
	}
}
func TestSQLConcurrentDeletionReceiptCASAndScopeDenial(t *testing.T) {
	s, _, scope, p := integrationService(t)
	ctx := context.Background()
	r := record("root", Core)
	r.Scope = scope
	r.LineageID = r.ID
	if e := s.Register(ctx, p, r); e != nil {
		t.Fatal(e)
	}
	if e := s.SetRule(ctx, p, scope, Rule{Source: Core, Approved: true}); e != nil {
		t.Fatal(e)
	}
	req, e := s.Request(ctx, p, SubjectRequest{ID: "delete", Scope: scope, SubjectID: "person", Kind: Delete, Reference: "request:1"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApproveRequest(ctx, p, scope, req.ID, 1); e != nil {
		t.Fatal(e)
	}
	plan, e := s.PlanDeletion(ctx, p, scope, req.ID)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 12)
	for i := range 12 {
		wg.Go(func() {
			_, e := s.RecordDeletion(ctx, p, scope, plan[0].ID, 1, "receipt:"+string(rune('a'+i)))
			wins <- e == nil
		})
	}
	wg.Wait()
	close(wins)
	n := 0
	for win := range wins {
		if win {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("CAS winners %d", n)
	}
	if _, e = s.Journal(ctx, Principal{ID: "admin"}, scope, req.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("implicit admin bypass", e)
	}
	wrong := scope
	wrong.UnitID = "other"
	if _, e = s.Request(ctx, p, SubjectRequest{ID: "cross", Scope: wrong, SubjectID: "person", Kind: Export, Reference: "request:x"}); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-scope request", e)
	}
}

func TestSQLRequestKindsRequireIndependentPermissions(t *testing.T) {
	s, _, scope, p := integrationService(t)
	ctx := context.Background()
	for _, kind := range []RequestKind{Access, Correct, Export, Delete} {
		perm, e := PermissionFor(kind)
		if e != nil {
			t.Fatal(e)
		}
		actor := Principal{ID: "subject-service", Grants: []Grant{{Scope: scope, Permission: perm}}}
		req := SubjectRequest{ID: string(kind), Scope: scope, SubjectID: "person", Kind: kind, Reference: "incident:synthetic"}
		v, e := s.Request(ctx, actor, req)
		if e != nil || v.Approved || v.Version != 1 {
			t.Fatalf("%s request %+v %v", kind, v, e)
		}
		replay, e := s.Request(ctx, actor, req)
		if e != nil || replay.ID != v.ID {
			t.Fatal("request replay", replay, e)
		}
		req.ID += "-denied"
		if _, e = s.Request(ctx, Principal{ID: p.ID}, req); !errors.Is(e, ErrDenied) {
			t.Fatalf("%s missing grant bypass", kind)
		}
		if kind != Delete {
			if _, e = s.ApproveRequest(ctx, p, scope, v.ID, 1); !errors.Is(e, ErrInvalid) {
				t.Fatal("nondelete approved for deletion", e)
			}
		}
	}
}
func TestSQLRestoreUnderHoldAndLateDerivedRegistrationStayBlocked(t *testing.T) {
	s, _, scope, p := integrationService(t)
	ctx := context.Background()
	r := record("root", Core)
	r.Scope = scope
	r.LineageID = r.ID
	if e := s.Register(ctx, p, r); e != nil {
		t.Fatal(e)
	}
	if e := s.SetRule(ctx, p, scope, Rule{Source: Core, Approved: true}); e != nil {
		t.Fatal(e)
	}
	if e := s.SetRule(ctx, p, scope, Rule{Source: Vector, Approved: true}); e != nil {
		t.Fatal(e)
	}
	req, e := s.Request(ctx, p, SubjectRequest{ID: "delete", Scope: scope, SubjectID: "person", Kind: Delete, Reference: "request:2"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApproveRequest(ctx, p, scope, req.ID, 1); e != nil {
		t.Fatal(e)
	}
	if e = s.SetHold(ctx, p, Hold{ID: "hold", Scope: scope, LineageID: "root", Reference: "hold:1", Active: true}); e != nil {
		t.Fatal(e)
	}
	restored, e := s.ReconcileRestore(ctx, p, scope, "root", "restore:held")
	if e != nil || len(restored) != 1 || restored[0].State != BlockedHold {
		t.Fatal("restored hold escaped", restored, e)
	}
	derived := record("late-vector", Vector)
	derived.Scope = scope
	if e = s.Register(ctx, p, derived); e != nil {
		t.Fatal(e)
	}
	entries, e := s.Journal(ctx, p, scope, req.ID)
	if e != nil || len(entries) != 2 {
		t.Fatal(entries, e)
	}
	for _, entry := range entries {
		if entry.State != BlockedHold {
			t.Fatal("late derived hold escaped", entry)
		}
	}
	if _, e = s.ReconcileRestore(ctx, p, scope, "late-vector", "restore:held"); !errors.Is(e, ErrConflict) {
		t.Fatal("restore key changed record", e)
	}
}

type lostGovernanceConfirmation struct{ Repository }

func (r lostGovernanceConfirmation) Transact(ctx context.Context, scope Scope, fn func(Transaction) error) (CommitOutcome, error) {
	outcome, e := r.Repository.Transact(ctx, scope, fn)
	if e != nil || outcome != Committed {
		return outcome, e
	}
	return Unknown, errors.New("synthetic lost confirmation after durable commit")
}

type failGovernanceAudit struct{ Repository }

func (r failGovernanceAudit) Transact(ctx context.Context, scope Scope, fn func(Transaction) error) (CommitOutcome, error) {
	return r.Repository.Transact(ctx, scope, func(tx Transaction) error { return fn(failAuditTransaction{tx}) })
}

type failAuditTransaction struct{ Transaction }

func (t failAuditTransaction) AppendEvent(context.Context, Event) error {
	return errors.New("synthetic audit storage failure")
}
func TestSQLGovernanceUnknownCommitReplayAndAuditFailureRollback(t *testing.T) {
	s, db, scope, p := integrationService(t)
	ctx := context.Background()
	req := SubjectRequest{ID: "uncertain", Scope: scope, SubjectID: "person", Kind: Export, Reference: "incident:unknown"}
	uncertain := Service{Repository: lostGovernanceConfirmation{s.Repository}}
	v, e := uncertain.Request(ctx, p, req)
	if !errors.Is(e, ErrCommitUnknown) || v.ID != "" {
		t.Fatal("false committed request", v, e)
	}
	v, e = s.Request(ctx, p, req)
	if e != nil || v.ID != req.ID || v.Version != 1 {
		t.Fatal("durable request replay", v, e)
	}
	failed := Service{Repository: failGovernanceAudit{s.Repository}}
	req.ID = "rolled-back"
	v, e = failed.Request(ctx, p, req)
	if e == nil || v.ID != "" {
		t.Fatal("failed audit returned request", v, e)
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM addon_governance_document WHERE organization_id=$1 AND unit_id=$2 AND document_key='rolled-back'", scope.OrganizationID, scope.UnitID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("request survived failed audit %d %v", n, e)
	}
}
