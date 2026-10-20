//go:build integration

package processing

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	_ "github.com/jackc/pgx/v5/stdlib"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This fixture processor tests queue/controller transaction boundaries only.
// Genuine scanner acceptance is separate and must never cite this as a scan.
type fixtureProcessor struct{ forged bool }

func (p fixtureProcessor) Process(_ context.Context, j documents.Job) (documents.Output, documents.Receipt, error) {
	o := documents.Output{Document: knowledge.Document{ID: j.Metadata.ID, Version: j.Metadata.Version, Content: j.Input, ContentHash: j.InputHash, Audiences: j.Metadata.Audiences, Effective: j.Metadata.Effective, Pages: []knowledge.Page{{Number: 1, Text: "fixture-only", Method: "text", ExtractorRevision: "fixture"}}}, Evidence: knowledge.Evidence{ContentHash: j.InputHash, ScannerRevision: "FIXTURE-NOT-SCAN", MalwareClean: true, ExtractionComplete: true}, Provenance: []documents.PageProvenance{{Number: 1, Method: "text", SourceHash: j.InputHash, TextHash: hash([]byte("fixture-only")), ExtractorRevision: "fixture"}}}
	m, _ := json.Marshal(j.Metadata)
	manifest := documents.JobManifest{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: hash(m)}
	var b bytes.Buffer
	json.NewEncoder(&b).Encode(documents.JobArtifact{JobManifest: manifest, Output: o})
	now := time.Now().UTC()
	r := documents.Receipt{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: manifest.MetadataHash, OutputHash: hash(b.Bytes()), Image: "sha256:" + strings.Repeat("a", 64), DefinitionsHash: strings.Repeat("b", 64), CompletedAt: now, SignaturesValidUntil: now.Add(time.Hour)}
	if p.forged {
		r.ActorID = "substituted"
	}
	return o, r, nil
}
func TestDurableQueueBridgeRejectsSubstitutionAndReplays(t *testing.T) {
	raw := os.Getenv("KNOWLEDGE_DATABASE_URL")
	if raw == "" {
		t.Skip("real disposable postgres not configured")
	}
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	admin, e := sql.Open("pgx", raw)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	var random [8]byte
	rand.Read(random[:])
	name := "processing_test_" + hex.EncodeToString(random[:])
	if _, e = admin.Exec("CREATE DATABASE " + name); e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { db.Close(); admin.Exec("DROP DATABASE " + name) }()
	ctx := context.Background()
	if e = knowledge.MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	actor := api.Actor{OrganizationID: "tenant", UserID: "uploader", SessionID: "session", MFA: true, Permissions: []string{"knowledge:upload"}, Grants: []api.Grant{{Permission: "knowledge:upload", Audience: "employees"}}}
	handler := knowledge.NewHTTPHandler(db, knowledge.HTTPConfig{Enabled: true, Authorize: func(context.Context, string, string, string, string) bool { return true }})
	upload := func() string {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		f, _ := w.CreateFormFile("file", "synthetic.pdf")
		f.Write([]byte("%PDF-synthetic controller fixture"))
		m := knowledge.UploadMetadata{DocumentID: "synthetic", Version: "1", Filename: "synthetic.pdf", MediaType: "application/pdf", Audiences: []string{"employees"}, Effective: time.Now().UTC()}
		mb, _ := json.Marshal(m)
		w.WriteField("metadata", string(mb))
		w.Close()
		req := httptest.NewRequest("POST", "/api/extensions/knowledge/uploads", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req = req.WithContext(api.WithActor(ctx, actor))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 202 {
			t.Fatalf("upload %d %s", rec.Code, rec.Body)
		}
		var response struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		json.Unmarshal(rec.Body.Bytes(), &response)
		return response.Data.ID
	}
	bridge := Bridge{Queue: knowledge.NewProcessingQueue(db), Processor: fixtureProcessor{}, WorkerID: "fixture-worker", ProfileID: "FIXTURE-NOT-SCAN", Image: "sha256:" + strings.Repeat("a", 64), DefinitionsHash: strings.Repeat("b", 64), Lease: 8 * time.Minute}
	id := upload()
	if e = bridge.Once(ctx); e != nil {
		t.Fatal(e)
	}
	var status string
	var receipt []byte
	if e = db.QueryRow("SELECT status,receipt FROM addon_knowledge_jobs WHERE id=$1", id).Scan(&status, &receipt); e != nil || status != "processed" || !bytes.Contains(receipt, []byte("FIXTURE-NOT-SCAN")) {
		t.Fatalf("notdurable %s %v", status, e)
	}
	if e = bridge.Once(ctx); e != sql.ErrNoRows {
		t.Fatalf("completed job replayed: %v", e)
	}
	bad := upload()
	bridge.Processor = fixtureProcessor{forged: true}
	if e = bridge.Once(ctx); e == nil {
		t.Fatal("substitution accepted")
	}
	db.QueryRow("SELECT status FROM addon_knowledge_jobs WHERE id=$1", bad).Scan(&status)
	if status != "failed" {
		t.Fatalf("unsafe status %s", status)
	}
}
