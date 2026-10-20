package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLocalModelEvaluation is opt-in: it consumes an external corpus and writes
// actual outputs outside the public checkout. It never publishes documents.
func TestLocalModelEvaluation(t *testing.T) {
	path, output := os.Getenv("AI_EVALUATION_CORPUS"), os.Getenv("AI_EVALUATION_OUTPUT")
	if path == "" || output == "" {
		t.Skip("external evaluation paths required")
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var corpus struct {
		Cases []struct {
			ID       string `json:"case_id"`
			Input    string `json:"input"`
			Scenario string `json:"scenario"`
			Pages    []struct {
				Text     string `json:"exact_text"`
				Hash     string `json:"sha256"`
				Document string `json:"document_id"`
				Revision string `json:"revision"`
				Page     int    `json:"page"`
				Original string `json:"original_sha256"`
			} `json:"source_pages"`
		} `json:"cases"`
	}
	if json.Unmarshal(data, &corpus) != nil {
		t.Fatal("invalid corpus")
	}
	p, e := NewDevelopmentProvider(DevelopmentConfig{SyntheticDevelopment: true, Endpoint: "http://host.docker.internal:11434", ModelDigest: DevelopmentModelDigest, EmbeddingDigest: DevelopmentEmbeddingDigest, Timeout: 110 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	capture := &evaluationCapture{next: p.client.Transport}
	p.client.Transport = capture
	f, e := os.Create(output)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, c := range corpus.Cases {
		sources := []knowledge.Source{}
		valid := true
		for _, s := range c.Pages {
			hash := sha256.Sum256([]byte(s.Text))
			if hex.EncodeToString(hash[:]) != s.Hash {
				valid = false
			}
			sources = append(sources, knowledge.Source{Reference: s.Document + "@" + s.Revision + ":" + s.Hash, DocumentID: s.Document, Version: s.Revision, ContentHash: s.Original, ManifestHash: "external-oracle-" + s.Hash, Page: knowledge.Page{Number: s.Page, Text: s.Text}})
		}
		capture.raw = nil
		capture.exchanges = nil
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		var raw Completion
		var modelError string
		var result Result
		if !valid {
			modelError = "source_hash_mismatch"
			result = fallback()
		} else if len(sources) == 0 {
			modelError = "no_oracle_sources_model_not_called"
			result = fallback()
		} else {
			raw, e = p.Generate(ctx, Request{Question: c.Input, Sources: sources})
			if e != nil {
				modelError = e.Error()
				result = fallback()
			} else {
				result = answer(ctx, generatorFunc(func(context.Context, Request) (Completion, error) { return raw, nil }), Request{Question: c.Input, Sources: sources})
			}
		}
		cancel()
		if e = enc.Encode(map[string]any{"case_id": c.ID, "scenario": c.Scenario, "model": DevelopmentModel, "model_digest": DevelopmentModelDigest, "native_response": capture.raw, "native_responses": capture.exchanges, "actual_completion": raw, "service_result": result, "model_error": modelError, "duration_ms": time.Since(start).Milliseconds(), "source_hashes_verified": valid, "source_approval_scanning": "SUBSTITUTED external exact-page oracle; no publication; no full-stack authorization claim", "semantic_review": "REQUIRED"}); e != nil {
			t.Fatal(e)
		}
		f.Sync()
		t.Log(c.ID, "handoff", result.Handoff, "error", modelError)
	}
}

type evaluationCapture struct {
	next      http.RoundTripper
	raw       json.RawMessage
	exchanges []json.RawMessage
	requests  []json.RawMessage
}

func (c *evaluationCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/api/chat" && r.Body != nil {
		data, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(data))
		c.requests = append(c.requests, append(json.RawMessage(nil), data...))
	}
	response, e := c.next.RoundTrip(r)
	if e == nil && r.URL.Path == "/api/chat" {
		data, readError := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
		response.Body.Close()
		if readError != nil {
			return nil, readError
		}
		response.Body = io.NopCloser(bytes.NewReader(data))
		c.raw = append(json.RawMessage(nil), data...)
		c.exchanges = append(c.exchanges, append(json.RawMessage(nil), data...))
	}
	return response, e
}
