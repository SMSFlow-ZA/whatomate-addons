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

func TestTrendHTTPReconcilesFiltersScopeExportAndMidRequestChanges(t *testing.T) {
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
	org := "trend-" + time.Now().Format("150405.000000000")
	sep := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	oct := sep.AddDate(0, 1, 0)
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	events := []cases.ReportFact{{OrganizationID: org, UnitID: "u", CaseID: "a", Version: 1, From: cases.Draft, To: cases.Submitted, OccurredAt: sep, RecordedAt: sep, Snapshot: cases.Classification{Zone: "=original", Department: "original department", CaseType: "t"}}, {OrganizationID: org, UnitID: "other-unit", CaseID: "b", Version: 1, From: cases.Draft, To: cases.Submitted, OccurredAt: oct, RecordedAt: oct, Snapshot: cases.Classification{Zone: "other", Department: "d", CaseType: "t"}}}
	actor := api.Actor{OrganizationID: org, UserID: "operator", SessionID: "s", MFA: true, Permissions: []string{"reports:operational", "reports:export"}, Grants: []api.Grant{{Permission: "reports:operational", UnitID: "u"}, {Permission: "reports:export", UnitID: "u"}}}
	sourceCalls := 0
	changeSource, changeActor := false, false
	refreshCalls := 0
	h := NewHTTPHandler(db, HTTPConfig{Enabled: true, MinGroup: 5, Clock: func() time.Time { return now }, RefreshActor: func(ctx context.Context, a api.Actor) (api.Actor, error) {
		refreshCalls++
		if changeActor && refreshCalls%2 == 0 {
			a.Grants = nil
		}
		return a, nil
	}, Source: func(context.Context, api.Actor, bool) ([]cases.ReportFact, error) {
		sourceCalls++
		if changeSource && sourceCalls%2 == 0 {
			return nil, nil
		}
		return events, nil
	}})
	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(api.WithActor(r.Context(), actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := "/api/extensions/reports/trend?end_month=2026-10&months=3&dimension=department"
	w := call(path)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct{ Data TrendReport }
	if e = json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if len(body.Data.Periods) != 3 || body.Data.Periods[1].NewCases != 1 || body.Data.Periods[2].NewCases != 0 || body.Data.Periods[2].Backlog != 1 || body.Data.Periods[1].Groups[0].Label != "original department" {
		t.Fatal("trend did not reconcile current-unit scope", w.Body.String())
	}
	w = call(path + "&zone=other")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"new_cases":0`) {
		t.Fatal("filter", w.Code, w.Body.String())
	}
	w = call("/api/extensions/reports/export?mode=trend&end_month=2026-10&months=3&dimension=zone")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "'=original") {
		t.Fatal("controlled export", w.Code, w.Body.String())
	}
	actor.Grants = actor.Grants[:1]
	w = call("/api/extensions/reports/export?mode=trend&end_month=2026-10&months=3&dimension=zone")
	if w.Code != 403 {
		t.Fatal("export scope not enforced", w.Code)
	}
	sourceCalls = 0
	changeSource = true
	w = call(path)
	if w.Code != 409 || strings.Contains(w.Body.String(), "original department") {
		t.Fatal("mid-request source leak", w.Code, w.Body.String())
	}
	changeSource = false
	refreshCalls = 0
	changeActor = true
	w = call(path)
	if w.Code != 403 {
		t.Fatal("mid-request access leak", w.Code)
	}
	actor.Permissions = []string{"reports:aggregate"}
	changeActor = false
	w = call(path)
	if w.Code != 403 {
		t.Fatal("leadership raw trends leaked", w.Code)
	}
}
