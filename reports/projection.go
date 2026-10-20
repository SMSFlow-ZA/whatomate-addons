package reports

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"sort"
	"time"
)

// Store owns projections only. It never reads or writes another module's tables.
type Store struct{ DB *sql.DB }

func (s Store) Migrate(ctx context.Context) error {
	if s.DB == nil {
		return ErrDenied
	}
	_, e := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_report_events(organization_id text NOT NULL,mode text NOT NULL,case_key text NOT NULL,version bigint NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(organization_id,mode,case_key,version))`)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_report_activity(organization_id text NOT NULL,mode text NOT NULL,event_key text NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(organization_id,mode,event_key))`)
	return e
}

// Project accepts only facts returned by the case owner's current access policy.
// Stored history does not authorize access: only keys present in this invocation
// may be reconstructed. No tenant-wide projection read endpoint exists.
func (s Store) Project(ctx context.Context, a api.Actor, mode string, events []cases.ReportFact, asOf time.Time) (Facts, error) {
	if s.DB == nil || len(events) > 100000 || asOf.IsZero() {
		return Facts{}, ErrDenied
	}
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil {
		return Facts{}, ErrDenied
	}
	permission := "reports:" + mode
	if mode != "aggregate" && mode != "operational" {
		return Facts{}, ErrDenied
	}
	if !a.Has(permission) {
		return Facts{}, ErrDenied
	}
	keys := map[string]uint64{}
	for _, e := range events {
		if e.OrganizationID != a.OrganizationID || !a.Allows(permission, e.UnitID) || e.CaseID == "" || e.Version == 0 {
			return Facts{}, ErrDenied
		}
		if e.Version > keys[e.CaseID] {
			keys[e.CaseID] = e.Version
		}
	}
	if _, e := Reconstruct(events, asOf); e != nil {
		return Facts{}, e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Facts{}, e
	}
	defer tx.Rollback()
	for _, f := range events {
		body, e := json.Marshal(f)
		if e != nil {
			return Facts{}, e
		}
		result, e := tx.ExecContext(ctx, `INSERT INTO addon_report_events(organization_id,mode,case_key,version,payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,mode,case_key,version) DO UPDATE SET payload=EXCLUDED.payload WHERE addon_report_events.payload=EXCLUDED.payload`, a.OrganizationID, mode, f.CaseID, f.Version, body)
		if e != nil {
			return Facts{}, e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			return Facts{}, fmt.Errorf("projection history conflict")
		}
	}
	// Query by each authorized case key: no unbounded tenant scan and no stale
	// authorization inherited from a projection created by a different operator.
	all := []cases.ReportFact{}
	for key, maxVersion := range keys {
		rows, e := tx.QueryContext(ctx, `SELECT payload FROM addon_report_events WHERE organization_id=$1 AND mode=$2 AND case_key=$3 AND version<=$4 ORDER BY version`, a.OrganizationID, mode, key, maxVersion)
		if e != nil {
			return Facts{}, e
		}
		for rows.Next() {
			var raw []byte
			var f cases.ReportFact
			if e = rows.Scan(&raw); e != nil {
				rows.Close()
				return Facts{}, e
			}
			if e = json.Unmarshal(raw, &f); e != nil {
				rows.Close()
				return Facts{}, e
			}
			all = append(all, f)
			if len(all) > 100000 {
				rows.Close()
				return Facts{}, ErrDenied
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return Facts{}, e
		}
	}
	facts, e := Reconstruct(all, asOf)
	if e != nil {
		return Facts{}, e
	}
	if e = tx.Commit(); e != nil {
		return Facts{}, e
	}
	return facts, nil
}

// Reconstruct uses submitted case time and the latest known event version at
// the snapshot. Drafts are excluded. A later reopening clears resolution time.
// RecordedAt also bounds knowledge: a later backdated change does not rewrite
// what a previously captured snapshot knew.
func Reconstruct(events []cases.ReportFact, asOf time.Time) (Facts, error) {
	if asOf.IsZero() {
		return Facts{}, ErrDenied
	}
	groups := map[string][]cases.ReportFact{}
	for _, e := range events {
		if e.CaseID == "" || e.OrganizationID == "" || e.UnitID == "" || e.Version == 0 || e.OccurredAt.IsZero() || e.RecordedAt.IsZero() {
			return Facts{}, ErrDenied
		}
		key := e.OrganizationID + "\x00" + e.CaseID
		groups[key] = append(groups[key], e)
	}
	result := Facts{Cases: []CaseFact{}}
	for _, list := range groups {
		sort.Slice(list, func(i, j int) bool { return list[i].Version < list[j].Version })
		first := list[0]
		if first.Version != 1 {
			return Facts{}, ErrDenied
		}
		var previous uint64
		var created, resolved time.Time
		for _, e := range list {
			if e.Version != previous+1 || e.Snapshot != first.Snapshot || e.UnitID != first.UnitID {
				return Facts{}, ErrDenied
			}
			previous = e.Version
			if e.RecordedAt.After(asOf) || e.OccurredAt.After(asOf) {
				continue
			}
			if created.IsZero() && e.To != cases.Draft {
				created = e.OccurredAt
			}
			if !created.IsZero() {
				if e.To == cases.Resolved || e.To == cases.Closed {
					if resolved.IsZero() {
						resolved = e.OccurredAt
					}
				} else {
					resolved = time.Time{}
				}
			}
		}
		if !created.IsZero() {
			result.Cases = append(result.Cases, CaseFact{OrganizationID: first.OrganizationID, ID: first.CaseID, Zone: first.Snapshot.Zone, Department: first.Snapshot.Department, CaseType: first.Snapshot.CaseType, CreatedAt: created, ResolvedAt: resolved})
		}
	}
	sort.Slice(result.Cases, func(i, j int) bool { return result.Cases[i].ID < result.Cases[j].ID })
	return result, nil
}

// ParseWindow uses UTC calendar boundaries. Leadership reports accept only a
// completed single calendar month; custom cohorts/filter combinations are denied.
func ParseWindow(start, end, asOf string, k int, board bool, now time.Time) (Window, error) {
	s, e := time.Parse("2006-01-02", start)
	if e != nil {
		return Window{}, ErrDenied
	}
	finish, e := time.Parse("2006-01-02", end)
	if e != nil {
		return Window{}, ErrDenied
	}
	snapshot := now.UTC()
	if asOf == "" && !finish.After(now) {
		snapshot = finish
	}
	if asOf != "" {
		snapshot, e = time.Parse(time.RFC3339, asOf)
		if e != nil {
			return Window{}, ErrDenied
		}
	}
	w := Window{Start: s, End: finish, AsOf: snapshot, MinGroup: k}
	if !finish.After(s) || finish.Sub(s) > 366*24*time.Hour || snapshot.Before(s) || snapshot.After(now) || k < 2 || k > 100000 {
		return Window{}, ErrDenied
	}
	if board {
		if s.Day() != 1 || finish != s.AddDate(0, 1, 0) || finish.After(now) || asOf != "" {
			return Window{}, ErrDenied
		}
		w.AsOf = finish
	}
	return w, nil
}
