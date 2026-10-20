package reports

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPRequiresFreshVerifiedActorAndExplicitPolicy(t *testing.T) {
	h := NewHTTPHandler(nil, HTTPConfig{Enabled: true, MinGroup: 5, RefreshActor: func(ctx context.Context, a api.Actor) (api.Actor, error) { return a, nil }})
	r := httptest.NewRequest("GET", "/api/extensions/reports/operational?start=2026-09-01&end=2026-10-01", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	a := api.Actor{OrganizationID: "o", UserID: "u", SessionID: "s", MFA: true, Permissions: []string{"reports:aggregate"}, Grants: []api.Grant{{Permission: "reports:aggregate", UnitID: "u"}}}
	r = r.WithContext(api.WithActor(r.Context(), a))
	r.URL.Path = "/api/extensions/reports/board"
	r.URL.RawQuery += "&zone=small"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("board filters", w.Code)
	}
	h = NewHTTPHandler(nil, HTTPConfig{Enabled: true, MinGroup: 5, Clock: func() time.Time { return time.Now() }})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("no revalidation callback", w.Code)
	}
}
func TestCSVNeutralizesFormulaLabels(t *testing.T) {
	for _, s := range []string{"=HYPERLINK(\"bad\")", " +formula", "\tcmd", "@cmd"} {
		if v := csvCell(s); len(v) == 0 || v[0] != '\'' {
			t.Fatal("unsafe", s, v)
		}
	}
	if csvCell("Normal") != "Normal" {
		t.Fatal("changed normal label")
	}
}
