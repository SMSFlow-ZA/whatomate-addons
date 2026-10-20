package ai

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderDeniesMissingAttestationAndUnlistedEndpoint(t *testing.T) {
	for _, c := range []ProviderConfig{{}, {Endpoint: "https://example.invalid/v1/chat/completions", AllowedEndpoints: []string{"https://other.invalid/v1/chat/completions"}, DeploymentAttestation: "review-1", Country: "ZA", Model: "pinned-1", PromptRevision: "prompt-1", Timeout: time.Second, MaxResponseBytes: 4096}} {
		if _, err := NewProvider(c); err == nil {
			t.Fatal("unsafe provider accepted")
		}
	}
}
func TestProviderBoundedHTTPAndPinnedModel(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string                           `json:"model"`
			Messages []struct{ Role, Content string } `json:"messages"`
			Tools    json.RawMessage                  `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "pinned-1" || len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "prompt-1") || len(request.Tools) != 0 || r.Method != http.MethodPost {
			http.Error(w, "invalid provider contract", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"pinned-1","choices":[{"message":{"content":"{\"answer\":\"Policy\",\"citations\":[\"ref-1\"],\"grounded\":true}"}}]}`))
	}))
	defer s.Close()
	c := ProviderConfig{Endpoint: s.URL, AllowedEndpoints: []string{s.URL}, DeploymentAttestation: "independent-review", Country: "ZA", Model: "pinned-1", PromptRevision: "prompt-1", Timeout: time.Second, MaxResponseBytes: 4096}
	p, err := newProvider(c, s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Generate(context.Background(), Request{Question: "policy", Sources: []knowledge.Source{{Reference: "ref-1", ManifestHash: "approved", Page: knowledge.Page{Number: 1, Text: "Policy"}}}})
	if err != nil || out.Answer != "Policy" {
		t.Fatalf("HTTP contract failed: %+v %v", out, err)
	}
	c.MaxResponseBytes = 8
	p, _ = newProvider(c, s.Client().Transport)
	if _, err = p.Generate(context.Background(), Request{Question: "policy", Sources: []knowledge.Source{{Reference: "ref-1", ManifestHash: "approved", Page: knowledge.Page{Number: 1, Text: "Policy"}}}}); err == nil {
		t.Fatal("oversized body accepted")
	}
}

type generatorFunc func(context.Context, Request) (Completion, error)

func (f generatorFunc) Generate(c context.Context, r Request) (Completion, error) { return f(c, r) }
func TestAnswerFailsClosedForUngroundedOrInventedCitations(t *testing.T) {
	src := knowledge.Source{Reference: "ref-1", ManifestHash: "approved", DocumentID: "guide", Version: "1", Page: knowledge.Page{Number: 1, Text: "Policy"}}
	for _, out := range []Completion{{Answer: "Invented", Grounded: true, Citations: []string{"made-up"}}, {Answer: "Guess", Grounded: false, Citations: []string{"ref-1"}}, {Answer: "No cite", Grounded: true}} {
		result := answer(context.Background(), generatorFunc(func(context.Context, Request) (Completion, error) { return out, nil }), Request{Question: "q", Sources: []knowledge.Source{src}})
		if !result.Handoff || result.Text != FixedResponse || len(result.Citations) != 0 {
			t.Fatalf("unsafe answer: %+v", result)
		}
	}
	good := answer(context.Background(), generatorFunc(func(context.Context, Request) (Completion, error) {
		return Completion{Answer: "Policy", Grounded: true, Citations: []string{"ref-1"}}, nil
	}), Request{Question: "q", Sources: []knowledge.Source{src}})
	if good.Handoff || len(good.Citations) != 1 || good.Citations[0].Page.Number != 1 {
		t.Fatalf("lost real provenance: %+v", good)
	}
}

