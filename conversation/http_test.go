package conversation

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSimulationRequiresBothTrustedMFAAndDevelopment(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/api/extensions/conversation/simulate", strings.NewReader(`{"sender":"+27820000001","text":"hello","provider_message_id":"a"}`))
		w := httptest.NewRecorder()
		NewHTTPHandler(nil, HTTPConfig{EnabledSyntheticDevelopment: enabled}).ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	a := api.Actor{OrganizationID: "tenant", UserID: "operator", SessionID: "session", MFA: true, Permissions: []string{"conversation:simulate"}}
	r := httptest.NewRequest("POST", "/api/extensions/conversation/simulate", strings.NewReader(`{}`))
	r = r.WithContext(api.WithActor(context.Background(), a))
	w := httptest.NewRecorder()
	NewHTTPHandler(nil, HTTPConfig{}).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
