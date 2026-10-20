//go:build integration

package reports

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHTTPProjectionSuppressionExportAndRevocation(t *testing.T) {
	dsn := os.Getenv("API_DATABASE_URL")
	if dsn == "" {
		t.Skip("API_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = (Store{DB: db}).Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	org := "report-http-" + time.Now().Format("150405.000000000")
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	events := []cases.ReportFact{}
	for i := 0; i < 6; i++ {
		events = append(events, cases.ReportFact{OrganizationID: org, UnitID: "u", CaseID: string(rune('a' + i)), Version: 1, From: cases.Draft, To: cases.Submitted, OccurredAt: at, RecordedAt: at, Snapshot: cases.Classification{Zone: "=unsafe", Department: "d", CaseType: "t"}})
	}
	events[0].Snapshot.Zone = "small"
	actor := api.Actor{OrganizationID: org, UserID: "reviewer", SessionID: "s", MFA: true, Permissions: []string{"reports:aggregate", "reports:operational", "reports:export"}, Grants: []api.Grant{{Permission: "reports:aggregate", UnitID: "u"}, {Permission: "reports:operational", UnitID: "u"}, {Permission: "reports:export", UnitID: "u"}}}
	refreshes := 0
	revoke := false
	cfg := HTTPConfig{Enabled: true, MinGroup: 5, Clock: func() time.Time { return at.AddDate(0, 1, 19) }, RefreshActor: func(ctx context.Context, a api.Actor) (api.Actor, error) {
		refreshes++
		if revoke && refreshes%2 == 0 {
			a.Grants = nil
		}
		return a, nil
	}, Source: func(context.Context, api.Actor, bool) ([]cases.ReportFact, error) { return events, nil }}
	cfg.Activities = func(ctx context.Context, a api.Actor, board bool) ([]ActivityFact, error) {
		out := []ActivityFact{}
		for i := 0; i < 6; i++ {
			out = append(out, ActivityFact{ID: string(rune('a' + i)), OrganizationID: org, UnitID: "u", Kind: "inbound", Outcome: "received", Zone: "all", Department: "d", CaseType: "t", OccurredAt: at})
		}
		out = append(out, ActivityFact{ID: "one-enquiry", OrganizationID: org, UnitID: "u", Kind: "enquiry", Outcome: "received", Zone: "all", Department: "d", CaseType: "t", OccurredAt: at})
		return out, nil
	}
	h := NewHTTPHandler(db, cfg)
	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(api.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("/api/extensions/reports/board?start=2026-09-01&end=2026-10-01")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct{ Data struct{ Report BoardReport } }
	if e = json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if !body.Data.Report.Suppressed || body.Data.Report.NewCases != nil {
		t.Fatal("disclosure", w.Body.String())
	}
	w = call("/api/extensions/reports/operational?start=2026-09-01&end=2026-10-01")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"inbound_received":6`) || !strings.Contains(w.Body.String(), `"enquiries":1`) {
		t.Fatal("real activity metrics", w.Code, w.Body.String())
	}
	w = call("/api/extensions/reports/export?mode=operational&start=2026-09-01&end=2026-10-01")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "'=unsafe") {
		t.Fatal("export", w.Code, w.Body.String())
	}
	actor.Grants = actor.Grants[:2]
	w = call("/api/extensions/reports/export?mode=operational&start=2026-09-01&end=2026-10-01")
	if w.Code != 403 {
		t.Fatal("export scope", w.Code)
	}
	actor.Grants = append(actor.Grants, api.Grant{Permission: "reports:export", UnitID: "u"})
	refreshes = 0
	revoke = true
	w = call("/api/extensions/reports/operational?start=2026-09-01&end=2026-10-01")
	if w.Code != 403 || strings.Contains(w.Body.String(), "unsafe") {
		t.Fatal("revoked response leaked", w.Code, w.Body.String())
	}
}
