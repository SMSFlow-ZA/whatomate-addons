package reports

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "reports" }
func privacyKeys(org, id string) (string, string) {
	sum := sha256.Sum256([]byte(org + "\x00" + id))
	return id, hex.EncodeToString(sum[:])
}
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	if !a.Allows("reports:operational", s.UnitID) {
		return nil, nil
	}
	cs, e := cases.PrivacyCases(ctx, tx, a, s, subject)
	if e != nil {
		return nil, e
	}
	out := []governance.Record{}
	for _, c := range cs {
		raw, opaque := privacyKeys(a.OrganizationID, c.ID)
		var exists bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_report_events WHERE organization_id=$1 AND case_key IN($2,$3)) OR EXISTS(SELECT 1 FROM addon_report_activity WHERE organization_id=$1 AND payload->>'CaseKey' IN($2,$3))`, a.OrganizationID, raw, opaque).Scan(&exists)
		if e != nil {
			return nil, e
		}
		if exists {
			out = append(out, governance.Record{ID: "reports:" + c.ID, LineageID: "cases:" + c.ID, Scope: s, SubjectID: subject, Source: governance.Report, Reference: c.ID, CreatedAt: c.Events[0].RecordedAt, Generation: 1})
		}
	}
	return out, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	c, e := cases.PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return nil, e
	}
	if c.UnitID != r.Scope.UnitID || !a.Allows("reports:operational", c.UnitID) {
		return nil, governance.ErrDenied
	}
	x, y := privacyKeys(a.OrganizationID, c.ID)
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT jsonb_build_object('events',(SELECT COALESCE(jsonb_agg(payload),'[]') FROM addon_report_events WHERE organization_id=$1 AND case_key IN($2,$3)),'activity',(SELECT COALESCE(jsonb_agg(payload),'[]') FROM addon_report_activity WHERE organization_id=$1 AND payload->>'CaseKey' IN($2,$3)))`, a.OrganizationID, x, y).Scan(&raw)
	return raw, e
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	c, e := cases.PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return e
	}
	if c.UnitID != r.Scope.UnitID {
		return governance.ErrDenied
	}
	x, y := privacyKeys(a.OrganizationID, c.ID)
	for _, q := range []string{`DELETE FROM addon_report_events WHERE organization_id=$1 AND case_key IN($2,$3)`, `DELETE FROM addon_report_activity WHERE organization_id=$1 AND payload->>'CaseKey' IN($2,$3)`} {
		if _, e = tx.ExecContext(ctx, q, a.OrganizationID, x, y); e != nil {
			return e
		}
	}
	return nil
}
