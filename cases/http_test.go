package cases

import (
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRejectsUntrustedAndSpoofedIntake(t *testing.T) {
	h := NewHTTPHandler(nil, HTTPConfig{Enabled: true})
	for _, actor := range []api.Actor{{}, {OrganizationID: "o", UserID: "u", SessionID: "s", MFA: false, Permissions: []string{string(SubmitOwn)}}} {
		r := httptest.NewRequest("GET", "/api/extensions/cases", nil)
		r = r.WithContext(api.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	a := api.Actor{OrganizationID: "o", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{string(SubmitOwn)}, Grants: []api.Grant{{Permission: string(SubmitOwn), UnitID: "unit"}}}
	r := httptest.NewRequest("POST", "/api/extensions/cases", strings.NewReader(`{"unit_id":"unit","key":"m","narrative":"n","snapshot":{"zone":"z","department":"d","case_type":"c"},"contact_id":"spoofed"}`))
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.Grants = []api.Grant{{Permission: string(SubmitOwn), UnitID: "*"}}
	r = httptest.NewRequest("POST", "/api/extensions/cases", strings.NewReader(`{"unit_id":"unit","key":"m","narrative":"n","snapshot":{"zone":"z","department":"d","case_type":"c"}}`))
	r = r.WithContext(api.WithActor(r.Context(), a))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}
