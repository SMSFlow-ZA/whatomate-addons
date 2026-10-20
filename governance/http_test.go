package governance

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"testing"
)

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
