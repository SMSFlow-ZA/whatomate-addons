package ai

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLocalClassificationEvaluation(t *testing.T) {
	path := os.Getenv("AI_CLASSIFICATION_OUTPUT")
	if path == "" {
		t.Skip("AI_CLASSIFICATION_OUTPUT required")
	}
	p, e := NewDevelopmentProvider(DevelopmentConfig{SyntheticDevelopment: true, Endpoint: "http://host.docker.internal:11434", ModelDigest: DevelopmentModelDigest, EmbeddingDigest: DevelopmentEmbeddingDigest, Timeout: 110 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	input := "Synthetic rehearsal: a staff member raises a concern about their manager. No outcome has been decided."
	labels := []string{"Workplace concern", "Attendance concern", "Other"}
	v, e := p.Suggest(context.Background(), input, labels)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.MarshalIndent(map[string]any{"model": DevelopmentModel, "digest": DevelopmentModelDigest, "input": input, "allowed_labels": labels, "actual_suggestion": v, "authority": "none; must be human reviewed; no case mutated"}, "", "  ")
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
}
