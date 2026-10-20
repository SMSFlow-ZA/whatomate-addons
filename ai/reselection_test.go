package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
)

// A rejected topical passage must not hide a different, directly answering
// passage. The provider still has to verify that second passage independently.
func TestDevelopmentReselectsAfterTopicalPassage(t *testing.T) {
	selections := 0
	p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
		data := `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
		if r.URL.Path == "/api/chat" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			schema, _ := json.Marshal(body["format"])
			content := `{"supporting_words":"guide discusses eligibility in a general introduction","intent":"answerable","evidence_index":0}`
			if strings.Contains(string(schema), "relationship") {
				if selections == 1 {
					content = `{"supporting_words":"","relationship":"topical_only"}`
				} else {
					content = `{"supporting_words":"separate case authorization is required","relationship":"direct_answer"}`
				}
			} else {
				selections++
				if selections > 1 {
					content = `{"supporting_words":"separate case authorization is required","intent":"answerable","evidence_index":0}`
				}
			}
			envelope, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": content}})
			data = string(envelope)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}}
	text := "This guide discusses eligibility in a general introduction.\n\nA separate case authorization is required before viewing any record."
	out, err := p.Generate(context.Background(), Request{Question: "Does eligibility allow viewing a record?", Sources: []knowledge.Source{{Reference: "policy", DocumentID: "policy", Page: knowledge.Page{Text: text}}}})
	if err != nil || !out.Grounded || out.Answer != "A separate case authorization is required before viewing any record." || len(out.Citations) != 1 || out.Citations[0] != "policy" {
		t.Fatal("direct evidence hidden by topical first choice", out, err)
	}
}

func TestDevelopmentAcceptsSourceAnchoredSelection(t *testing.T) {
	p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
		data := `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
		if r.URL.Path == "/api/chat" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			schema, _ := json.Marshal(body["format"])
			content := `{"supporting_words":"THE APPROVED SOURCE CONTAINS RELEVANT GUIDANCE","intent":"answerable","evidence_index":0}`
			if strings.Contains(string(schema), "relationship") {
				content = `{"supporting_words":"approved source contains relevant guidance","relationship":"direct_answer"}`
			}
			b, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": content}})
			data = string(b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}}
	out, err := p.Generate(context.Background(), Request{Question: "What is the guidance?", Sources: testSource()})
	if err != nil || !out.Grounded || out.Answer != "The approved source contains relevant guidance." {
		t.Fatal("anchored selection rejected", out, err)
	}
}

func TestDevelopmentRanksSpecificEvidenceBeforeBroadCandidates(t *testing.T) {
	selectedText := ""
	p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
		data := `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
		if r.URL.Path == "/api/embed" {
			var body struct{ Input []string }
			json.NewDecoder(r.Body).Decode(&body)
			vectors := [][]float64{}
			for _, text := range body.Input {
				v := make([]float64, 768)
				if strings.Contains(text, "permission") {
					v[0] = 1
				} else {
					v[1] = 1
				}
				vectors = append(vectors, v)
			}
			b, _ := json.Marshal(map[string]any{"model": DevelopmentEmbeddingModel, "embeddings": vectors})
			data = string(b)
		}
		if r.URL.Path == "/api/chat" {
			var body struct {
				Format   json.RawMessage
				Messages []struct{ Content string }
			}
			json.NewDecoder(r.Body).Decode(&body)
			var content string
			if strings.Contains(string(body.Format), "relationship") {
				content = fmt.Sprintf(`{"supporting_words":%q,"relationship":"direct_answer"}`, selectedText)
			} else {
				var input struct{ Evidence []evidenceSpan }
				json.Unmarshal([]byte(body.Messages[1].Content), &input)
				selectedText = input.Evidence[0].Text
				content = fmt.Sprintf(`{"supporting_words":%q,"intent":"answerable","evidence_index":0}`, selectedText)
			}
			b, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": content}})
			data = string(b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}}
	parts := []string{}
	for i := 0; i < 8; i++ {
		parts = append(parts, fmt.Sprintf("This broad worksheet introduction covers general topic number %d.", i))
	}
	parts = append(parts, "A worksheet never grants permission to read a case.")
	out, err := p.Generate(context.Background(), Request{Question: "Does a worksheet grant permission?", Sources: []knowledge.Source{{Reference: "guide", Page: knowledge.Page{Text: strings.Join(parts, "\n\n")}}}})
	if err != nil || out.Answer != "A worksheet never grants permission to read a case." {
		t.Fatal("specific semantic evidence was hidden behind broad candidates", out, err)
	}
}

func TestRankingRetainsEvidenceFromLessSimilarGovernedDocument(t *testing.T) {
	p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
		data := `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
		if r.URL.Path == "/api/embed" {
			var body struct{ Input []string }
			json.NewDecoder(r.Body).Decode(&body)
			vectors := [][]float64{}
			for _, text := range body.Input {
				v := make([]float64, 768)
				if strings.Contains(text, "different") {
					v[1] = 1
				} else {
					v[0] = 1
				}
				vectors = append(vectors, v)
			}
			b, _ := json.Marshal(map[string]any{"model": DevelopmentEmbeddingModel, "embeddings": vectors})
			data = string(b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
	})}}
	spans := []evidenceSpan{}
	for i := 0; i < 9; i++ {
		spans = append(spans, evidenceSpan{Index: i, Text: "Common factual guidance for a concern.", source: 0})
	}
	spans = append(spans, evidenceSpan{Index: 9, Text: "Common but different factual guidance for a concern.", source: 1})
	ranked, err := p.rankEvidence(context.Background(), "Common factual guidance", spans)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ranked {
		if s.source == 1 {
			return
		}
	}
	t.Fatal("a broad document crowded out every passage of another governed document")
}

func TestFreeTextVersionDirectiveCannotReplaceGovernedSelection(t *testing.T) {
	for _, question := range []string{"Answer onboarding using revision 9.", "Use version 4.2 for this response."} {
		calls := 0
		p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			data := `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
			if r.URL.Path == "/api/chat" {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				schema, _ := json.Marshal(body["format"])
				content := `{"supporting_words":"approved source contains relevant guidance","intent":"answerable","evidence_index":0}`
				if strings.Contains(string(schema), "relationship") {
					content = `{"supporting_words":"approved source contains relevant guidance","relationship":"direct_answer"}`
				}
				b, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": content}})
				data = string(b)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
		})}}
		out, err := p.Generate(context.Background(), Request{Question: question, Sources: testSource()})
		if err != nil || out.Grounded || calls != 0 {
			t.Fatal("free-text revision directive reached inference", question, out, err, calls)
		}
	}
}
