//go:build integration

package governance_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPhysicalDerivativesAndCaseCorrectionUseRealOwnedStores(t *testing.T) {
	db, a, scope, svc := physicalFixture(t)
	ctx := context.Background()
	for _, p := range []string{"cases:assign.triage", "reports:operational", "conversation:privacy", "directory:privacy", "directory:read"} {
		a.Permissions = append(a.Permissions, p)
		a.Grants = append(a.Grants, api.Grant{Permission: p, UnitID: "unit"})
	}
	for _, p := range []string{"knowledge:read", "ai:assist"} {
		a.Permissions = append(a.Permissions, p)
		a.Grants = append(a.Grants, api.Grant{Permission: p, Audience: "staff"})
	}
	for _, f := range []func(context.Context) error{func(c context.Context) error { return (ai.SQLStore{DB: db}).Migrate(c) }, func(c context.Context) error { return conversation.Migrate(c, db) }, func(c context.Context) error { return (reports.Store{DB: db}).Migrate(c) }, func(c context.Context) error { return directory.MigrateHTTP(c, db) }} {
		if e := f(ctx); e != nil {
			t.Fatal(e)
		}
	}
	cs := cases.Service{Repository: &cases.SQLRepository{DB: db}}
	result, e := cs.Intake(ctx, cases.IntakeRequest{OrganizationID: a.OrganizationID, UnitID: "unit", ContactID: "linked-operator", Key: "message", Narrative: "SYNTHETIC ORIGINAL"})
	if e != nil {
		t.Fatal(e)
	}
	snap := directory.Snapshot{Version: 1, ImportedAt: time.Now().UTC(), Employees: map[string]directory.Employee{"employee": {ID: "employee", Phone: "+12025550101", Zone: "unit", Department: "demo"}}, Revoked: map[string]bool{}}
	snapRaw, _ := json.Marshal(snap)
	if _, e = db.Exec(`INSERT INTO directory_snapshots VALUES($1,1,$2)`, a.OrganizationID, snapRaw); e != nil {
		t.Fatal(e)
	}
	turn := conversation.Turn{EmployeeID: "employee", Cases: []string{result.CaseID}, Narrative: "SYNTHETIC DRAFT", Reply: "SYNTHETIC REPLY", IntakeSnapshot: cases.Classification{Zone: "unit"}}
	turnRaw, _ := json.Marshal(turn)
	for _, q := range []string{`INSERT INTO addon_conversation_turn VALUES($1,'account','+12025550101',$2)`, `INSERT INTO addon_conversation_inbound VALUES($1,'account','provider-message','fp',$2)`} {
		if _, e = db.Exec(q, a.OrganizationID, turnRaw); e != nil {
			t.Fatal(e)
		}
	}
	messageRaw, _ := json.Marshal(map[string]string{"employee_id": "employee", "case_id": result.CaseID, "text": "SYNTHETIC DELIVERY", "sender": "+12025550101"})
	if _, e = db.Exec(`INSERT INTO addon_conversation_delivery(id,tenant,sender,payload)VALUES($1,$2,'+12025550101',$3)`, a.OrganizationID+"delivery", a.OrganizationID, messageRaw); e != nil {
		t.Fatal(e)
	}
	aiRaw, _ := json.Marshal(ai.Record{ID: "answer", CaseID: result.CaseID, Audience: "staff", ActorID: a.UserID, Question: "SYNTHETIC PROMPT", Version: 1, UpdatedAt: time.Now()})
	if _, e = db.Exec(`INSERT INTO addon_ai_records VALUES($1,'answer',$2,'staff',1,$3)`, a.OrganizationID, result.CaseID, aiRaw); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO addon_ai_audit VALUES($1,'answer',1,'reviewer','suggested',now(),$2)`, a.OrganizationID, aiRaw); e != nil {
		t.Fatal(e)
	}
	facts, e := cases.ReportFacts(ctx, db, a)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = (reports.Store{DB: db}).Project(ctx, a, "operational", facts, time.Now().Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}, reports.PrivacyAdapter{}, ai.PrivacyAdapter{}, directory.PrivacyAdapter{}, conversation.PrivacyAdapter{}}, SubjectAliases: func(context.Context, string, string) ([]string, error) { return []string{"linked-operator"}, nil }}
	records, e := physical.Discover(ctx, a, scope, "employee")
	if e != nil || len(records) != 5 {
		t.Fatalf("real inventory %+v %v", records, e)
	}
	p := governance.PrincipalForActor(a)
	correction, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "correct", Scope: scope, SubjectID: "employee", Kind: governance.Correct, Reference: "synthetic:authority"})
	if e != nil {
		t.Fatal(e)
	}
	patch := json.RawMessage(`{"expected_version":1,"narrative":"SYNTHETIC CORRECTED","reason":"Verified synthetic correction"}`)
	if e = physical.Correct(ctx, a, scope, correction.ID, "cases:"+result.CaseID, patch); e != nil {
		t.Fatal(e)
	}
	c, e := cs.Read(ctx, cases.PrincipalForActor(a), result.CaseID)
	if e != nil || c.Narrative != "SYNTHETIC CORRECTED" || c.Version != 2 || c.Events[1].Kind != "privacy_correction" {
		t.Fatal(c, e)
	}
	savedCase, _ := json.Marshal(c)
	var savedReport []byte
	if e = db.QueryRow(`SELECT payload FROM addon_report_events WHERE organization_id=$1 LIMIT 1`, a.OrganizationID).Scan(&savedReport); e != nil {
		t.Fatal(e)
	}
	if e = physical.Correct(ctx, a, scope, correction.ID, "cases:"+result.CaseID, patch); !errors.Is(e, governance.ErrConflict) {
		t.Fatal("stale correction accepted", e)
	}
	for _, source := range []governance.Source{governance.Case, governance.Report, governance.AssistedAnswer, governance.Directory, governance.Conversation} {
		if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
			t.Fatal(e)
		}
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "delete", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:authority"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, 1); e != nil {
		t.Fatal(e)
	}
	entries, e := physical.EraseRequest(ctx, a, scope, req.ID)
	if e != nil || len(entries) != 5 {
		t.Fatal(entries, e)
	}
	for _, table := range []string{"addon_cases_case", "addon_ai_records", "addon_ai_audit", "addon_report_events"} {
		var n int
		if e = db.QueryRow("SELECT count(*) FROM "+table+" WHERE organization_id=$1", a.OrganizationID).Scan(&n); e != nil || n != 0 {
			t.Fatal("derivative survived", table, n, e)
		}
	}
	for _, table := range []string{"addon_conversation_turn", "addon_conversation_inbound", "addon_conversation_delivery"} {
		var n int
		if e = db.QueryRow("SELECT count(*) FROM "+table+" WHERE tenant=$1", a.OrganizationID).Scan(&n); e != nil || n != 0 {
			t.Fatal("conversation survived", table, n, e)
		}
	}
	var after directory.Snapshot
	var raw []byte
	if e = db.QueryRow(`SELECT payload FROM directory_snapshots WHERE tenant=$1`, a.OrganizationID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &after); e != nil {
		t.Fatal(e)
	}
	if _, exists := after.Employees["employee"]; exists || !after.Revoked["employee"] {
		t.Fatal("directory deletion/revocation incorrect", after)
	}
	// Restore actual case/report copies, preserving the newer independent
	// lifecycle journal. Reopening is forbidden until this HTTP gate succeeds.
	if _, e = db.Exec(`INSERT INTO addon_cases_case(id,organization_id,unit_id,contact_id,version,payload)VALUES($1,$2,$3,$4,$5,$6)`, c.ID, c.OrganizationID, c.UnitID, c.ContactID, c.Version, savedCase); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO addon_report_events VALUES($1,'operational',$2,1,$3)`, a.OrganizationID, c.ID, savedReport); e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]string{"unit_id": "unit", "subject_id": "employee", "request_id": req.ID, "restore_key": "offline-restore-derivatives"})
	r := httptest.NewRequest("POST", "/api/extensions/governance/restore", bytes.NewReader(body))
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	governance.NewHTTPHandler(db, governance.HTTPConfig{Enabled: true, Adapters: physical.Adapters, SubjectAliases: physical.SubjectAliases, RefreshActor: func(context.Context, api.Actor) (api.Actor, error) { return a, nil }}).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("restore gate failed", w.Code, w.Body.String())
	}
	for _, table := range []string{"addon_cases_case", "addon_report_events"} {
		var n int
		if e = db.QueryRow("SELECT count(*) FROM "+table+" WHERE organization_id=$1", a.OrganizationID).Scan(&n); e != nil || n != 0 {
			t.Fatal("projection resurrection", table, n, e)
		}
	}
}

