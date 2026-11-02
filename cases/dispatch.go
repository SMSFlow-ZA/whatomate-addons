package cases

import (
	"context"
	"database/sql"
	"encoding/json"
)

func ListDispatch(ctx context.Context, db *sql.DB, p Principal, tenant string) ([]DispatchDTO, error) {
	rows, e := db.QueryContext(ctx, `SELECT payload FROM addon_cases_case WHERE organization_id=$1 ORDER BY id`, tenant)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DispatchDTO{}
	for rows.Next() {
		var raw []byte
		var c Case
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return nil, e
		}
		if CanDispatch(p, c) {
			out = append(out, DispatchView(c))
		}
	}
	return out, rows.Err()
}

// CanDispatch preserves the subject conflict check and exact unit assignment
// grant. It does not grant narrative or employee message access.
func CanDispatch(p Principal, c Case) bool {
	return !operatorConflict(p, c) && scoped(p, c, AssignTriage)
}

type DispatchDTO struct {
	ID          string       `json:"id"`
	UnitID      string       `json:"unit_id"`
	Version     uint64       `json:"version"`
	State       State        `json:"state"`
	Assignments []Assignment `json:"assignments"`
}

func DispatchView(c Case) DispatchDTO {
	return DispatchDTO{ID: c.ID, UnitID: c.UnitID, Version: c.Version, State: c.State, Assignments: append([]Assignment(nil), c.Assignments...)}
}

// ReadRouting is a trusted server port for recipient selection. The returned
// participant identifiers exist solely for candidate conflict checks; HTTP must
// serialize DispatchView, never this value. Narrative and event text are absent.
func (s Service) ReadRouting(ctx context.Context, p Principal, id string) (Case, error) {
	if s.Repository == nil {
		return Case{}, ErrInvalid
	}
	tx, e := s.Repository.Begin(ctx)
	if e != nil {
		return Case{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	c, e := tx.Load(ctx, id)
	if e != nil {
		return Case{}, ErrDenied
	}
	if !CanDispatch(p, c) && !CanWork(p, c) {
		return Case{}, ErrDenied
	}
	c.Narrative = ""
	c.Category = ""
	c.Snapshot = Classification{}
	c.Events = nil
	return c, nil
}
