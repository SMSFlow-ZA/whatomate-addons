package carrier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"time"
)

// PrivacyAdapter owns ingress and receipt copies, retaining replay tombstones.
type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "carrier" }
func carrierScope(a api.Actor, s governance.Scope) bool {
	return a.OrganizationID == s.OrganizationID && a.Allows("conversation:privacy", s.UnitID)
}

type privacyRoute struct{ id, caseID string }

func privacyRoutes(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]privacyRoute, error) {
	if !carrierScope(a, s) {
		return nil, governance.ErrDenied
	}
	ids, _ := json.Marshal(governance.SubjectIDs(ctx, subject))
	rows, e := tx.QueryContext(ctx, `SELECT id,case_id FROM addon_carrier_route WHERE tenant=$1 AND unit_id=$2 AND $3::jsonb ? employee_id ORDER BY id FOR UPDATE`, a.OrganizationID, s.UnitID, ids)
	if e != nil {
		return nil, e
	}
	var out []privacyRoute
	for rows.Next() {
		var r privacyRoute
		if e = rows.Scan(&r.id, &r.caseID); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, r := range out {
		if r.caseID != "" {
			c, e := cases.PrivacyCase(ctx, tx, a, r.caseID)
			if e != nil {
				return nil, e
			}
			if c.UnitID != s.UnitID {
				return nil, governance.ErrDenied
			}
		}
	}
	return out, nil
}
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	if !carrierScope(a, s) {
		return nil, nil
	}
	routes, e := privacyRoutes(ctx, tx, a, s, subject)
	if e != nil {
		return nil, e
	}
	ids, _ := json.Marshal(governance.SubjectIDs(ctx, subject))
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM addon_carrier_ingress WHERE tenant=$1 AND unit_id=$2 AND $3::jsonb ? employee_id AND state<>'erased'`, a.OrganizationID, s.UnitID, ids).Scan(&count); e != nil {
		return nil, e
	}
	if count == 0 && len(routes) == 0 {
		return nil, nil
	}
	id := "carrier:" + subject
	records := []governance.Record{{ID: id, LineageID: id, Scope: s, SubjectID: subject, Source: governance.Conversation, Reference: subject, CreatedAt: time.Now().UTC(), Generation: 1}}
	external := false
	for _, r := range routes {
		_, possible, e := delivery.ReadPrivacyIntent(ctx, tx, a.OrganizationID, r.id)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		external = external || possible
	}
	if external {
		records = append(records, governance.Record{ID: id + ":transport", LineageID: id, Scope: s, SubjectID: subject, Source: governance.Provider, Reference: subject, CreatedAt: time.Now().UTC(), Generation: 1})
	}
	return records, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	routes, e := privacyRoutes(ctx, tx, a, r.Scope, r.SubjectID)
	if e != nil {
		return nil, e
	}
	if r.Source == governance.Provider {
		return json.Marshal(map[string]string{"state": "awaiting_external_expiry"})
	}
	ids, _ := json.Marshal(governance.SubjectIDs(ctx, r.SubjectID))
	rows, e := tx.QueryContext(ctx, `SELECT payload FROM addon_carrier_ingress WHERE tenant=$1 AND unit_id=$2 AND $3::jsonb ? employee_id AND state<>'erased' UNION ALL SELECT c.payload FROM addon_carrier_receipt c JOIN addon_carrier_route r ON r.tenant=c.tenant AND r.account=c.account AND r.id=c.route_id WHERE r.tenant=$1 AND r.unit_id=$2 AND $3::jsonb ? r.employee_id`, a.OrganizationID, r.Scope.UnitID, ids)
	if e != nil {
		return nil, e
	}
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, raw)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, route := range routes {
		raw, _, e := delivery.ReadPrivacyIntent(ctx, tx, a.OrganizationID, route.id)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, raw)
	}
	return json.Marshal(out)
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	routes, e := privacyRoutes(ctx, tx, a, r.Scope, r.SubjectID)
	if e != nil {
		return e
	}
	if r.Source == governance.Provider {
		return governance.ErrExternalPending
	}
	ids, _ := json.Marshal(governance.SubjectIDs(ctx, r.SubjectID))
	if _, e = tx.ExecContext(ctx, `UPDATE addon_carrier_receipt c SET payload='{}',state='erased' FROM addon_carrier_route r WHERE r.tenant=c.tenant AND r.account=c.account AND r.id=c.route_id AND r.tenant=$1 AND r.unit_id=$2 AND $3::jsonb ? r.employee_id`, a.OrganizationID, r.Scope.UnitID, ids); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE addon_carrier_ingress SET payload='{}',state='erased',employee_id='',unit_id='' WHERE tenant=$1 AND unit_id=$2 AND $3::jsonb ? employee_id`, a.OrganizationID, r.Scope.UnitID, ids); e != nil {
		return e
	}
	for _, route := range routes {
		if e = delivery.EraseIntent(ctx, tx, a.OrganizationID, route.id); e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `DELETE FROM addon_carrier_route WHERE tenant=$1 AND unit_id=$2 AND $3::jsonb ? employee_id`, a.OrganizationID, r.Scope.UnitID, ids)
	return e
}