func physicalFixture(t *testing.T) (*sql.DB, api.Actor, governance.Scope, governance.Service) {
	t.Helper()
	dsn := os.Getenv("GOVERNANCE_DATABASE_URL")
	if dsn == "" {
		t.Skip("GOVERNANCE_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if e = governance.MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	repo := cases.SQLRepository{DB: db}
	if e = repo.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = repo.MigrateOutboxDelivery(ctx); e != nil {
		t.Fatal(e)
	}
	if e = knowledge.MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: fmt.Sprintf("privacy-%d", time.Now().UnixNano()), UserID: "reviewer", SessionID: "live", MFA: true}
	for _, p := range []string{"governance:inventory.write", "governance:hold.manage", "governance:policy.manage", "governance:request.access", "governance:request.correct", "governance:request.export", "governance:request.delete", "governance:deletion.approve", "governance:deletion.record", "governance:restore.reconcile", "governance:journal.read", "cases:read.triage"} {
		a.Permissions = append(a.Permissions, p)
		a.Grants = append(a.Grants, api.Grant{Permission: p, UnitID: "unit"})
	}
	return db, a, governance.Scope{OrganizationID: a.OrganizationID, UnitID: "unit"}, governance.Service{Repository: &governance.SQLRepository{DB: db}}
}

func TestPhysicalDeletionIsHeldAtomicAndReappliedAfterRestore(t *testing.T) {
	db, a, scope, svc := physicalFixture(t)
	ctx := context.Background()
	p := governance.PrincipalForActor(a)
	cs := cases.Service{Repository: &cases.SQLRepository{DB: db}}
	result, e := cs.Intake(ctx, cases.IntakeRequest{OrganizationID: a.OrganizationID, UnitID: "unit", ContactID: "employee", Key: "synthetic-message", Narrative: "SYNTHETIC PRIVACY MARKER"})
	if e != nil {
		t.Fatal(e)
	}
	// Save actual database rows to simulate a later business-data restore while
	// retaining the independent current governance journal.
	var saved []byte
	if e = db.QueryRow(`SELECT payload FROM addon_cases_case WHERE id=$1`, result.CaseID).Scan(&saved); e != nil {
		t.Fatal(e)
	}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}}}
	records, e := physical.Discover(ctx, a, scope, "employee")
	if e != nil || len(records) != 1 {
		t.Fatal(records, e)
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "delete-person", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:verified-authority"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); !errors.Is(e, governance.ErrApproval) {
		t.Fatal("unapproved erase", e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, req.Version); e != nil {
		t.Fatal(e)
	}
	if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: governance.Case, Approved: true}); e != nil {
		t.Fatal(e)
	}
	hold := governance.Hold{ID: "hold", Scope: scope, LineageID: records[0].ID, Reference: "synthetic:hold", Active: true}
	if e = svc.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); !errors.Is(e, governance.ErrHold) {
		t.Fatal("hold bypass", e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM addon_cases_case WHERE id=$1`, result.CaseID).Scan(&count); e != nil || count != 1 {
		t.Fatal("held content changed", count, e)
	}
	denied := a
	denied.Grants = append([]api.Grant(nil), a.Grants...)
	for i := range denied.Grants {
		denied.Grants[i].UnitID = "other"
	}
	if _, e = physical.EraseRequest(ctx, denied, scope, req.ID); !errors.Is(e, governance.ErrDenied) {
		t.Fatal("scope bypass", e)
	}
	hold.Active = false
	if e = svc.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	entries, e := physical.EraseRequest(ctx, a, scope, req.ID)
	if e != nil || len(entries) != 1 || entries[0].State != governance.RecordedApplied {
		t.Fatal(entries, e)
	}
	for _, table := range []string{"addon_cases_case", "addon_cases_event", "addon_cases_intake", "addon_cases_outbox", "addon_cases_delivery"} {
		if e = db.QueryRow("SELECT count(*) FROM "+table+" WHERE organization_id=$1", a.OrganizationID).Scan(&count); e != nil || count != 0 {
			t.Fatal("physical object survived", table, count, e)
		}
	}
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); e != nil {
		t.Fatal("idempotent repeat", e)
	}
	var restored cases.Case
	if e = json.Unmarshal(saved, &restored); e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO addon_cases_case(id,organization_id,unit_id,contact_id,version,payload)VALUES($1,$2,$3,$4,$5,$6)`, restored.ID, restored.OrganizationID, restored.UnitID, restored.ContactID, restored.Version, saved)
	if e != nil {
		t.Fatal(e)
	}
	entries, e = svc.ReconcileRestore(ctx, p, scope, records[0].ID, "isolated-restore-one")
	if e != nil || entries[0].State != governance.Pending {
		t.Fatal(entries, e)
	}
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM addon_cases_case WHERE id=$1`, result.CaseID).Scan(&count); e != nil || count != 0 {
		t.Fatal("restore resurrected erased content", count, e)
	}
}

func TestPhysicalKnowledgeQuarantineBytesRemovedWithExternalReceiptPending(t *testing.T) {
	db, a, scope, svc := physicalFixture(t)
	ctx := context.Background()
	for _, p := range []string{"knowledge:read", "knowledge:retire"} {
		a.Permissions = append(a.Permissions, p)
		a.Grants = append(a.Grants, api.Grant{Permission: p, Audience: "unit"})
	}
	m := knowledge.UploadMetadata{DocumentID: "synthetic-policy", Version: "1", Filename: "demo.pdf", MediaType: "application/pdf", Audiences: []string{"unit"}, Effective: time.Now()}
	metadata, _ := json.Marshal(m)
	_, e := db.Exec(`INSERT INTO addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash,receipt)VALUES($1,$2,$3,$4,'test',$5,'test','{"image":"pinned-worker"}')`, a.OrganizationID+"job", a.OrganizationID, a.UserID, []byte("SYNTHETIC QUARANTINE BYTES"), metadata)
	if e != nil {
		t.Fatal(e)
	}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{knowledge.PrivacyAdapter{}}}
	records, e := physical.Discover(ctx, a, scope, m.DocumentID)
	if e != nil || len(records) != 2 {
		t.Fatal(records, e)
	}
	p := governance.PrincipalForActor(a)
	for _, source := range []governance.Source{governance.Knowledge, governance.WorkerFile} {
		if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
			t.Fatal(e)
		}
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "remove-policy", Scope: scope, SubjectID: m.DocumentID, Kind: governance.Delete, Reference: "synthetic:authority"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, 1); e != nil {
		t.Fatal(e)
	}
	entries, e := physical.EraseRequest(ctx, a, scope, req.ID)
	if e != nil {
		t.Fatal(e)
	}
	applied, pending := 0, 0
	for _, v := range entries {
		if v.State == governance.RecordedApplied {
			applied++
		}
		if v.State == governance.AwaitingExpiry {
			pending++
		}
	}
	if applied != 1 || pending != 1 {
		t.Fatal(entries)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM addon_knowledge_jobs WHERE tenant=$1`, a.OrganizationID).Scan(&n); e != nil || n != 0 {
		t.Fatal("quarantine survived", n, e)
	}
}

