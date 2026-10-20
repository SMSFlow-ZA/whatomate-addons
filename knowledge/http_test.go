package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"strings"
	"testing"
	"time"
)

// Claim deliberately scans the entire worker queue, across tenants. Each HTTP/
// worker test therefore needs its own PostgreSQL schema, not only a tenant ID:
// other packages exercise quarantined or intentionally invalid privacy fixtures.
// Keep production hash, lease and audience checks intact instead of consuming or
// deleting unrelated jobs to make a test pass.
func knowledgeHTTPFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("KNOWLEDGE_DATABASE_URL")
	if dsn == "" {
		t.Skip("KNOWLEDGE_DATABASE_URL not configured")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	schema := "knowledge_http_" + randomID()[:20]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.ExecContext(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop isolated knowledge HTTP schema: %v", err)
		}
	})
	isolated := cfg.Copy()
	isolated.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*isolated)
	t.Cleanup(func() { db.Close() })
	return db, t.Name()
}
func TestProcessingQueueLeaseAndHashBoundary(t *testing.T) {
	db, tenant := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if err := MigrateHTTP(ctx, db); err != nil {
		t.Fatal(err)
	}
	q := NewProcessingQueue(db)
	metadata := UploadMetadata{DocumentID: "synthetic", Version: "1", Filename: "policy.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now().Add(-time.Hour)}
	id, err := q.enqueue(ctx, tenant, "u", []byte("synthetic PDF"), metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DELETE FROM addon_knowledge_jobs WHERE tenant=$1", tenant)
	job, err := q.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != id {
		t.Fatal("claimed wrong job")
	}
	pages := []Page{{Number: 1, Text: "synthetic policy", Method: "text", ExtractorRevision: "test-1"}}
	evidence := Evidence{ContentHash: job.InputHash, ScannerRevision: "test-1", MalwareClean: true, ExtractionComplete: true}
	forged := job
	forged.Metadata.Audiences = []string{"other"}
	if err = q.Complete(ctx, forged, pages, evidence); !errors.Is(err, ErrDenied) {
		t.Fatalf("metadata forgery accepted: %v", err)
	}
	forged = job
	forged.Input = []byte("changed")
	if err = q.Complete(ctx, forged, pages, evidence); !errors.Is(err, ErrDenied) {
		t.Fatalf("input forgery accepted: %v", err)
	}
	failed := evidence
	failed.MalwareClean = false
	if err = q.Complete(ctx, job, pages, failed); !errors.Is(err, ErrDenied) {
		t.Fatalf("failed scan accepted: %v", err)
	}
	if err = q.Complete(ctx, job, pages, evidence); err != nil {
		t.Fatal(err)
	}
	if err = q.Complete(ctx, job, pages, evidence); !errors.Is(err, ErrDenied) {
		t.Fatalf("replay accepted: %v", err)
	}
}

func TestHTTPLifecycleCurrentScopesAndRetirement(t *testing.T) {
	db, tenant := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if err := MigrateHTTP(ctx, db); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DELETE FROM addon_knowledge_jobs WHERE tenant=$1", tenant)
	actors := map[string]api.Actor{}
	for user, action := range map[string]string{"u": "upload", "r": "review", "p": "publish", "x": "retire", "reader": "read"} {
		permission := "knowledge:" + action
		actors[user] = api.Actor{OrganizationID: tenant, UserID: user, SessionID: "session", MFA: true, Permissions: []string{permission}, Grants: []api.Grant{{Permission: permission, Audience: "staff"}}}
	}
	revoked := false
	readerRevoked := false
	h := NewHTTPHandler(db, HTTPConfig{Enabled: true, Authorize: func(_ context.Context, org, user, p, aud string) bool {
		if readerRevoked && user == "reader" {
			return false
		}
		if revoked && user == "r" {
			return false
		}
		a := actors[user]
		return org == tenant && a.AudienceAllowed(p, aud)
	}})
	m := UploadMetadata{DocumentID: "synthetic", Version: "1", Filename: "policy.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now().Add(-time.Hour)}
	w := uploadRequest(h, actors["u"], m)
	if w.Code != 202 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	id := response.Data.ID
	q := NewProcessingQueue(db)
	j, err := q.Claim(ctx, "test-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != id {
		t.Fatal("unexpected job")
	}
	if w = knowledgeRequest(h, actors["r"], "POST", "jobs/"+id+"/approve", `{"expected_version":2}`); w.Code != 409 {
		t.Fatal("approved quarantined bytes")
	}
	pages := []Page{{Number: 1, Text: "synthetic policy", Method: "text", ExtractorRevision: "test-1"}}
	if err = q.Complete(ctx, j, pages, Evidence{ContentHash: j.InputHash, ScannerRevision: "test-1", MalwareClean: true, ExtractionComplete: true}); err != nil {
		t.Fatal(err)
	}
	bad := actors["r"]
	bad.Grants = []api.Grant{{Permission: "knowledge:review", Audience: "other"}}
	if w = knowledgeRequest(h, bad, "GET", "jobs/"+id, ""); w.Code != 404 {
		t.Fatalf("different audience saw job: %d", w.Code)
	}
	if w = knowledgeRequest(h, actors["r"], "POST", "jobs/"+id+"/approve", `{"expected_version":3,"evidence":{}}`); w.Code != 400 {
		t.Fatal("client evidence accepted")
	}
	if w = knowledgeRequest(h, actors["r"], "POST", "jobs/"+id+"/approve", `{"expected_version":3}`); w.Code != 200 {
		t.Fatalf("review: %d %s", w.Code, w.Body)
	}
	revoked = true
	if w = knowledgeRequest(h, actors["p"], "POST", "jobs/"+id+"/publish", `{"expected_version":4}`); w.Code != 403 {
		t.Fatal("revoked reviewer accepted")
	}
	revoked = false
	if w = knowledgeRequest(h, actors["p"], "POST", "jobs/"+id+"/publish", `{"expected_version":4}`); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}
	if w = knowledgeRequest(h, actors["reader"], "GET", "search?audience=other&q=policy", ""); w.Code != 403 {
		t.Fatal("cross audience search")
	}
	if w = knowledgeRequest(h, actors["reader"], "GET", "search?audience=staff&q=policy", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic policy") {
		t.Fatalf("missing approved search: %d %s", w.Code, w.Body)
	}
	repo, _ := NewPostgresRepository(db, tenant)
	question := Query{Principal: "reader", Audience: "staff", Text: "Where can I find the synthetic policy?", At: time.Now()}
	allow := AuthorizeFunc(func(context.Context, string, string) bool { return true })
	if candidates, err := repo.RetrieveCandidates(ctx, question, allow); err != nil || len(candidates) != 1 {
		t.Fatalf("question candidates unavailable: %v %v", candidates, err)
	}
	readerRevoked = true
	if w = knowledgeRequest(h, actors["reader"], "GET", "search?audience=staff&q=policy", ""); w.Code != 403 {
		t.Fatal("stale read actor bypassed current role revocation")
	}
	if w = knowledgeRequest(h, actors["reader"], "GET", "download?audience=staff&document_id=synthetic&version=1", ""); w.Code != 404 {
		t.Fatal("stale read actor downloaded after current role revocation")
	}
	readerRevoked = false
	if w = knowledgeRequest(h, actors["reader"], "GET", "download?audience=staff&document_id=synthetic&version=1", ""); w.Code != 200 || w.Body.String() != "synthetic PDF" {
		t.Fatal("download failed")
	}
	if w = knowledgeRequest(h, actors["x"], "POST", "jobs/"+id+"/retire", `{"expected_version":5}`); w.Code != 200 {
		t.Fatalf("retire: %d %s", w.Code, w.Body)
	}
	if w = knowledgeRequest(h, actors["reader"], "GET", "download?audience=staff&document_id=synthetic&version=1", ""); w.Code != 404 {
		t.Fatal("retired download")
	}
	if w = knowledgeRequest(h, actors["reader"], "GET", "search?audience=staff&q=policy", ""); strings.Contains(w.Body.String(), "synthetic policy") {
		t.Fatal("retired search")
	}
	if candidates, err := repo.RetrieveCandidates(ctx, question, allow); err != nil || len(candidates) != 0 {
		t.Fatalf("retired candidates exposed: %v %v", candidates, err)
	}
}

func TestProcessingReceiptsAndUnknownLease(t *testing.T) {
	db, tenant := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if e := MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	defer db.Exec("DELETE FROM addon_knowledge_jobs WHERE tenant=$1", tenant)
	q := NewProcessingQueue(db)
	m := UploadMetadata{DocumentID: "receipt", Version: "1", Filename: "policy.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now()}
	_, e := q.enqueue(ctx, tenant, "u", []byte("synthetic"), m)
	if e != nil {
		t.Fatal(e)
	}
	j, e := q.Claim(ctx, "test-worker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	pages := []Page{{Number: 1, Text: "policy", Method: "text", ExtractorRevision: "revision"}}
	ev := Evidence{ContentHash: j.InputHash, ScannerRevision: "definitions", MalwareClean: true, ExtractionComplete: true}
	receipt := ProcessingReceipt{JobID: j.ID, InputHash: j.InputHash, MetadataHash: j.MetadataHash, OutputHash: strings.Repeat("a", 64), ProfileID: "pinned-profile", ImageDigest: "local/worker@sha256:" + strings.Repeat("b", 64), DefinitionsRevision: "definitions"}
	forged := receipt
	forged.JobID = "another-job"
	if e = q.CompleteWithReceipt(ctx, j, pages, ev, forged); !errors.Is(e, ErrDenied) {
		t.Fatal("forged receipt accepted")
	}
	// A local content-addressed image ID is as immutable as a registry digest.
	receipt.ImageDigest = "sha256:" + strings.Repeat("b", 64)
	if e = q.CompleteWithReceipt(ctx, j, pages, ev, receipt); e != nil {
		t.Fatal(e)
	}
	var stored []byte
	if e = db.QueryRow("SELECT receipt FROM addon_knowledge_jobs WHERE id=$1", j.ID).Scan(&stored); e != nil || !strings.Contains(string(stored), "pinned-profile") {
		t.Fatalf("receipt not retained: %s %v", stored, e)
	}
	m.Version = "2"
	_, e = q.enqueue(ctx, tenant, "u", []byte("synthetic"), m)
	if e != nil {
		t.Fatal(e)
	}
	j, e = q.Claim(ctx, "test-worker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE addon_knowledge_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", j.ID); e != nil {
		t.Fatal(e)
	}
	if e = q.Complete(ctx, j, pages, ev); !errors.Is(e, ErrDenied) {
		t.Fatal("expired completion accepted")
	}
	if _, e = q.Claim(ctx, "test-worker", time.Minute); !errors.Is(e, sql.ErrNoRows) {
		t.Fatalf("unknown job replayed: %v", e)
	}
	var status string
	if e = db.QueryRow("SELECT status FROM addon_knowledge_jobs WHERE id=$1", j.ID).Scan(&status); e != nil || status != "unknown" {
		t.Fatalf("lease outcome lost: %s %v", status, e)
	}
	m.Version = "3"
	_, e = q.enqueue(ctx, tenant, "u", []byte("synthetic"), m)
	if e != nil {
		t.Fatal(e)
	}
	j, e = q.Claim(ctx, "test-worker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = q.Fail(ctx, j, "sensitive worker stdout", false); e != nil {
		t.Fatal(e)
	}
	var failure string
	if e = db.QueryRow("SELECT status,failure FROM addon_knowledge_jobs WHERE id=$1", j.ID).Scan(&status, &failure); e != nil || status != "failed" || failure != "processing_failed" {
		t.Fatalf("failure outcome invalid: %s %s %v", status, failure, e)
	}
	if e = q.Complete(ctx, j, pages, ev); !errors.Is(e, ErrDenied) {
		t.Fatal("failed scan job completed")
	}
}
