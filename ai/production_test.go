package ai

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func productionFixture(endpoint string) ProviderConfig {
	return ProviderConfig{Endpoint: endpoint + "/chat", AllowedEndpoints: []string{endpoint + "/chat", endpoint + "/embed"}, DeploymentAttestation: "reviewed-evidence", Country: "ZA", Model: "pinned-chat", PromptRevision: "reviewed-prompt", Timeout: time.Second, MaxResponseBytes: 4096}
}
func TestProductionProviderRequiresCompleteReviewedConfiguration(t *testing.T) {
	c := productionFixture("https://example.invalid")
	e := EmbeddingConfig{Endpoint: "https://example.invalid/embed", Model: "pinned-embed", Dimensions: 3}
	for _, mutate := range []func(*ProviderConfig, *EmbeddingConfig){
		func(c *ProviderConfig, e *EmbeddingConfig) { c.DeploymentAttestation = "" },
		func(c *ProviderConfig, e *EmbeddingConfig) { c.Country = "US" },
		func(c *ProviderConfig, e *EmbeddingConfig) { e.Endpoint = "http://example.invalid/embed" },
		func(c *ProviderConfig, e *EmbeddingConfig) { e.Endpoint = "https://unlisted.invalid/embed" },
		func(c *ProviderConfig, e *EmbeddingConfig) { e.Model = "" },
		func(c *ProviderConfig, e *EmbeddingConfig) { e.Dimensions = 0 },
		func(c *ProviderConfig, e *EmbeddingConfig) { e.Dimensions = 4097 },
	} {
		config, embedding := c, e
		mutate(&config, &embedding)
		if _, err := NewProductionProvider(config, embedding); err == nil {
			t.Fatal("incomplete production configuration accepted")
		}
	}
}
func TestProductionProviderEmbeddingsAndAdvisoryClassification(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("authentication missing")
		}
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Fatal("bad request")
		}
		switch r.URL.Path {
		case "/embed":
			if in["model"] != "pinned-embed" {
				t.Error("unpinned embedding")
			}
			w.Write([]byte(`{"model":"pinned-embed","data":[{"index":1,"embedding":[0,1,0]},{"index":0,"embedding":[1,0,0]}]}`))
		case "/chat":
			if in["model"] != "pinned-chat" || in["tools"] != nil {
				t.Error("unsafe classifier contract")
			}
			w.Write([]byte(`{"model":"pinned-chat","choices":[{"message":{"content":"{\"label\":\"Workplace concern\",\"rationale\":\"Provisional concern\"}"}}]}`))
		default:
			t.Error("unconfigured endpoint")
		}
	}))
	defer s.Close()
	c := productionFixture(s.URL)
	c.APIKey = "synthetic-key"
	p, err := newProductionProvider(c, EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3}, s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := p.Embed(context.Background(), []string{"question", "passage"})
	if err != nil || len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("embedding order or response invalid: %v %v", vectors, err)
	}
	suggestion, err := p.Suggest(context.Background(), "A concern", []string{"Workplace concern"})
	if err != nil || suggestion.Label != "Workplace concern" || suggestion.Apply() != ErrHumanApprovalRequired {
		t.Fatalf("classification acquired authority or failed: %+v %v", suggestion, err)
	}
}
func TestProductionProviderRejectsUnboundedOrUntrustedResults(t *testing.T) {
	for _, body := range []string{
		`{"model":"pinned-embed","data":[{"embedding":[1,0,0]}]}`,
		`{"model":"other","data":[{"index":0,"embedding":[1,0,0]}]}`,
		`{"model":"pinned-embed","data":[{"index":0,"embedding":[1,0]}]}`,
		`{"model":"pinned-embed","data":[{"index":0,"embedding":[0,0,0]}]}`,
		`{"model":"pinned-embed","data":[{"index":1,"embedding":[1,0,0]}]}`,
		`{"model":"pinned-embed","data":[{"index":0,"embedding":[1e999,0,0]}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer s.Close()
			p, err := newProductionProvider(productionFixture(s.URL), EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3}, s.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Embed(context.Background(), []string{"question"}); err == nil {
				t.Fatal("untrusted vector accepted")
			}
		})
	}
}

func TestProductionClassifierRejectsAuthorityAndMalformedReplies(t *testing.T) {
	for _, content := range []string{
		`{"label":"Other","rationale":"unlisted"}`,
		`{"label":"Workplace concern","rationale":"ok","action":"close-case"}`,
		`{"label":"Workplace concern","rationale":"ok"} {}`,
		`{"label":"Workplace concern","rationale":""}`,
	} {
		t.Run(content, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"model": "pinned-chat", "choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
			}))
			defer s.Close()
			p, err := newProductionProvider(productionFixture(s.URL), EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3}, s.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Suggest(context.Background(), "concern", []string{"Workplace concern"}); err == nil {
				t.Fatal("unsafe classification accepted")
			}
		})
	}
}

func TestProductionProviderDoesNotFollowEmbeddingRedirects(t *testing.T) {
	calls := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/fallback", http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	p, err := newProductionProvider(productionFixture(s.URL), EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3}, s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Embed(context.Background(), []string{"question"}); err == nil || calls != 1 {
		t.Fatal("embedding redirect followed")
	}
}

func TestProductionServiceRetainsCitationsAndNoSourceHandoff(t *testing.T) {
	calls := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"model":"pinned-chat","choices":[{"message":{"content":"{\"answer\":\"Approved policy\",\"citations\":[\"ref-1\"],\"grounded\":true}"}}]}`))
	}))
	defer s.Close()
	p, err := newProductionProvider(productionFixture(s.URL), EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3}, s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	result := answer(context.Background(), p, Request{Question: "policy", Sources: []knowledge.Source{{Reference: "ref-1", ManifestHash: "approved", Page: knowledge.Page{Number: 1, Text: "Approved policy"}}}})
	if result.Handoff || result.Text != "Approved policy" || len(result.Citations) != 1 || calls != 1 {
		t.Fatalf("production lost provenance: %+v calls=%d", result, calls)
	}
	result = answer(context.Background(), p, Request{Question: "policy"})
	if !result.Handoff || result.Text != FixedResponse || calls != 1 {
		t.Fatal("no-source request reached production provider")
	}
}