func TestPhysicalFailureRollsBackContentAndReceipt(t *testing.T) {
	db, a, scope, svc := physicalFixture(t)
	ctx := context.Background()
	p := governance.PrincipalForActor(a)
	cs := cases.Service{Repository: &cases.SQLRepository{DB: db}}
	v, e := cs.Intake(ctx, cases.IntakeRequest{OrganizationID: a.OrganizationID, UnitID: "unit", ContactID: "employee", Key: "synthetic", Narrative: "ROLLBACK MARKER"})
	if e != nil {
		t.Fatal(e)
	}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}}}
	if _, e = physical.Discover(ctx, a, scope, "employee"); e != nil {
		t.Fatal(e)
	}
	if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: governance.Case, Approved: true}); e != nil {
		t.Fatal(e)
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "delete", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:verified"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, 1); e != nil {
		t.Fatal(e)
	}
	// Test-only fault after owner mutations, before transaction commit. The trigger
	// is unique and restricted to this synthetic organization, so concurrent
	// package tests cannot be affected.
	name := fmt.Sprintf("privacy_fault_%d", time.Now().UnixNano())
	statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.organization_id='%s' AND NEW.action='deletion.physical' THEN RAISE EXCEPTION 'synthetic receipt failure'; END IF; RETURN NEW; END$$;CREATE TRIGGER %s BEFORE INSERT ON addon_governance_event FOR EACH ROW EXECUTE FUNCTION %s()`, name, a.OrganizationID, name, name)
	if _, e = db.Exec(statement); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON addon_governance_event;DROP FUNCTION IF EXISTS %s()`, name, name))
	})
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); e == nil {
		t.Fatal("unconfirmed mutation reported success")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM addon_cases_case WHERE id=$1 AND payload->>'Narrative'='ROLLBACK MARKER'`, v.CaseID).Scan(&n); e != nil || n != 1 {
		t.Fatal("physical mutation escaped failed transaction", n, e)
	}
	journal, e := svc.Journal(ctx, p, scope, req.ID)
	if e != nil || len(journal) != 0 {
		t.Fatal("failed receipt persisted", journal, e)
	}
}

func TestPrivacyNeverUpgradesRedactedOwnAccessToOperatorAccess(t *testing.T) {
	db, a, scope, _ := physicalFixture(t)
	ctx := context.Background()
	service := cases.Service{Repository: &cases.SQLRepository{DB: db}}
	result, e := service.Intake(ctx, cases.IntakeRequest{OrganizationID: a.OrganizationID, UnitID: "unit", ContactID: a.UserID, Key: "own", Narrative: "PROTECTED OWN NARRATIVE"})
	if e != nil {
		t.Fatal(e)
	}
	grants := []api.Grant{}
	for _, g := range a.Grants {
		if g.Permission != "cases:read.triage" {
			grants = append(grants, g)
		}
	}
	a.Grants = grants
	for _, p := range []string{"cases:read.own", "cases:work.assigned"} {
		a.Permissions = append(a.Permissions, p)
		a.Grants = append(a.Grants, api.Grant{Permission: p, UnitID: "unit"})
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = cases.PrivacyCase(ctx, tx, a, result.CaseID); !errors.Is(e, governance.ErrDenied) {
		t.Fatal("own-case plus unassigned work scope exposed operator content", e)
	}
	tx.Rollback()
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}}}
	records, e := physical.Discover(ctx, a, scope, a.UserID)
	if e != nil || len(records) != 0 {
		t.Fatal("own-case discovery exposed protected record", records, e)
	}
}

func TestKnowledgeDuplicateVersionDoesNotAuthorizeOtherAudienceErasure(t *testing.T) {
	db, a, scope, svc := physicalFixture(t)
	ctx := context.Background()
	for _, p := range []string{"knowledge:read", "knowledge:retire"} {
		a.Permissions = append(a.Permissions, p)
		a.Grants = append(a.Grants, api.Grant{Permission: p, Audience: "unit"})
	}
	for i, aud := range []string{"unit", "private"} {
		metadata, _ := json.Marshal(knowledge.UploadMetadata{DocumentID: "shared", Version: "1", Audiences: []string{aud}, Filename: "synthetic.pdf", MediaType: "application/pdf", Effective: time.Now()})
		if _, e := db.Exec(`INSERT INTO addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash)VALUES($1,$2,'u',$3,'h',$4,'m')`, fmt.Sprintf("%s-%d", a.OrganizationID, i), a.OrganizationID, []byte("SYNTHETIC"), metadata); e != nil {
			t.Fatal(e)
		}
	}
	physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{knowledge.PrivacyAdapter{}}}
	if _, e := physical.Discover(ctx, a, scope, "shared"); e != nil {
		t.Fatal(e)
	}
	p := governance.PrincipalForActor(a)
	if e := svc.SetRule(ctx, p, scope, governance.Rule{Source: governance.Knowledge, Approved: true}); e != nil {
		t.Fatal(e)
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "delete", Scope: scope, SubjectID: "shared", Kind: governance.Delete, Reference: "synthetic:authority"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = physical.EraseRequest(ctx, a, scope, req.ID); !errors.Is(e, governance.ErrDenied) {
		t.Fatal("private duplicate erased", e)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM addon_knowledge_jobs WHERE tenant=$1`, a.OrganizationID).Scan(&n); e != nil || n != 2 {
		t.Fatal("cross audience partial deletion", n, e)
	}
}
