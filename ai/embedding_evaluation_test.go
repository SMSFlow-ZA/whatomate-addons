package ai

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLocalEmbeddingEvaluation(t *testing.T) {
	output := os.Getenv("AI_EMBEDDING_OUTPUT")
	if output == "" {
		t.Skip("AI_EMBEDDING_OUTPUT required")
	}
	p, e := NewDevelopmentProvider(DevelopmentConfig{SyntheticDevelopment: true, Endpoint: "http://host.docker.internal:11434", ModelDigest: DevelopmentModelDigest, EmbeddingDigest: DevelopmentEmbeddingDigest, Timeout: 110 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	v, e := p.Embed(context.Background(), []string{"Synthetic employee asks for an approved form.", "Synthetic request for a blank approved intake form.", "Synthetic unrelated cafeteria opening hours."})
	if e != nil {
		t.Fatal(e)
	}
	r := map[string]any{"model": DevelopmentEmbeddingModel, "digest": DevelopmentEmbeddingDigest, "vector_dimension": len(v[0]), "related_cosine": cosine(v[0], v[1]), "unrelated_cosine": cosine(v[0], v[2]), "elapsed_ms": time.Since(start).Milliseconds(), "actual_vectors": v, "fixture": "public synthetic phrases; actual local model inference"}
	b, _ := json.MarshalIndent(r, "", "  ")
	if e = os.WriteFile(output, b, 0600); e != nil {
		t.Fatal(e)
	}
}
