package ai

import (
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
