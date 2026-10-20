package cases

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPIntakeUsesServerClassificationWithoutClientPlaceholders(t *testing.T) {
	called := false
	h := NewHTTPHandler(nil, HTTPConfig{Enabled: true, ResolveIntake: func(context.Context, api.Actor) (Classification, error) {
		called = true
		return Classification{}, ErrDenied
	}})
	a := api.Actor{OrganizationID: "o", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{string(SubmitOwn)}, Grants: []api.Grant{{Permission: string(SubmitOwn), UnitID: "unit"}}}
	r := httptest.NewRequest("POST", "/api/extensions/cases/", strings.NewReader(`{"unit_id":"unit","key":"synthetic","narrative":"Synthetic concern","draft":false}`))
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called || w.Code != 403 {
		t.Fatalf("expected server intake policy without invented client fields; called=%v code=%d", called, w.Code)
	}
}
