//go:build integration

// These are domain/persistence rehearsals with synthetic data, not a live
// WhatsApp, deployed model, browser or compliance acceptance demonstration.
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

// excerptFixture substitutes only inference. SQL publication, permissions,
// retrieval, eligibility, case transactions and state changes are real.
type excerptFixture struct{}

func (excerptFixture) Generate(_ context.Context, r ai.Request) (ai.Completion, error) {
	if len(r.Sources) == 0 {
		return ai.Completion{}, fmt.Errorf("no source")
	}
	return ai.Completion{Answer: r.Sources[0].Page.Text, Citations: []string{r.Sources[0].Reference}, Grounded: true}, nil
}

func TestSyntheticRosterToApprovedAnswerAndProtectedCase(t *testing.T) {
	dsn := os.Getenv("WORKFLOW_DATABASE_URL")
	if dsn == "" {
		t.Skip("WORKFLOW_DATABASE_URL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	org := "workflow-" + time.Now().Format("150405.000000000")
	now := time.Now().UTC()
	store, err := directory.NewPostgresStore(db, org, "roster-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	preview, err := directory.NewPreview([]directory.Employee{{ID: "synthetic-employee", Phone: "+27123456789", Active: true, Zone: "Alpha", Department: "Operations"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := directory.Apply(ctx, store, preview, preview.Digest(), now)
	if err != nil {
		t.Fatal(err)
	}
	employee, err := directory.Match(snapshot, "+27123456789", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = directory.Match(snapshot, "+27999999999", now, time.Hour); err == nil {
		t.Fatal("unknown employee entered workflow")
	}

	repo, err := knowledge.NewPostgresRepository(db, org)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range [][3]string{{"reviewer", "review", "*"}, {"publisher", "publish", "*"}, {employee.ID, "read", "staff"}} {
		if _, err = db.ExecContext(ctx, `INSERT INTO addon_knowledge_grants(tenant,principal,action,scope) VALUES($1,$2,$3,$4)`, org, g[0], g[1], g[2]); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("SYNTHETIC TEST SOURCE ONLY: Ask the human reviewer about lateness.")
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	document := knowledge.Document{ID: "synthetic-sop", Version: "1", ContentHash: hash, Content: content, Audiences: []string{"staff"}, Effective: now.Add(-time.Hour), Pages: []knowledge.Page{{Number: 1, Text: string(content), Method: "text", ExtractorRevision: "test-fixture-1"}}}
	approval, err := repo.Approve(ctx, "reviewer", document)
	if err != nil {
		t.Fatal(err)
	}
	// Trusted fixture evidence substitutes scanning only; it is never real-policy evidence.
	if err = repo.Publish(ctx, "publisher", document, knowledge.Evidence{ContentHash: hash, ScannerRevision: "synthetic-fixture", MalwareClean: true, ExtractionComplete: true}, approval); err != nil {
		t.Fatal(err)
	}
	assistant := ai.Service{Catalog: repo, Authorizer: knowledge.AuthorizeFunc(func(_ context.Context, p, a string) bool { return p == employee.ID && a == "staff" }), Provider: excerptFixture{}, Now: func() time.Time { return now }}
	answer := assistant.Answer(ctx, employee.ID, "staff", "lateness")
	if answer.Handoff || len(answer.Citations) != 1 || answer.Citations[0].Page.Number != 1 {
		t.Fatalf("grounded fixture failed: %+v", answer)
	}
	if !assistant.Answer(ctx, "unknown", "staff", "lateness").Handoff {
		t.Fatal("unauthorized source returned")
	}

	caseRepo := &cases.SQLRepository{DB: db}
	if err = caseRepo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svc := cases.Service{Repository: caseRepo}
	request := cases.IntakeRequest{OrganizationID: org, UnitID: employee.Department, ContactID: employee.ID, Key: "synthetic-provider-message-1", Participants: []cases.Participant{{ID: "subject", Role: cases.Subject}}}
	intake, err := svc.Intake(ctx, request)
	if err != nil || intake.CaseID == "" {
		t.Fatalf("intake failed: %+v %v", intake, err)
	}
	replayed, err := svc.Intake(ctx, request)
	if err != nil || intake != replayed {
		t.Fatal("duplicate intake")
	}
	principal := func(id string, p cases.Permission) cases.Principal {
		return cases.Principal{ID: id, Grants: []cases.Grant{{OrganizationID: org, UnitID: employee.Department, Permission: p}}}
	}
	triage := principal("triage", cases.AssignTriage)
	handler := principal("handler", cases.WorkAssigned)
	assigned, err := svc.Assign(ctx, triage, intake.CaseID, 1, []cases.Candidate{{Principal: handler, Role: cases.Handler}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Read(ctx, principal("subject", cases.ReadTriage), intake.CaseID); err == nil {
		t.Fatal("subject granted case access")
	}
	if _, err = svc.Read(ctx, principal("other", cases.WorkAssigned), intake.CaseID); err == nil {
		t.Fatal("unassigned handler granted access")
	}
	progressed, err := svc.ChangeState(ctx, handler, assigned.ID, assigned.Version, cases.InProgress, "", now)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.ChangeState(ctx, handler, progressed.ID, progressed.Version, cases.Resolved, "reviewed by human", now)
	if err != nil || resolved.State != cases.Resolved {
		t.Fatalf("resolution failed %v", err)
	}
	if _, err = svc.ChangeState(ctx, handler, resolved.ID, resolved.Version, cases.InProgress, "", now); err == nil {
		t.Fatal("reopen without reason")
	}

	revoked, err := directory.Revoke(snapshot, employee.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompareAndSwap(ctx, snapshot.Version, revoked); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = directory.Match(current, employee.Phone, now, time.Hour); err == nil {
		t.Fatal("urgent revocation did not stop subsequent eligibility")
	}
	var acknowledgements int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM addon_cases_outbox WHERE organization_id=$1`, org).Scan(&acknowledgements); err != nil || acknowledgements != 1 {
		t.Fatalf("ack intents: %d %v", acknowledgements, err)
	}
}
