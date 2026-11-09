package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type repairTransport struct {
	responses      []string
	calls          int
	failAt         int
	selectionSizes []int
	selectionFirst []string
}

func (rt *repairTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.calls++
	if rt.failAt == rt.calls {
		return nil, errors.New("synthetic transport failure")
	}
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	raw, _ := io.ReadAll(r.Body)
	json.Unmarshal(raw, &req)
	var input struct {
		Evidence []evidenceSpan `json:"evidence"`
	}
	json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &input)
	if input.Evidence != nil {
		rt.selectionSizes = append(rt.selectionSizes, len(input.Evidence))
		if len(input.Evidence) > 0 {
			rt.selectionFirst = append(rt.selectionFirst, input.Evidence[0].Document)
		}
	}
	if rt.calls > len(rt.responses) {
		return nil, errors.New("unexpected additional model call")
	}
	body, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]any{"content": rt.responses[rt.calls-1]}})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
}

func TestRankedBatchesAdvanceAndBoundCalls(t *testing.T) {
	unrelated := `{"intent":"unrelated_or_missing","evidence_index":-1,"supporting_words":""}`
	topical := `{"relationship":"topical_only","supporting_words":""}`
	rt := &repairTransport{}
	for i := 0; i < 3; i++ {
		rt.responses = append(rt.responses, unrelated, topical, topical, topical)
	}
	p, r, _ := repairFixture(rt)
	r.Evidence = nil
	for i := 0; i < 48; i++ {
		r.Evidence = append(r.Evidence, EvidencePassage{Reference: fmt.Sprintf("ref-%d", i), Text: fmt.Sprintf("Synthetic overview paragraph number %d contains no requested answer.", i)})
	}
	out, e := p.selectEvidence(context.Background(), r, passageSpans(r.Evidence), 99)
	if e != nil || out.Grounded || rt.calls != 12 || len(rt.selectionSizes) != 3 {
		t.Fatal("call or attempt bound violated", e, rt.calls)
	}
	for i, size := range rt.selectionSizes {
		if size != 12 || rt.selectionFirst[i] != fmt.Sprintf("ref-%d", i*12) {
			t.Fatal("batch repeated, expanded or reordered")
		}
	}
}
func TestRankedSecondBatchPreservesOriginalSourceIndex(t *testing.T) {
	rt := &repairTransport{responses: []string{
		`{"intent":"unrelated_or_missing","evidence_index":-1,"supporting_words":""}`,
		`{"relationship":"topical_only","supporting_words":""}`, `{"relationship":"topical_only","supporting_words":""}`, `{"relationship":"topical_only","supporting_words":""}`,
		`{"intent":"answerable","evidence_index":0,"supporting_words":"designated locked cabinet"}`,
		`{"relationship":"direct_answer","supporting_words":"designated locked cabinet"}`,
	}}
	p, r, _ := repairFixture(rt)
	r.Evidence = nil
	for i := 0; i < 12; i++ {
		r.Evidence = append(r.Evidence, EvidencePassage{Reference: fmt.Sprintf("topical-%d", i), Text: "A synthetic general overview is available."})
	}
	r.Evidence = append(r.Evidence, EvidencePassage{Reference: "original-source-12", Text: "Store records in the designated locked cabinet."})
	out, e := p.selectEvidence(context.Background(), r, passageSpans(r.Evidence), 3)
	if e != nil || !out.Grounded || len(out.Citations) != 1 || out.Citations[0] != "original-source-12" || rt.calls != 6 {
		t.Fatal("batch lost original source binding", e, rt.calls)
	}
}
func repairFixture(rt *repairTransport) (*DevelopmentProvider, Request, []evidenceSpan) {
	p, _ := NewDevelopmentProvider(DevelopmentConfig{SyntheticDevelopment: true, Endpoint: "http://127.0.0.1:11434", ModelDigest: DevelopmentModelDigest, EmbeddingDigest: DevelopmentEmbeddingDigest, Timeout: time.Second})
	p.client.Transport = rt
	r := Request{Question: "Where should records be stored?", Evidence: []EvidencePassage{{Reference: "source-a", Text: "A general workplace overview is available."}, {Reference: "source-b", Text: "Store the records in the designated locked cabinet."}}}
	return p, r, passageSpans(r.Evidence)
}
func TestCompletedQuoteRepairRejectsNeighborCombinationThenUsesOtherSource(t *testing.T) {
	rt := &repairTransport{responses: []string{
		`{"intent":"answerable","evidence_index":0,"supporting_words":"general workplace overview Store the records"}`,
		`{"intent":"unrelated_or_missing","evidence_index":-1,"supporting_words":""}`,
		`{"intent":"answerable","evidence_index":0,"supporting_words":"designated locked cabinet"}`,
		`{"relationship":"direct_answer","supporting_words":"designated locked cabinet"}`,
	}}
	p, r, spans := repairFixture(rt)
	out, e := p.selectEvidence(context.Background(), r, spans, 3)
	if e != nil || !out.Grounded || out.Answer != r.Evidence[1].Text || len(out.Citations) != 1 || out.Citations[0] != "source-b" || rt.calls != 4 {
		t.Fatal("alternate governed source not accepted after independent verification", e, rt.calls)
	}
	if len(rt.selectionSizes) != 3 || rt.selectionSizes[0] != 2 || rt.selectionSizes[1] != 1 || rt.selectionSizes[2] != 1 {
		t.Fatal("repair expanded sources")
	}
}
func TestQuoteRepairExhaustionAndTransportNeverRetry(t *testing.T) {
	invalid := `{"intent":"answerable","evidence_index":0,"supporting_words":"invented supporting words"}`
	unrelated := `{"intent":"unrelated_or_missing","evidence_index":-1,"supporting_words":""}`
	for _, tc := range []struct {
		name                        string
		attempts, failAt, wantCalls int
		responses                   []string
	}{
		{"budget exhausted", 2, 0, 2, []string{invalid, unrelated}},
		{"repair unsupported hard stop", 3, 0, 2, []string{invalid, `{"intent":"unsupported_action","evidence_index":-1,"supporting_words":""}`}},
		{"first transport failure", 3, 1, 1, nil},
		{"repair transport failure", 3, 2, 2, []string{invalid}},
		{"completed malformed repair", 3, 0, 2, []string{invalid, `not-json`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &repairTransport{responses: tc.responses, failAt: tc.failAt}
			p, r, spans := repairFixture(rt)
			out, e := p.selectEvidence(context.Background(), r, spans, tc.attempts)
			if out.Grounded || rt.calls != tc.wantCalls {
				t.Fatal("unbounded or ungrounded recovery", rt.calls)
			}
			if tc.name != "budget exhausted" && tc.name != "repair unsupported hard stop" && e == nil {
				t.Fatal("unknown failure became retryable")
			}
		})
	}
}