func TestModelGroundedFlagCannotAuthorizeUnsupportedAnswer(t *testing.T) {
	result := answer(context.Background(), generatorFunc(func(context.Context, Request) (Completion, error) {
		return Completion{Answer: "Invented claim", Grounded: true, Citations: []string{"ref-1"}}, nil
	}), Request{Question: "q", Sources: []knowledge.Source{{Reference: "ref-1", ManifestHash: "approved", Page: knowledge.Page{Number: 1, Text: "Actual policy"}}}})
	if !result.Handoff {
		t.Fatal("model self-certification accepted unsupported prose")
	}
}
func TestClassificationCannotApplyWithoutHumanApproval(t *testing.T) {
	if (ClassificationSuggestion{Label: "restricted"}).Apply() != ErrHumanApprovalRequired {
		t.Fatal("suggestion acquired authority")
	}
}
func TestProviderRejectsRedirectModelMismatchAndTimeout(t *testing.T) {
	for _, mode := range []string{"redirect", "model", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "redirect":
					http.Redirect(w, r, "/fallback", http.StatusTemporaryRedirect)
				case "model":
					w.Write([]byte(`{"model":"unpinned","choices":[{"message":{"content":"{}"}}]}`))
				case "timeout":
					time.Sleep(30 * time.Millisecond)
					w.Write([]byte(`{}`))
				}
			}))
			defer s.Close()
			c := ProviderConfig{Endpoint: s.URL, AllowedEndpoints: []string{s.URL}, DeploymentAttestation: "review-1", Country: "ZA", Model: "pinned-1", PromptRevision: "prompt-1", Timeout: 10 * time.Millisecond, MaxResponseBytes: 4096}
			p, err := newProvider(c, s.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Generate(context.Background(), Request{Question: "policy", Sources: []knowledge.Source{{Reference: "ref-1"}}})
			if err == nil {
				t.Fatal("unsafe response accepted")
			}
			if mode == "redirect" && calls != 1 {
				t.Fatal("redirect followed")
			}
		})
	}
}
func TestServiceRefusesNoSourcesAndRechecksWithdrawal(t *testing.T) {
	c := knowledge.NewCatalog()
	calls := 0
	g := generatorFunc(func(context.Context, Request) (Completion, error) { calls++; return Completion{}, nil })
	s := Service{Catalog: c, Authorizer: knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return true }), Provider: g, Now: time.Now}
	result := s.Answer(context.Background(), "u", "staff", "policy")
	if !result.Handoff || calls != 0 {
		t.Fatal("provider called without approved sources")
	}
}

func TestServiceRejectsSourcesWithdrawnDuringGeneration(t *testing.T) {
	d := knowledge.Document{ID: "guide", Version: "1", Content: []byte("abc"), ContentHash: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Audiences: []string{"staff"}, Effective: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Pages: []knowledge.Page{{Number: 1, Text: "policy", Method: "text", ExtractorRevision: "extract-1"}}}
	p, err := knowledge.Publish(d, knowledge.Evidence{ContentHash: d.ContentHash, MalwareClean: true, ScannerRevision: "scan-1", ExtractionComplete: true}, knowledge.Approval{Reviewer: "reviewer", ManifestHash: knowledge.ManifestHash(d)})
	if err != nil {
		t.Fatal(err)
	}
	c := knowledge.NewCatalog()
	c.Add(p)
	s := Service{Catalog: c, Authorizer: knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return true }), Now: func() time.Time { return d.Effective.Add(time.Hour) }, Provider: generatorFunc(func(_ context.Context, r Request) (Completion, error) {
		c.Withdraw("guide", "1")
		return Completion{Answer: "policy", Grounded: true, Citations: []string{r.Sources[0].Reference}}, nil
	})}
	if out := s.Answer(context.Background(), "u", "staff", "policy"); !out.Handoff || len(out.Citations) != 0 {
		t.Fatal("withdrawal during generation bypassed")
	}
}

func TestGeneratorCannotRewriteRetrievedProvenance(t *testing.T) {
	source := knowledge.Source{Reference: "ref-1", ManifestHash: "approved", DocumentID: "guide", Version: "1", Page: knowledge.Page{Number: 1, Text: "Policy"}}
	result := answer(context.Background(), generatorFunc(func(_ context.Context, r Request) (Completion, error) {
		r.Sources[0].Page.Text = "Fabricated instruction"
		return Completion{Answer: "Fabricated instruction", Grounded: true, Citations: []string{"ref-1"}}, nil
	}), Request{Question: "q", Sources: []knowledge.Source{source}})
	if !result.Handoff {
		t.Fatal("provider rewrote approved page provenance")
	}
}

func TestProviderDoesNotInheritGlobalTLSBypass(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model":"pinned-1","choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = original }()
	p, err := NewProvider(ProviderConfig{Endpoint: server.URL, AllowedEndpoints: []string{server.URL}, DeploymentAttestation: "review", Country: "ZA", Model: "pinned-1", PromptRevision: "prompt-1", Timeout: time.Second, MaxResponseBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Generate(context.Background(), Request{Question: "q", Sources: []knowledge.Source{{Reference: "ref-1"}}}); err == nil {
		t.Fatal("global TLS trust bypass inherited")
	}
}
