package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "ai" }
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	if !a.Has("ai:assist") {
		return nil, nil
	}
	cs, e := cases.PrivacyCases(ctx, tx, a, s, subject)
	if e != nil {
		return nil, e
	}
	out := []governance.Record{}
	for _, c := range cs {
		var exists bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_ai_records WHERE organization_id=$1 AND case_id=$2)`, a.OrganizationID, c.ID).Scan(&exists)
		if e != nil {
			return nil, e
		}
		if exists {
			candidate := governance.Record{Scope: s, Reference: c.ID}
			if _, e = (PrivacyAdapter{}).Export(ctx, tx, a, candidate); e != nil {
				if e == governance.ErrDenied {
					continue
				}
				return nil, e
			}
			out = append(out, governance.Record{ID: "ai:" + c.ID, LineageID: "cases:" + c.ID, Scope: s, SubjectID: subject, Source: governance.AssistedAnswer, Reference: c.ID, CreatedAt: c.Events[0].RecordedAt, Generation: 1})
		}
	}
	return out, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	c, e := cases.PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return nil, e
	}
	if c.UnitID != r.Scope.UnitID {
		return nil, governance.ErrDenied
	}
	rows, e := tx.QueryContext(ctx, `SELECT audience,payload FROM addon_ai_records WHERE organization_id=$1 AND case_id=$2 FOR UPDATE`, a.OrganizationID, c.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var audience string
		var raw []byte
		if e = rows.Scan(&audience, &raw); e != nil {
			return nil, e
		}
		if !a.AudienceAllowed("knowledge:read", audience) || !a.AudienceAllowed("ai:assist", audience) {
			return nil, governance.ErrDenied
		}
		out = append(out, raw)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return json.Marshal(out)
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	if _, e := (PrivacyAdapter{}).Export(ctx, tx, a, r); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `DELETE FROM addon_ai_audit WHERE organization_id=$1 AND record_id IN(SELECT id FROM addon_ai_records WHERE organization_id=$1 AND case_id=$2)`, a.OrganizationID, r.Reference); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM addon_ai_records WHERE organization_id=$1 AND case_id=$2`, a.OrganizationID, r.Reference)
	return e
}
