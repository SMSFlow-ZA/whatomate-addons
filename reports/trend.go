package reports

import (
	"encoding/csv"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"net/http"
	"strconv"
	"time"
)

// TrendQuery defines disjoint UTC month cohorts ending in EndMonth (inclusive).
type TrendQuery struct {
	EndMonth                              string
	Months                                int
	Dimension, Zone, Department, CaseType string
}
type TrendPeriod struct {
	Month                   string `json:"month"`
	NewCases                int    `json:"new_cases"`
	ResolvedCreatedInPeriod int    `json:"resolved_created_in_period"`
	Backlog                 int    `json:"backlog"`
	Partial                 bool   `json:"partial"`
	Groups                  []Cell `json:"groups"`
}
type TrendReport struct {
	Dimension string        `json:"dimension"`
	AsOf      time.Time     `json:"as_of"`
	Timezone  string        `json:"timezone"`
	Scope     string        `json:"scope"`
	Periods   []TrendPeriod `json:"periods"`
}

func BuildTrend(org string, events []cases.ReportFact, q TrendQuery, now time.Time) (TrendReport, error) {
	end, e := time.Parse("2006-01", q.EndMonth)
	if e != nil || q.Months < 1 || q.Months > 12 || end.After(now.UTC()) || (q.Dimension != "zone" && q.Dimension != "department" && q.Dimension != "case_type") {
		return TrendReport{}, ErrDenied
	}
	r := TrendReport{Dimension: q.Dimension, AsOf: now.UTC(), Timezone: "UTC", Scope: "current authorized cases; immutable creation-time dimensions; case counts only", Periods: []TrendPeriod{}}
	for i := q.Months - 1; i >= 0; i-- {
		start := end.AddDate(0, -i, 0)
		finish := start.AddDate(0, 1, 0)
		snapshot := finish.Add(-time.Nanosecond)
		partial := finish.After(now)
		if partial {
			snapshot = now.UTC()
		}
		facts, e := Reconstruct(events, snapshot)
		if e != nil {
			return TrendReport{}, e
		}
		filtered := Facts{Cases: []CaseFact{}}
		for _, c := range facts.Cases {
			if c.OrganizationID != org {
				return TrendReport{}, ErrDenied
			}
			if (q.Zone == "" || c.Zone == q.Zone) && (q.Department == "" || c.Department == q.Department) && (q.CaseType == "" || c.CaseType == q.CaseType) {
				filtered.Cases = append(filtered.Cases, c)
			}
		}
		result, e := Operational(Actor{OrganizationID: org, Permissions: []string{"reports:operational"}}, filtered, Window{Start: start, End: finish, AsOf: snapshot, MinGroup: 2}, func(CaseFact) bool { return true })
		if e != nil {
			return TrendReport{}, e
		}
		groups := result.ByZone
		if q.Dimension == "department" {
			groups = result.ByDepartment
		} else if q.Dimension == "case_type" {
			groups = result.ByCaseType
		}
		groupCells, _ := cells(groups, 1)
		r.Periods = append(r.Periods, TrendPeriod{Month: start.Format("2006-01"), NewCases: result.NewCases, ResolvedCreatedInPeriod: result.ResolvedCreatedInPeriod, Backlog: result.Backlog, Partial: partial, Groups: groupCells})
	}
	return r, nil
}
func (h *httpHandler) serveTrend(w http.ResponseWriter, r *http.Request, a api.Actor, export bool) {
	if r.Method != "GET" || !a.Has("reports:operational") || (export && !a.Has("reports:export")) {
		api.Error(w, 403, "report_access_denied")
		return
	}
	values := r.URL.Query()
	for key, list := range values {
		if len(list) != 1 || (key != "end_month" && key != "months" && key != "dimension" && key != "zone" && key != "department" && key != "case_type" && !(export && key == "mode")) {
			api.Error(w, 400, "invalid_trend_window")
			return
		}
	}
	months, e := strconv.Atoi(values.Get("months"))
	if e != nil {
		api.Error(w, 400, "invalid_trend_window")
		return
	}
	q := TrendQuery{EndMonth: values.Get("end_month"), Months: months, Dimension: values.Get("dimension"), Zone: values.Get("zone"), Department: values.Get("department"), CaseType: values.Get("case_type")}
	now := time.Now().UTC()
	if h.cfg.Clock != nil {
		now = h.cfg.Clock().UTC()
	}
	if _, e = BuildTrend(a.OrganizationID, nil, q, now); e != nil {
		api.Error(w, 400, "invalid_trend_window")
		return
	}
	events, e := h.source(r.Context(), a, false)
	if e != nil {
		api.Error(w, 503, "report_source_unavailable")
		return
	}
	if export {
		for _, f := range events {
			if !a.Allows("reports:export", f.UnitID) {
				api.Error(w, 403, "report_access_denied")
				return
			}
		}
	}
	// Persist/validate the same owner facts; reconstruction below uses frozen
	// owner events only and cannot discover stale keys from old projections.
	if _, e = h.store.Project(r.Context(), a, "operational", events, now); e != nil {
		api.Error(w, 503, "report_projection_unavailable")
		return
	}
	result, e := BuildTrend(a.OrganizationID, events, q, now)
	if e != nil {
		api.Error(w, 503, "report_projection_unavailable")
		return
	}
	fresh, e := h.cfg.RefreshActor(r.Context(), a)
	if e != nil || digest(fresh) != digest(a) {
		api.Error(w, 403, "report_access_changed")
		return
	}
	current, e := h.source(r.Context(), fresh, false)
	if e != nil || digest(current) != digest(events) {
		api.Error(w, 409, "report_source_changed")
		return
	}
	if export {
		writeTrendCSV(w, result)
		return
	}
	api.Write(w, 200, result)
}
func writeTrendCSV(w http.ResponseWriter, r TrendReport) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="case-trend.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write([]string{"month", "dimension", "group", "new_cases", "resolved_created_in_period", "backlog", "partial", "as_of_utc"})
	for _, p := range r.Periods {
		_ = writer.Write([]string{p.Month, "total", "", fmt.Sprint(p.NewCases), fmt.Sprint(p.ResolvedCreatedInPeriod), fmt.Sprint(p.Backlog), fmt.Sprint(p.Partial), r.AsOf.Format(time.RFC3339)})
		for _, c := range p.Groups {
			_ = writer.Write([]string{p.Month, r.Dimension, csvCell(c.Label), fmt.Sprint(*c.Value), "", "", fmt.Sprint(p.Partial), r.AsOf.Format(time.RFC3339)})
		}
	}
}
