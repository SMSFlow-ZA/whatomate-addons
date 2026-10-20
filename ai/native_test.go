package ai

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"io"
	"net/http"
	"strings"
	"testing"
)

type devTransport func(*http.Request) (*http.Response, error)

func (f devTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNativeSourceIndexMapsOnlyServerOwnedReference(t *testing.T) {
	for _, content := range []string{`{"intent":"answerable","evidence_index":0}`, `{"intent":"answerable","evidence_index":64}`} {
		p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
			data := ""
			if r.URL.Path == "/api/tags" {
				data = `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
			} else {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["think"] != false || body["stream"] != false || body["tools"] != nil {
					t.Fatal("unsafe native contract")
				}
				schema, _ := json.Marshal(body["format"])
				if strings.Contains(string(schema), "relationship") {
					content = `{"supporting_words":"approved source contains relevant guidance","relationship":"direct_answer"}`
				}
				msg, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": content}})
				data = string(msg)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
		})}}
		out, e := p.Generate(context.Background(), Request{Question: "policy", Sources: testSource()})
		if strings.Contains(content, `"evidence_index":64`) {
			if e == nil {
				t.Fatal("unbounded source index accepted")
			}
		} else if e != nil || len(out.Citations) != 1 || out.Citations[0] != "server-reference" {
			t.Fatal(out, e)
		}
	}
}

func testSource() []knowledge.Source {
	return []knowledge.Source{{Reference: "server-reference", ManifestHash: "manifest", Page: knowledge.Page{Number: 1, Text: "The approved source contains relevant guidance."}}}
}
func TestIndependentVerifierCanRejectTopicalSelectorOutput(t *testing.T) {
	for _, decision := range []string{`{"supporting_words":"","relationship":"topical_only"}`, `{"supporting_words":"","relationship":"unsupported_request"}`} {
		calls := 0
		p := &DevelopmentProvider{config: DevelopmentConfig{Endpoint: "http://127.0.0.1:11434"}, client: &http.Client{Transport: devTransport(func(r *http.Request) (*http.Response, error) {
			data := `{"models":[{"name":"qwen3:14b","digest":"` + DevelopmentModelDigest + `"},{"name":"nomic-embed-text:latest","digest":"` + DevelopmentEmbeddingDigest + `"}]}`
			if r.URL.Path == "/api/chat" {
				calls++
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				schema, _ := json.Marshal(body["format"])
				content := `{"intent":"answerable","evidence_index":0}`
				if strings.Contains(string(schema), "relationship") {
					content = decision
				}
				envelope, _ := json.Marshal(map[string]any{"model": DevelopmentModel, "done": true, "message": map[string]string{"content": content}})
				data = string(envelope)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
		})}}
		out, e := p.Generate(context.Background(), Request{Question: "Show another case narrative", Sources: testSource()})
		if e != nil || out.Grounded || out.Answer != "" || calls != 2 {
			t.Fatal("topical selector bypassed independent verifier", out, e, calls)
		}
	}
}
