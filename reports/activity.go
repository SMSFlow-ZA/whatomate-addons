package reports

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"time"
)

// ActivityFact contains no message content or person identifiers. The owning
// module must apply current linked-case/subject policy before returning it.
type ActivityFact struct {
	ID, OrganizationID, UnitID, CaseKey, Zone, Department, CaseType, Kind, Outcome string
	OccurredAt                                                                     time.Time
}
type ServiceReport struct {
	Inbound        *int   `json:"inbound_received"`
	OutboundQueued *int   `json:"outbound_queued"`
	Enquiries      *int   `json:"enquiries"`
	Handoffs       *int   `json:"handoffs"`
	ByZone         []Cell `json:"by_zone"`
	ByDepartment   []Cell `json:"by_department"`
	ByCaseType     []Cell `json:"by_case_type"`
	Suppressed     bool   `json:"suppressed"`
}

func (s Store) projectActivity(ctx context.Context, a api.Actor, mode string, events []ActivityFact) ([]ActivityFact, error) {
	if s.DB == nil || len(events) > 100000 {
		return nil, ErrDenied
	}
	p := "reports:" + mode
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	result := []ActivityFact{}
	seen := map[string]bool{}
	for _, f := range events {
		if f.OrganizationID != a.OrganizationID || !a.Allows(p, f.UnitID) || f.ID == "" || f.OccurredAt.IsZero() || !validActivity(f) {
			return nil, ErrDenied
		}
		b, e := json.Marshal(f)
		if e != nil {
			return nil, e
		}
		r, e := tx.ExecContext(ctx, `INSERT INTO addon_report_activity(organization_id,mode,event_key,payload) VALUES($1,$2,$3,$4) ON CONFLICT(organization_id,mode,event_key) DO UPDATE SET payload=EXCLUDED.payload WHERE addon_report_activity.payload=EXCLUDED.payload`, a.OrganizationID, mode, f.ID, b)
		if e != nil {
			return nil, e
		}
		n, e := r.RowsAffected()
		if e != nil || n != 1 {
			return nil, ErrDenied
		}
		if !seen[f.ID] {
			result = append(result, f)
			seen[f.ID] = true
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return result, nil
}
func validActivity(f ActivityFact) bool {
	return f.Kind == "inbound" && f.Outcome == "received" || f.Kind == "outbound" && f.Outcome == "queued" || f.Kind == "enquiry" || f.Kind == "handoff" && f.Outcome == "handed_off"
}
func serviceReport(events []ActivityFact, w Window, board bool, zone, department, caseType string) ServiceReport {
	in, out, enquiries, handoffs := 0, 0, 0, 0
	z, d, c := map[string]int{}, map[string]int{}, map[string]int{}
	for _, f := range events {
		if !during(f.OccurredAt, w) || (zone != "" && f.Zone != zone) || (department != "" && f.Department != department) || (caseType != "" && f.CaseType != caseType) {
			continue
		}
		switch f.Kind {
		case "inbound":
			in++
			z[f.Zone]++
			d[f.Department]++
			c[f.CaseType]++
		case "outbound":
			out++
		case "enquiry":
			enquiries++
		case "handoff":
			handoffs++
		}
	}
	k := 1
	if board {
		k = w.MinGroup
	}
	zs, zsmall := cells(z, k)
	ds, dsmall := cells(d, k)
	cs, csmall := cells(c, k)
	r := ServiceReport{Inbound: &in, OutboundQueued: &out, Enquiries: &enquiries, Handoffs: &handoffs, ByZone: zs, ByDepartment: ds, ByCaseType: cs, Suppressed: zsmall || dsmall || csmall}
	if board {
		for _, n := range []int{in, out, enquiries, handoffs} {
			if n > 0 && n < w.MinGroup {
				r.Suppressed = true
			}
		}
		if r.Suppressed {
			suppressService(&r)
		}
	}
	return r
}
func suppressService(r *ServiceReport) {
	r.Suppressed = true
	r.Inbound = nil
	r.OutboundQueued = nil
	r.Enquiries = nil
	r.Handoffs = nil
	for _, table := range [][]Cell{r.ByZone, r.ByDepartment, r.ByCaseType} {
		for i := range table {
			table[i].Value = nil
			table[i].Suppressed = true
		}
	}
}
func suppressBoard(r *BoardReport) {
	r.Suppressed = true
	r.NewCases = nil
	r.Backlog = nil
	r.Resolved = nil
	r.Messages = nil
	r.Enquiries = nil
	for _, table := range [][]Cell{r.ByZone, r.ByDepartment, r.ByCaseType} {
		for i := range table {
			table[i].Value = nil
			table[i].Suppressed = true
		}
	}
}
