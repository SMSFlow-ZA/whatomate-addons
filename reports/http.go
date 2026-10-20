package reports

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"net/http"
	"sort"
	"strings"
	"time"
)

type HTTPConfig struct {
	Enabled  bool
	MinGroup int
	Clock    func() time.Time
	// RefreshActor must revalidate the live session, MFA and current exact grants.
	RefreshActor func(context.Context, api.Actor) (api.Actor, error)
	// Source is an optional trusted owner adapter. Nil uses the case module ports.
	Source func(context.Context, api.Actor, bool) ([]cases.ReportFact, error)
	// Activities is optional: nil reports explicitly unavailable service volumes.
	Activities func(context.Context, api.Actor, bool) ([]ActivityFact, error)
}
type httpHandler struct {
	store Store
	cfg   HTTPConfig
}

func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler {
	return &httpHandler{store: Store{DB: db}, cfg: cfg}
}
func (h *httpHandler) source(ctx context.Context, a api.Actor, board bool) ([]cases.ReportFact, error) {
	var f []cases.ReportFact
	var e error
	if h.cfg.Source != nil {
		f, e = h.cfg.Source(ctx, a, board)
	} else if board {
		f, e = cases.AggregateFacts(ctx, h.store.DB, a)
	} else {
		f, e = cases.ReportFacts(ctx, h.store.DB, a)
	}
	if e != nil {
		return nil, e
	}
	out := []cases.ReportFact{}
	p := "reports:operational"
	if board {
		p = "reports:aggregate"
	}
	for _, v := range f {
		if v.OrganizationID == a.OrganizationID && a.Allows(p, v.UnitID) {
			out = append(out, v)
		}
	}
	return out, nil
}
func digest(v any) [32]byte { b, _ := json.Marshal(v); return sha256.Sum256(b) }
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		api.Error(w, 404, "module_unavailable")
		return
	}
	if h.cfg.RefreshActor == nil || h.cfg.MinGroup < 2 || h.cfg.MinGroup > 100000 {
		api.Error(w, 503, "report_policy_unconfigured")
		return
	}
	actor, e := api.ActorFrom(r.Context())
	if e != nil {
		api.Error(w, 403, "report_access_denied")
		return
	}
	a, e := h.cfg.RefreshActor(r.Context(), actor)
	if e != nil || a.OrganizationID != actor.OrganizationID || a.UserID != actor.UserID || a.SessionID != actor.SessionID {
		api.Error(w, 403, "report_access_denied")
		return
	}
	if _, e = api.ActorFrom(api.WithActor(r.Context(), a)); e != nil {
		api.Error(w, 403, "report_access_denied")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/extensions/reports"), "/")
	q := r.URL.Query()
	export := path == "export"
	mode := path
	if export {
		mode = q.Get("mode")
	}
	if mode == "trend" {
		h.serveTrend(w, r, a, export)
		return
	}
	board := mode == "board"
	if r.Method != "GET" || (!board && mode != "operational") {
		api.Error(w, 404, "report_unavailable")
		return
	}
	permission := "reports:operational"
	projectionMode := "operational"
	if board {
		permission = "reports:aggregate"
		projectionMode = "aggregate"
	}
	if !a.Has(permission) || (export && !a.Has("reports:export")) {
		api.Error(w, 403, "report_access_denied")
		return
	}
	for key := range q {
		if key != "start" && key != "end" && key != "as_of" && key != "zone" && key != "department" && key != "case_type" && !(export && key == "mode") {
			api.Error(w, 400, "invalid_report_window")
			return
		}
		if len(q[key]) != 1 {
			api.Error(w, 400, "invalid_report_window")
			return
		}
	}
	if board && (q.Get("zone") != "" || q.Get("department") != "" || q.Get("case_type") != "") {
		api.Error(w, 400, "board_cohort_filters_denied")
		return
	}
	now := time.Now().UTC()
	if h.cfg.Clock != nil {
		now = h.cfg.Clock().UTC()
	}
	window, e := ParseWindow(q.Get("start"), q.Get("end"), q.Get("as_of"), h.cfg.MinGroup, board, now)
	if e != nil {
		api.Error(w, 400, "invalid_report_window")
		return
	}
	events, e := h.source(r.Context(), a, board)
	if e != nil {
		api.Error(w, 503, "report_source_unavailable")
		return
	}
	// Export scopes must explicitly match every source unit. A global export
	// permission alone cannot widen the report's scope.
	if export {
		for _, f := range events {
			if !a.Allows("reports:export", f.UnitID) {
				api.Error(w, 403, "report_access_denied")
				return
			}
		}
	}
	var activities []ActivityFact
	var activitySourceDigest [32]byte
	if h.cfg.Activities != nil {
		activities, e = h.cfg.Activities(r.Context(), a, board)
		if e != nil {
			api.Error(w, 503, "report_activity_unavailable")
			return
		}
		if export {
			for _, f := range activities {
				if !a.Allows("reports:export", f.UnitID) {
					api.Error(w, 403, "report_access_denied")
					return
				}
			}
		}
		activitySourceDigest = digest(activities)
		activities, e = h.store.projectActivity(r.Context(), a, projectionMode, activities)
		if e != nil {
			api.Error(w, 503, "report_activity_projection_unavailable")
			return
		}
	}
	snapshot := window.AsOf
	if snapshot.Equal(window.End) {
		snapshot = snapshot.Add(-time.Nanosecond)
	}
	facts, e := h.store.Project(r.Context(), a, projectionMode, events, snapshot)
	if e != nil {
		api.Error(w, 503, "report_projection_unavailable")
		return
	}
	filtered := Facts{Cases: []CaseFact{}}
	for _, f := range facts.Cases {
		if (q.Get("zone") == "" || f.Zone == q.Get("zone")) && (q.Get("department") == "" || f.Department == q.Get("department")) && (q.Get("case_type") == "" || f.CaseType == q.Get("case_type")) {
			filtered.Cases = append(filtered.Cases, f)
		}
	}
	reportActor := Actor{OrganizationID: a.OrganizationID, Permissions: a.Permissions}
	var result any
	if board {
		result, e = Board(reportActor, filtered, window)
	} else {
		result, e = Operational(reportActor, filtered, window, func(CaseFact) bool { return true })
	}
	if e != nil {
		api.Error(w, 503, "report_projection_unavailable")
		return
	}
	var service *ServiceReport
	if h.cfg.Activities != nil {
		v := serviceReport(activities, window, board, q.Get("zone"), q.Get("department"), q.Get("case_type"))
		service = &v
		if board {
			v := result.(BoardReport)
			if v.Suppressed || service.Suppressed {
				suppressBoard(&v)
				suppressService(service)
			} else {
				v.Messages = service.Inbound
				v.Enquiries = service.Enquiries
			}
			result = v
		} else {
			v := result.(Report)
			v.Messages = *service.Inbound
			v.Enquiries = *service.Enquiries
			result = v
		}
	}
	// Reject changes during calculation, including assignment/subject changes that
	// leave host role grants unchanged. The caller can safely refresh the report.
	fresh, e := h.cfg.RefreshActor(r.Context(), a)
	if e != nil || digest(fresh) != digest(a) {
		api.Error(w, 403, "report_access_changed")
		return
	}
	current, e := h.source(r.Context(), fresh, board)
	if e != nil || digest(current) != digest(events) {
		api.Error(w, 409, "report_source_changed")
		return
	}
	if h.cfg.Activities != nil {
		currentActivity, e := h.cfg.Activities(r.Context(), fresh, board)
		if e != nil || digest(currentActivity) != activitySourceDigest {
			api.Error(w, 409, "report_source_changed")
			return
		}
	}
	metadata := map[string]any{"start": window.Start, "end": window.End, "as_of": window.AsOf, "scope": "current authorized cases; creation-time dimensions", "timezone": "UTC", "service_metrics": map[string]any{"available": false, "reason": "message/enquiry source not connected"}}
	if service != nil {
		metadata["service_metrics"] = map[string]any{"available": true, "scope": "current authorized unit and case activity; outbound is queued, not delivered"}
	}
	if export {
		writeCSV(w, result, window, service)
		return
	}
	api.Write(w, 200, map[string]any{"report": result, "metadata": metadata, "service": service})
}
func csvCell(s string) string {
	if strings.ContainsAny(strings.TrimLeft(s, " "), "\x00") {
		return "'" + strings.ReplaceAll(s, "\x00", "")
	}
	t := strings.TrimLeft(s, " ")
	if t != "" && strings.ContainsRune("=+-@\t\r\n", rune(t[0])) {
		return "'" + s
	}
	return s
}
func writeCSV(w http.ResponseWriter, result any, window Window, service *ServiceReport) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="case-report.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write([]string{"section", "label", "value", "status"})
	_ = writer.Write([]string{"window", window.Start.Format(time.RFC3339), window.End.Format(time.RFC3339), "as of " + window.AsOf.Format(time.RFC3339)})
	metric := func(label string, n *int) {
		value, status := "", "suppressed"
		if n != nil {
			value = fmt.Sprint(*n)
			status = "available"
		}
		_ = writer.Write([]string{"metric", label, value, status})
	}
	switch v := result.(type) {
	case Report:
		metric("new_cases", &v.NewCases)
		metric("resolved_created_in_period", &v.ResolvedCreatedInPeriod)
		metric("backlog", &v.Backlog)
		for _, table := range []struct {
			name   string
			values map[string]int
		}{{"zone", v.ByZone}, {"department", v.ByDepartment}, {"case_type", v.ByCaseType}} {
			keys := []string{}
			for k := range table.values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				_ = writer.Write([]string{table.name, csvCell(k), fmt.Sprint(table.values[k]), "available"})
			}
		}
	case BoardReport:
		metric("new_cases", v.NewCases)
		metric("resolved_created_in_period", v.Resolved)
		metric("backlog", v.Backlog)
		for _, table := range []struct {
			name  string
			cells []Cell
		}{{"zone", v.ByZone}, {"department", v.ByDepartment}, {"case_type", v.ByCaseType}} {
			for _, cell := range table.cells {
				value, status := "", "suppressed"
				if cell.Value != nil {
					value = fmt.Sprint(*cell.Value)
					status = "available"
				}
				_ = writer.Write([]string{table.name, csvCell(cell.Label), value, status})
			}
		}
	}
	if service == nil {
		_ = writer.Write([]string{"service", "messages/enquiries", "", "source not connected"})
	} else {
		metric("inbound_received", service.Inbound)
		metric("outbound_queued", service.OutboundQueued)
		metric("enquiries", service.Enquiries)
		metric("handoffs", service.Handoffs)
		for _, table := range []struct {
			name  string
			cells []Cell
		}{{"service_zone", service.ByZone}, {"service_department", service.ByDepartment}, {"service_case_type", service.ByCaseType}} {
			for _, cell := range table.cells {
				value, status := "", "suppressed"
				if cell.Value != nil {
					value = fmt.Sprint(*cell.Value)
					status = "available"
				}
				_ = writer.Write([]string{table.name, csvCell(cell.Label), value, status})
			}
		}
	}
}
