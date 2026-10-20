package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"time"
)

type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "conversation" }
func privacyTurns(ctx context.Context, tx *sql.Tx, a api.Actor, unit, subject string) ([]json.RawMessage, error) {
	if !a.Allows("conversation:privacy", unit) {
		return nil, governance.ErrDenied
	}
	ids, _ := json.Marshal(governance.SubjectIDs(ctx, subject))
	rows, e := tx.QueryContext(ctx, `SELECT response FROM addon_conversation_inbound WHERE tenant=$1 AND ($2::jsonb ? (response->>'employee_id')) UNION ALL SELECT payload FROM addon_conversation_turn WHERE tenant=$1 AND ($2::jsonb ? (payload->>'employee_id'))`, a.OrganizationID, ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		var t Turn
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &t); e != nil {
			return nil, e
		}
		if t.IntakeSnapshot.Zone != "" && t.IntakeSnapshot.Zone != unit {
			return nil, governance.ErrDenied
		}
		out = append(out, raw)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	// Close rows before owner lookups; all linked cases require current access.
	rows.Close()
	for _, raw := range out {
		var t Turn
		_ = json.Unmarshal(raw, &t)
		for _, id := range t.Cases {
			c, e := cases.PrivacyCase(ctx, tx, a, id)
			if e != nil {
				return nil, e
			}
			if c.UnitID != unit {
				return nil, governance.ErrDenied
			}
		}
	}
	return out, nil
}
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	if a.OrganizationID != s.OrganizationID || !a.Allows("conversation:privacy", s.UnitID) {
		return nil, nil
	}
	turns, e := privacyTurns(ctx, tx, a, s.UnitID, subject)
	if e != nil {
		return nil, e
	}
	if len(turns) == 0 {
		return nil, nil
	}
	id := "conversation:" + subject
	records := []governance.Record{{ID: id, LineageID: id, Scope: s, SubjectID: subject, Source: governance.Conversation, Reference: subject, CreatedAt: time.Now().UTC(), Generation: 1}}
	external := false
	var created time.Time
	for _, subjectID := range governance.SubjectIDs(ctx, subject) {
		inventory, err := delivery.EmployeePrivacyInventory(ctx, tx, a.OrganizationID, subjectID)
		if err != nil {
			return nil, err
		}
		// A delivery linked to a case remains subject to the case owner's current
		// ordinary read policy as well as the exact conversation privacy scope.
		for _, caseID := range inventory.CaseIDs {
			c, err := cases.PrivacyCase(ctx, tx, a, caseID)
			if err != nil {
				return nil, err
			}
			if c.UnitID != s.UnitID {
				return nil, governance.ErrDenied
			}
		}
		external = external || inventory.PossibleExternal
		if !inventory.CreatedAt.IsZero() && (created.IsZero() || inventory.CreatedAt.Before(created)) {
			created = inventory.CreatedAt
		}
	}
	if external {
		records = append(records, governance.Record{ID: id + ":transport", LineageID: id, Scope: s, SubjectID: subject, Source: governance.Provider, Reference: subject, CreatedAt: created, Generation: 1})
	}
	return records, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	turns, e := privacyTurns(ctx, tx, a, r.Scope.UnitID, r.SubjectID)
	if e != nil {
		return nil, e
	}
	if r.Source == governance.Provider {
		return json.Marshal(map[string]string{"state": "awaiting_external_expiry"})
	}
	return json.Marshal(turns)
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	turns, e := privacyTurns(ctx, tx, a, r.Scope.UnitID, r.SubjectID)
	if e != nil {
		return e
	}
	if r.Source == governance.Provider {
		return governance.ErrExternalPending
	}
	for _, raw := range turns {
		var t Turn
		_ = json.Unmarshal(raw, &t)
		for _, id := range t.Cases {
			if _, e = tx.ExecContext(ctx, `DELETE FROM addon_conversation_activity WHERE tenant=$1 AND case_id=$2`, a.OrganizationID, id); e != nil {
				return e
			}
		}
	}
	for _, subjectID := range governance.SubjectIDs(ctx, r.SubjectID) {
		if e = delivery.EraseEmployee(ctx, tx, a.OrganizationID, subjectID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM addon_conversation_timer WHERE tenant=$1 AND employee_id=$2`, a.OrganizationID, subjectID); e != nil {
			return e
		}
	}
	ids, _ := json.Marshal(governance.SubjectIDs(ctx, r.SubjectID))
	for _, q := range []string{`DELETE FROM addon_conversation_inbound WHERE tenant=$1 AND ($2::jsonb ? (response->>'employee_id'))`, `DELETE FROM addon_conversation_turn WHERE tenant=$1 AND ($2::jsonb ? (payload->>'employee_id'))`} {
		if _, e = tx.ExecContext(ctx, q, a.OrganizationID, ids); e != nil {
			return e
		}
	}
	return nil
}
