package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConversationalVerifierQuoteToleratesLayoutOnly(t *testing.T) {
	for _, tc := range []struct {
		name, source, quote string
		allowed             bool
	}{
		{"PDF line wrap", "Include an issue description\nand a preferred contact time.", "Include an issue description and a preferred contact time.", true},
		{"CRLF and tab", "Include an issue description\r\nand a preferred\tcontact time.", "Include an issue description and a preferred contact time.", true},
		{"changed word", "Include an issue description\nand a preferred contact time.", "Include an issue description and a required contact time.", false},
		{"changed punctuation", "Include an issue description\nand a preferred contact time.", "Include an issue description, and a preferred contact time.", false},
		{"changed case", "Include an issue description\nand a preferred contact time.", "include an issue description and a preferred contact time.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verification, _ := json.Marshal(map[string]any{"verdict": "supported", "supporting_quotes": []string{tc.quote}})
			rt := &repairTransport{responses: []string{`{"answer":"Give a short issue description and your preferred contact time."}`, string(verification)}}
			p, _, _ := repairFixture(rt)
			r := Request{Question: "What should I include?", Evidence: []EvidencePassage{{Reference: "practice", Text: tc.source}}}
			out, err := p.converse(context.Background(), r, Completion{Answer: tc.source, Citations: []string{"practice"}, Grounded: true})
			if err != nil || out.Grounded != tc.allowed || rt.calls != 2 {
				t.Fatal("layout/wording boundary", err, out.Grounded, rt.calls)
			}
			if tc.allowed {
				support := out.VerifiedSupport(r.Question)
				if len(support) != 1 || support[0].Quote != tc.source || !validCompletionSupport(r.Question, out, map[string]string{"practice": tc.source}) {
					t.Fatal("original immutable support was normalized or lost")
				}
			}
		})
	}
}

func TestConversationalWorstCaseCallBound(t *testing.T) {
	rt := &repairTransport{}
	for batch := 0; batch < 3; batch++ {
		rt.responses = append(rt.responses, `{"intent":"unrelated_or_missing","evidence_index":-1,"supporting_words":""}`)
		for candidate := 0; candidate < 3; candidate++ {
			response := `{"relationship":"topical_only","supporting_words":""}`
			if batch == 2 && candidate == 2 {
				response = `{"relationship":"direct_answer","supporting_words":"Synthetic equipment guidance"}`
			}
			rt.responses = append(rt.responses, response)
		}
	}
	rt.responses = append(rt.responses, `{"answer":"Store shared equipment carefully."}`, `{"verdict":"supported","supporting_quotes":["Synthetic equipment guidance"]}`)
	p, r, _ := repairFixture(rt)
	r.Evidence = nil
	for source := 0; source < 12; source++ {
		parts := []string{}
		for n := 0; n < 3; n++ {
			parts = append(parts, fmt.Sprintf("Synthetic equipment guidance requires careful storage of item %d.", source*3+n))
		}
		r.Evidence = append(r.Evidence, EvidencePassage{Reference: fmt.Sprint(source), Text: strings.Join(parts, "\n\n")})
	}
	selected, err := p.selectEvidence(context.Background(), r, passageSpans(r.Evidence), 3)
	if err != nil || !selected.Grounded {
		t.Fatal("last bounded candidate not selected", err)
	}
	out, err := p.converse(context.Background(), r, selected)
	if err != nil || !out.Grounded || rt.calls != 14 {
		t.Fatal("combined selector/writer/verifier call bound", err, rt.calls)
	}
}

