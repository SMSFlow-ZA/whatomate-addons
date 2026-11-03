package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type quoteTransport func(*http.Request) (*http.Response, error)

func (f quoteTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestQuoteRepairLocksOriginalSelectedSource(t *testing.T) {
	calls := 0
	answers := []string{`{"intent":"answerable","evidence_index":1,"supporting_words":"Invented supporting phrase"}`, `{"intent":"answerable","evidence_index":0,"supporting_words":"Describe what happened"}`, `{"relationship":"direct_answer","supporting_words":"Describe what happened"}`}
	p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: quoteTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 3 {
			t.Fatal("unbounded repair")
		}
		var req struct {
			Messages []struct{ Role, Content string }
		}
		json.NewDecoder(r.Body).Decode(&req)
		if calls == 2 {
			var input struct {
				Question string
				Evidence []evidenceSpan
			}
			json.Unmarshal([]byte(req.Messages[1].Content), &input)
			if input.Question != "What should I describe?" || len(input.Evidence) != 1 || input.Evidence[0].Document != "legal:correct" || input.Evidence[0].Index != 0 {
				t.Fatal("repair drifted beyond selected source")
			}
		}
		raw, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": answers[calls-1]}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
	})}}
	r := Request{Question: "What should I describe?", Evidence: []EvidencePassage{{Reference: "legal:other", Text: "This unrelated source discusses working hours only."}, {Reference: "legal:correct", Text: "Describe what happened, when it happened and what support you need."}}}
	out, e := p.selectEvidence(context.Background(), r, passageSpans(r.Evidence), 3)
	if e != nil || !out.Grounded || len(out.Citations) != 1 || out.Citations[0] != "legal:correct" || calls != 3 {
		t.Fatal("source identity lost during index renumbering")
	}
}
func TestKnownInvalidQuoteRepairPreservesGrounding(t *testing.T) {
	for _, tt := range []struct {
		name         string
		contents     []string
		transport    bool
		wantGrounded bool
		calls        int
		stage        string
	}{
		{"repair_then_independent_verifier", []string{`{"intent":"answerable","evidence_index":0,"supporting_words":"Invented supporting phrase"}`, `{"intent":"answerable","evidence_index":0,"supporting_words":"Describe what happened"}`, `{"relationship":"direct_answer","supporting_words":"Describe what happened"}`}, false, true, 3, ""},
		{"repeated_bad_quote_stops", []string{`{"intent":"answerable","evidence_index":0,"supporting_words":"Invented supporting phrase"}`, `{"intent":"answerable","evidence_index":0,"supporting_words":"Invented supporting phrase"}`}, false, false, 2, "supporting_quote_invalid"},
		{"repaired_decline_cannot_use_shortlist_fallback", []string{`{"intent":"answerable","evidence_index":0,"supporting_words":"Invented supporting phrase"}`, `{"intent":"unrelated_or_missing","evidence_index":-1,"supporting_words":""}`}, false, false, 2, ""},
		{"verifier_still_rejects", []string{`{"intent":"answerable","evidence_index":0,"supporting_words":"Invented supporting phrase"}`, `{"intent":"answerable","evidence_index":0,"supporting_words":"Describe what happened"}`, `{"relationship":"topical_only","supporting_words":""}`}, false, false, 3, ""},
		{"transport_unknown_never_retries", nil, true, false, 1, ""},
		{"malformed_json_never_repairs", []string{`not json`}, false, false, 1, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var firstUser string
			p := &DevelopmentProvider{client: &http.Client{Transport: quoteTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if tt.transport {
					return nil, errors.New("synthetic transport failure")
				}
				if calls > len(tt.contents) {
					t.Fatal("unexpected retry")
				}
				var req struct {
					Messages []struct{ Role, Content string }
				}
				json.NewDecoder(r.Body).Decode(&req)
				if calls == 1 {
					firstUser = req.Messages[1].Content
				}
				if calls == 2 {
					if req.Messages[1].Content != firstUser || !strings.Contains(req.Messages[0].Content, "previous selection") {
						t.Fatal("repair changed evidence/question or omitted correction instruction")
					}
				}
				raw, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": tt.contents[calls-1]}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
			})}, config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434", Timeout: time.Second}}
			r := Request{Question: "What should I describe?", Evidence: []EvidencePassage{{Reference: "legal:fixture", Title: "Illustrative", Text: "Describe what happened, when it happened and what support you need."}}}
			out, e := p.selectEvidence(context.Background(), r, passageSpans(r.Evidence), 3)
			if out.Grounded != tt.wantGrounded || calls != tt.calls {
				t.Fatalf("grounded=%v calls=%d", out.Grounded, calls)
			}
			if tt.wantGrounded && (e != nil || out.Answer != r.Evidence[0].Text) {
				t.Fatal("answer must remain immutable exact source")
			}
			if tt.stage != "" && FailureStage(e) != tt.stage {
				t.Fatal("missing safe stage")
			}
		})
	}
}
