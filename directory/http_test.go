package directory

import (
	"bytes"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func directoryRequest(h http.Handler, actor api.Actor, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/extensions/directory/"+path, bytes.NewReader(b))
	r = r.WithContext(api.WithActor(r.Context(), actor))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPDefaultDenyAndUnavailable(t *testing.T) {
	h := NewHTTPHandler(nil, HTTPConfig{Freshness: time.Hour, Clock: time.Now})
	for _, a := range []api.Actor{{}, {OrganizationID: "t", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{"admin"}}} {
		if w := directoryRequest(h, a, "GET", "status", nil); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	a := api.Actor{OrganizationID: "t", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{"directory:read"}}
	if w := directoryRequest(h, a, "GET", "status", nil); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHTTPRequiresExactActionAndMFA(t *testing.T) {
	h := NewHTTPHandler(nil, HTTPConfig{Freshness: time.Hour, Clock: time.Now})
	a := api.Actor{OrganizationID: "t", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{"directory:read"}}
	for _, route := range []string{"preview", "approve", "revoke"} {
		w := directoryRequest(h, a, "POST", route, map[string]any{})
		if w.Code != 403 {
			t.Fatalf("read granted mutation %s: %d", route, w.Code)
		}
	}
	a.MFA = false
	if w := directoryRequest(h, a, "GET", "roster", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	a.MFA = true
	a.Permissions = []string{"directory:*"}
	if w := directoryRequest(h, a, "GET", "roster", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
