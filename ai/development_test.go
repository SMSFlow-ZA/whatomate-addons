package ai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentProviderCannotUseRemoteOrUnpinnedModels(t *testing.T) {
	for _, c := range []DevelopmentConfig{{}, {SyntheticDevelopment: true, Endpoint: "http://example.com:11434", Timeout: time.Minute}, {SyntheticDevelopment: true, Endpoint: "http://127.0.0.1:11434", ModelDigest: "changed", Timeout: time.Minute}} {
		if _, e := NewDevelopmentProvider(c); e == nil {
			t.Fatal("unsafe development configuration")
		}
	}
}
func TestAuthorizedPolicySandboxRetainsLocalProviderBoundaries(t *testing.T) {
	valid := DevelopmentConfig{AuthorizedPolicySandbox: true, Endpoint: "http://127.0.0.1:11434", ModelDigest: DevelopmentModelDigest, EmbeddingDigest: DevelopmentEmbeddingDigest, Timeout: time.Minute}
	for _, endpoint := range []string{"http://127.0.0.1:11434", "http://host.docker.internal:11434"} {
		c := valid
		c.Endpoint = endpoint
		p, err := NewDevelopmentProvider(c)
		if err != nil || p.config.SyntheticDevelopment || !p.config.AuthorizedPolicySandbox {
			t.Fatal("explicit policy sandbox not preserved", err)
		}
		if p.client.Transport.(*http.Transport).Proxy != nil || p.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
			t.Fatal("local transport boundary changed")
		}
	}
	for _, mutate := range []func(*DevelopmentConfig){
		func(c *DevelopmentConfig) { c.AuthorizedPolicySandbox = false },
		func(c *DevelopmentConfig) { c.Endpoint = "https://remote.example.invalid" },
		func(c *DevelopmentConfig) { c.ModelDigest = "changed" },
		func(c *DevelopmentConfig) { c.EmbeddingDigest = "changed" },
		func(c *DevelopmentConfig) { c.Timeout = 111 * time.Second },
	} {
		c := valid
		mutate(&c)
		if _, err := NewDevelopmentProvider(c); err == nil {
			t.Fatal("unsafe sandbox accepted")
		}
	}
}
func TestHTTPRejectsMissingActorAndCallerSources(t *testing.T) {
	h := NewHTTPHandler(nil, HTTPConfig{Enabled: true})
	for _, body := range []string{`{}`, `{"sources":[]}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/extensions/ai/answers", strings.NewReader(body)))
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
