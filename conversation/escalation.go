package conversation

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
)

// EscalateDue uses the cases owner's conflict-aware resolver and audited mutation.
// No case table is read or mutated by this package. Pending timers survive chat restarts.
func (s Service) EscalateDue(ctx context.Context) (int, error) {
	if s.DB == nil || s.Config.Clock == nil || s.Config.ResolveCandidates == nil || s.Config.LoadEscalationCase == nil {
		return 0, ErrDenied
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT tenant,case_id,employee_id,sender,unit_id FROM addon_conversation_timer WHERE state='pending' AND due_at<=$1 ORDER BY due_at LIMIT 100`, s.Config.Clock())
	if e != nil {
		return 0, e
	}
	type timer struct{ tenant, id, employee, sender, unit string }
	var timers []timer
	for rows.Next() {
		var t timer
		if e = rows.Scan(&t.tenant, &t.id, &t.employee, &t.sender, &t.unit); e != nil {
			rows.Close()
			return 0, e
		}
		timers = append(timers, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	service := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	for _, t := range timers {
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return count, e
		}
		var state string
		e = tx.QueryRowContext(ctx, `SELECT state FROM addon_conversation_timer WHERE case_id=$1 FOR UPDATE SKIP LOCKED`, t.id).Scan(&state)
		if e == sql.ErrNoRows {
			tx.Rollback()
			continue
		}
		if e != nil {
			tx.Rollback()
			return count, e
		}
		if state != "pending" {
			tx.Rollback()
			continue
		}
		c, e := s.Config.LoadEscalationCase(ctx, t.tenant, t.unit, t.id)
		if e != nil {
			tx.Rollback()
			continue
		}
		if c.ID != t.id || c.OrganizationID != t.tenant || c.UnitID != t.unit {
			tx.Rollback()
			continue
		}
		if c.State == cases.Resolved || c.State == cases.Closed {
			state = "complete"
		} else {
			actor, candidates, err := s.Config.ResolveCandidates(ctx, c)
			if err != nil {
				tx.Rollback()
				continue
			}
			// Persist uncertainty before crossing the case owner's transaction boundary.
			// A crash after successful mutation cannot trigger a second handoff.
			if _, err = tx.ExecContext(ctx, `UPDATE addon_conversation_timer SET state='held_unknown' WHERE case_id=$1`, t.id); err != nil {
				tx.Rollback()
				return count, err
			}
			if err = tx.Commit(); err != nil {
				return count, err
			}
			_, err = service.Escalate(ctx, actor, c.ID, c.Version, candidates, "conversation escalation timer due", s.Config.Clock())
			if err != nil {
				state = "held"
				if errors.Is(err, cases.ErrCommitUnknown) {
					state = "held_unknown"
				}
				if errors.Is(err, cases.ErrConflict) {
					state = "pending"
				}
				if _, e = s.DB.ExecContext(ctx, `UPDATE addon_conversation_timer SET state=$2 WHERE case_id=$1`, t.id, state); e != nil {
					return count, e
				}
				continue
			}
			tx, e = s.DB.BeginTx(ctx, nil)
			if e != nil {
				return count, e
			}
			fact := ActivityFact{ID: digest(t.tenant + "|timer|" + t.id), OrganizationID: t.tenant, UnitID: c.UnitID, Zone: c.Snapshot.Zone, Department: c.Snapshot.Department, CaseType: c.Snapshot.CaseType, Kind: "handoff", Outcome: "handed_off", OccurredAt: s.Config.Clock()}
			if e = recordActivity(ctx, tx, fact, c.ID); e != nil {
				tx.Rollback()
				return count, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE addon_conversation_timer SET state='handed_off' WHERE case_id=$1`, t.id); e != nil {
				tx.Rollback()
				return count, e
			}
			if e = tx.Commit(); e != nil {
				return count, e
			}
			count++
			continue
		}
		_, e = tx.ExecContext(ctx, `UPDATE addon_conversation_timer SET state=$2 WHERE case_id=$1`, t.id, state)
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e != nil {
			return count, e
		}
		count++
	}
	return count, nil
}
