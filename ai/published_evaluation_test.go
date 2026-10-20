//go:build integration

package ai

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"os"
	"testing"
	"time"
)

type observedGenerator struct {
	next    Generator
	sources []knowledge.Source
}

func (g *observedGenerator) Generate(ctx context.Context, r Request) (Completion, error) {
	g.sources = append([]knowledge.Source(nil), r.Sources...)
	return g.next.Generate(ctx, r)
}

// Uses actual governed published SQL pages and existing durable grants. Does not
// create publications or roles. This direct service evaluation is not an HTTP
// authentication/session acceptance test.
func TestActualPublishedModelEvaluation(t *testing.T) {
	dsn, tenant, principal, path, out := os.Getenv("AI_ACTUAL_DSN"), os.Getenv("AI_ACTUAL_TENANT"), os.Getenv("AI_ACTUAL_PRINCIPAL"), os.Getenv("AI_ACTUAL_QUESTIONS"), os.Getenv("AI_ACTUAL_OUTPUT")
	if dsn == "" || path == "" || out == "" {
		t.Skip("actual published evaluation environment required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal("database unavailable")
	}
	defer db.Close()
	repo, e := knowledge.NewPostgresRepository(db, tenant)
	if e != nil {
		t.Fatal(e)
	}
	authorizer := knowledge.AuthorizeFunc(func(ctx context.Context, p, a string) bool {
		var one int
		return db.QueryRowContext(ctx, `SELECT 1 FROM addon_knowledge_grants WHERE tenant=$1 AND principal=$2 AND action='read' AND scope=$3`, tenant, p, a).Scan(&one) == nil
	})
	provider, e := NewDevelopmentProvider(DevelopmentConfig{SyntheticDevelopment: true, Endpoint: "http://host.docker.internal:11434", ModelDigest: DevelopmentModelDigest, EmbeddingDigest: DevelopmentEmbeddingDigest, Timeout: 110 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	capture := &evaluationCapture{next: provider.client.Transport}
	provider.client.Transport = capture
	observed := &observedGenerator{next: provider}
	service := Service{Catalog: HybridRetriever{Catalog: repo, Candidates: repo, Embeddings: provider}, Authorizer: authorizer, Provider: observed, Now: time.Now}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	output, e := os.Create(out)
	if e != nil {
		t.Fatal(e)
	}
	defer output.Close()
	enc := json.NewEncoder(output)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var q struct {
			ID    string `json:"oracle_id"`
			Input string `json:"input"`
		}
		if json.Unmarshal(scanner.Bytes(), &q) != nil {
			t.Fatal("invalid questions")
		}
		capture.raw = nil
		capture.exchanges = nil
		observed.sources = nil
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		result := service.Answer(ctx, principal, "employees", q.Input)
		cancel()
		sources := []map[string]any{}
		for _, s := range observed.sources {
			sources = append(sources, map[string]any{"document_id": s.DocumentID, "version": s.Version, "page": s.Page.Number, "content_hash": s.ContentHash, "manifest_hash": s.ManifestHash, "extractor": s.Page.ExtractorRevision})
		}
		enc.Encode(map[string]any{"question_id": q.ID, "question": q.Input, "actual_result": result, "actual_native_response": capture.raw, "actual_native_responses": capture.exchanges, "retrieved_sources": sources, "elapsed_ms": time.Since(start).Milliseconds(), "evidence_mode": "ACTUAL governed SQL publications and durable current grant; actual scanned/poppler pages; direct Service, not HTTP/session acceptance", "semantic_review": "REQUIRED"})
		output.Sync()
		t.Log(q.ID, "handoff", result.Handoff, "bytes", len(result.Text))
	}
	if e = scanner.Err(); e != nil {
		t.Fatal(e)
	}
}
