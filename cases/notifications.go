package cases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// NotificationScope is an explicit trusted background-service capability. Hosts
// construct it from reviewed configuration, never HTTP input or a fake session.
type NotificationScope struct {
	OrganizationID string
	UnitIDs        []string
}
type NotificationCursor struct {
	CaseID  string
	Version uint64
}

// EscalationFact contains no narrative, category, participants or actor evidence.
type EscalationFact struct {
	OrganizationID, UnitID, CaseID, HandlerID string
	Version                                   uint64
	RecordedAt                                time.Time
}

func (s NotificationScope) valid() bool {
	if s.OrganizationID == "" || len(s.UnitIDs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, u := range s.UnitIDs {
		if u == "" || u == "*" || strings.TrimSpace(u) != u || seen[u] {
			return false
		}
		seen[u] = true
	}
	return true
}
func (s NotificationScope) allows(org, unit string) bool {
	if !s.valid() || org != s.OrganizationID {
		return false
	}
	for _, u := range s.UnitIDs {
		if u == unit {
			return true
		}
	}
	return false
}

// EscalationFacts reads committed owner facts in the caller's projection
// transaction. Consumers must rescan from the beginning after a bounded pass:
// sorting by event time/sequence alone can miss transactions committed late.
func EscalationFacts(ctx context.Context, tx *sql.Tx, scope NotificationScope, since time.Time, after NotificationCursor, limit int) ([]EscalationFact, error) {
	if tx == nil || !scope.valid() || since.IsZero() || limit < 1 || limit > 100 {
		return nil, ErrDenied
	}
	rows, e := tx.QueryContext(ctx, `SELECT case_id,version,organization_id,unit_id,recorded_at,payload->'Assignments' FROM addon_cases_event WHERE organization_id=$1 AND unit_id=ANY($2) AND recorded_at >= $3 AND payload->>'Kind'='escalation' AND (case_id,version)>($4,$5) ORDER BY case_id,version LIMIT $6`, scope.OrganizationID, scope.UnitIDs, since, after.CaseID, after.Version, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []EscalationFact{}
	for rows.Next() {
		var f EscalationFact
		var raw []byte
		var a []Assignment
		if e = rows.Scan(&f.CaseID, &f.Version, &f.OrganizationID, &f.UnitID, &f.RecordedAt, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &a); e != nil {
			return nil, e
		}
		if len(a) == 1 && (a[0].Role == Handler || a[0].Role == Supervisor) {
			f.HandlerID = a[0].ID
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CanNotifyHandler is narrower than CanRead: own-case/triage grants and alias
// assignments never grant a background notice to an unassigned host user.
func CanNotifyHandler(p Principal, c Case) bool {
	if c.State == Draft || c.State == Resolved || c.State == Closed || subject(p, c) || !scoped(p, c, WorkAssigned) {
		return false
	}
	for _, a := range c.Assignments {
		if a.ID == p.ID && p.ID != "" && (a.Role == Handler || a.Role == Supervisor) {
			return true
		}
	}
	return false
}

// CurrentNotification rechecks current authority and the immutable source event
// without exporting case content. Event version must still identify this user.
func CurrentNotification(ctx context.Context, db *sql.DB, scope NotificationScope, p Principal, id string, version uint64) (EscalationFact, error) {
	if db == nil || !scope.valid() || id == "" || version == 0 {
		return EscalationFact{}, ErrDenied
	}
	var raw, event []byte
	e := db.QueryRowContext(ctx, `SELECT c.payload,e.payload FROM addon_cases_case c JOIN addon_cases_event e ON e.case_id=c.id WHERE c.organization_id=$1 AND c.id=$2 AND e.version=$3`, scope.OrganizationID, id, version).Scan(&raw, &event)
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return EscalationFact{}, ErrDenied
		}
		return EscalationFact{}, e
	}
	var c Case
	var v Event
	if json.Unmarshal(raw, &c) != nil || json.Unmarshal(event, &v) != nil || !scope.allows(c.OrganizationID, c.UnitID) || !CanNotifyHandler(p, c) || v.Kind != "escalation" || len(v.Assignments) != 1 || v.Assignments[0].ID != p.ID || (v.Assignments[0].Role != Handler && v.Assignments[0].Role != Supervisor) {
		return EscalationFact{}, ErrDenied
	}
	return EscalationFact{OrganizationID: c.OrganizationID, UnitID: c.UnitID, CaseID: c.ID, HandlerID: p.ID, Version: version, RecordedAt: v.RecordedAt}, nil
}