func TestProductionProviderEmbeddingsDoNotInheritGlobalTLSBypass(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS server received data") }))
	defer s.Close()
	original := http.DefaultTransport
	http.DefaultTransport = s.Client().Transport
	defer func() { http.DefaultTransport = original }()
	p, err := NewProductionProvider(productionFixture(s.URL), EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Embed(context.Background(), []string{"question"}); err == nil {
		t.Fatal("global TLS override inherited by embedding client")
	}
}

func TestProviderProcessingScopeRequiresExplicitInternationalApproval(t *testing.T) {
	c := productionFixture("https://example.invalid")
	for _, scope := range []string{"", "za_only"} {
		c.ProcessingScope = scope
		if _, e := NewProvider(c); e != nil {
			t.Fatal(e)
		}
	}
	c.ProcessingScope = "global_standard"
	if _, e := NewProvider(c); e == nil {
		t.Fatal("international inference accepted without approval")
	}
	c.InternationalProcessingApproval = "operator-reviewed-approval"
	if _, e := NewProvider(c); e == nil {
		t.Fatal("international inference accepted without disclosure")
	}
	c.InternationalProcessingDisclosure = "Stored data remains in ZA; model processing may occur internationally."
	if _, e := NewProvider(c); e != nil {
		t.Fatal(e)
	}
	c.ProcessingScope = "unknown"
	if _, e := NewProvider(c); e == nil {
		t.Fatal("unknown processing scope accepted")
	}
}

func TestProviderRejectsGatewayProcessingScopeMismatch(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embed" {
			w.Write([]byte(`{"model":"pinned-embed","data":[{"index":0,"embedding":[1,0,0]}],"gateway_metadata":{"processing_scope":"global_standard","international_processing_disclosure":"international inference"}}`))
			return
		}
		w.Write([]byte(`{"model":"pinned-chat","choices":[{"message":{"content":"{\"answer\":\"Approved policy\",\"citations\":[\"ref-1\"],\"grounded\":true}"}}],"gateway_metadata":{"processing_scope":"global_standard","international_processing_disclosure":"international inference"}}`))
	}))
	defer s.Close()
	p, err := newProductionProvider(productionFixture(s.URL), EmbeddingConfig{Endpoint: s.URL + "/embed", Model: "pinned-embed", Dimensions: 3}, s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Generate(context.Background(), Request{Question: "policy", Sources: []knowledge.Source{{Reference: "ref-1"}}}); err == nil {
		t.Fatal("gateway international chat accepted by ZA-only host")
	}
	if _, err = p.Embed(context.Background(), []string{"policy"}); err == nil {
		t.Fatal("gateway international embedding accepted by ZA-only host")
	}
}