func TestConversationalFullPipelinePreservesSourceAndCallBound(t *testing.T) {
	rt := &repairTransport{responses: []string{`{"supporting_words":"Store confidential papers","intent":"answerable","evidence_index":0}`, `{"supporting_words":"Store confidential papers","relationship":"direct_answer"}`, `{"answer":"Before leaving, put confidential papers in the locked cabinet."}`, `{"verdict":"supported","supporting_quotes":["Store confidential papers in the locked cabinet before leaving."]}`}}
	p, _, _ := repairFixture(rt)
	p.client.Transport = devTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/tags" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`))}, nil
		}
		return rt.RoundTrip(req)
	})
	r := Request{Question: "How should I store the papers?", Evidence: []EvidencePassage{{Reference: "trusted-reference", Text: "Store confidential papers in the locked cabinet before leaving."}}}
	out, err := GenerateGroundedEvidence(context.Background(), p, r)
	if err != nil || !out.Grounded || out.Answer == r.Evidence[0].Text || len(out.VerifiedSupport(r.Question)) != 1 || out.Citations[0] != "trusted-reference" || rt.calls != 4 {
		t.Fatal("full conversational path lost provenance or bounded calls", err, rt.calls)
	}
}

func TestConversationalGroundingAndTamperBoundary(t *testing.T) {
	rt := &repairTransport{responses: []string{`{"answer":"Before you leave, lock the papers in a cabinet."}`, `{"verdict":"supported","supporting_quotes":["Store confidential papers in a locked cabinet before leaving."]}`}}
	p, _, _ := repairFixture(rt)
	r := Request{Question: "How should I leave my desk?", Evidence: []EvidencePassage{{Reference: "policy:1", Text: "Store confidential papers in a locked cabinet before leaving."}}}
	selected := Completion{Answer: r.Evidence[0].Text, Citations: []string{"policy:1"}, Grounded: true}
	got, err := p.converse(context.Background(), r, selected)
	if err != nil || !got.Grounded || got.Answer == selected.Answer || !got.verifiedFor(r.Question) || rt.calls != 2 {
		t.Fatalf("grounded paraphrase rejected: %v", err)
	}
	if !validCompletionSupport(r.Question, got, map[string]string{"policy:1": r.Evidence[0].Text}) {
		t.Fatal("valid evidence rejected")
	}
	got.Answer += " You will be dismissed otherwise."
	if validCompletionSupport(r.Question, got, map[string]string{"policy:1": r.Evidence[0].Text}) {
		t.Fatal("altered answer accepted")
	}
}

func TestConversationalRejectsUnsupportedExtrasAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, verdict, quote string
		fail                 int
	}{
		{"invented sanction", "unsupported", "Store confidential papers in a locked cabinet before leaving.", 0},
		{"fabricated quote", "supported", "Dismiss employees immediately.", 0},
		{"missing support", "supported", "", 0},
		{"unknown verdict", "maybe", "Store confidential papers in a locked cabinet before leaving.", 0},
		{"transport", "supported", "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &repairTransport{responses: []string{`{"answer":"Store the papers securely or you will be dismissed."}`, `{"verdict":"` + tc.verdict + `","supporting_quotes":["` + tc.quote + `"]}`}, failAt: tc.fail}
			p, _, _ := repairFixture(rt)
			r := Request{Question: "What should I do?", Evidence: []EvidencePassage{{Reference: "p", Text: "Store confidential papers in a locked cabinet before leaving."}}}
			got, _ := p.converse(context.Background(), r, Completion{Answer: r.Evidence[0].Text, Citations: []string{"p"}, Grounded: true})
			if tc.verdict == "unsupported" {
				if !got.Grounded || strings.Contains(got.Answer, "dismissed") || !strings.Contains(got.Answer, "“"+r.Evidence[0].Text+"”") || !got.verifiedFor(r.Question) {
					t.Fatal("rejected draft did not fall back to visibly quoted exact evidence")
				}
			} else if got.Grounded {
				t.Fatal("invalid verifier output accepted")
			}
			if rt.calls != 2 {
				t.Fatal("unsupported output accepted or retried", rt.calls)
			}
		})
	}
}

func TestConversationalCallerCannotForgeEvidence(t *testing.T) {
	forged := Completion{Answer: "You may destroy the papers.", Citations: []string{"p"}, Grounded: true, Support: []SupportQuote{{Reference: "p", Quote: "Lock the papers away."}}}
	if validCompletionSupport("What now?", forged, map[string]string{"p": "Lock the papers away."}) {
		t.Fatal("caller forged verifier proof")
	}
}
