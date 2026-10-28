package governance

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRejectsOwnerOnlyHoldDependencies(t *testing.T) {
	a := api.Actor{OrganizationID: "org", UserID: "user", SessionID: "session", MFA: true}
	r := httptest.NewRequest("POST", "/api/extensions/governance/discover", strings.NewReader(`{"unit_id":"unit","subject_id":"employee","hold_lineage_ids":["cases:case"]}`))
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	NewHTTPHandler(&sql.DB{}, HTTPConfig{Enabled: true, RefreshActor: func(context.Context, api.Actor) (api.Actor, error) { return a, nil }}).ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("trusted owner hold metadata accepted from HTTP: %d", w.Code)
	}
}

func TestHTTPDoesNotTrustStaleOrReplacementActor(t *testing.T) {
	actor := api.Actor{OrganizationID: "org", UserID: "user", SessionID: "session", MFA: true}
	for name, refresh := range map[string]func(context.Context, api.Actor) (api.Actor, error){"missing": nil, "revoked": func(context.Context, api.Actor) (api.Actor, error) { return api.Actor{}, api.ErrDenied }, "replacement": func(context.Context, api.Actor) (api.Actor, error) {
		other := actor
		other.UserID = "other"
		return other, nil
	}, "lost-mfa": func(context.Context, api.Actor) (api.Actor, error) {
		other := actor
		other.MFA = false
		return other, nil
	}} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/extensions/governance/inventory?unit_id=unit", nil)
			r = r.WithContext(api.WithActor(r.Context(), actor))
			w := httptest.NewRecorder()
			NewHTTPHandler(nil, HTTPConfig{Enabled: true, RefreshActor: refresh}).ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("expected fail closed before storage, got %d", w.Code)
			}
		})
	}
}
