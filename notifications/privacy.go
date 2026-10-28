package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"strings"
	"time"
)

// EraseIntent is a trusted owner port: call only in the governed transaction
// after case scope, subject, retention and all lineage holds were checked.
func EraseIntent(ctx context.Context, tx *sql.Tx, tenant, id string) error {
	if tx == nil || tenant == "" || id == "" {
		return ErrDenied
	}
	// Serialize projection against erasure, preserving the shared lock order.
	if _, e := tx.ExecContext(ctx, `SELECT tenant FROM addon_notifications_activation WHERE tenant=$1 FOR UPDATE`, tenant); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO addon_notifications_erased(tenant,id) VALUES($1,$2) ON CONFLICT DO NOTHING`, tenant, id); e != nil {
		return e
	}
	if e := delivery.EraseIntent(ctx, tx, tenant, id); e != nil {
		return e
	}
	if e := carrier.EraseProtectedIntent(ctx, tx, tenant, id); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM addon_notifications_intent WHERE tenant=$1 AND id=$2`, tenant, id)
	return e
}

// PrivacyAdapter registers notices as derivatives of their case. Case lineage
// holds therefore block every local notice/receipt erasure; transport copies
// remain independently awaiting expiry rather than being called deleted.
type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "notifications" }
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, scope governance.Scope, subject string) ([]governance.Record, error) {
	if a.OrganizationID != scope.OrganizationID || !a.Allows("conversation:privacy", scope.UnitID) {
		return nil, nil
	}
	if _, e := tx.ExecContext(ctx, `SELECT tenant FROM addon_notifications_activation WHERE tenant=$1 FOR UPDATE`, scope.OrganizationID); e != nil {
		return nil, e
	}
	list, e := cases.PrivacyCases(ctx, tx, a, scope, subject)
	if e != nil {
		return nil, e
	}
	out := []governance.Record{}
	for _, c := range list {
		rows, e := tx.QueryContext(ctx, `SELECT id,created_at FROM addon_notifications_intent WHERE tenant=$1 AND case_id=$2 AND unit_id=$3 ORDER BY id`, scope.OrganizationID, c.ID, scope.UnitID)
		if e != nil {
			return nil, e
		}
		type ref struct {
			id string
			at time.Time
		}
		var refs []ref
		for rows.Next() {
			var r ref
			if e = rows.Scan(&r.id, &r.at); e != nil {
				rows.Close()
				return nil, e
			}
			refs = append(refs, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for _, r := range refs {
			record := governance.Record{ID: "notifications:" + r.id, LineageID: "cases:" + c.ID, Scope: scope, SubjectID: subject, Source: governance.Conversation, Reference: r.id, CreatedAt: r.at, Generation: 1}
			out = append(out, record)
			_, external, e := delivery.ReadPrivacyIntent(ctx, tx, a.OrganizationID, r.id)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			if external {
				record.ID += ":transport"
				record.Source = governance.Provider
				out = append(out, record)
			}
		}
	}
	return out, nil
}
func notificationPrivacy(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (bool, error) {
	if r.Source != governance.Conversation && r.Source != governance.Provider {
		return false, governance.ErrDenied
	}
	if a.OrganizationID != r.Scope.OrganizationID || !a.Allows("conversation:privacy", r.Scope.UnitID) {
		return false, governance.ErrDenied
	}
	var caseID, unit string
	e := tx.QueryRowContext(ctx, `SELECT case_id,unit_id FROM addon_notifications_intent WHERE tenant=$1 AND id=$2`, a.OrganizationID, r.Reference).Scan(&caseID, &unit)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	c, e := cases.PrivacyCase(ctx, tx, a, caseID)
	if e != nil {
		return false, e
	}
	if unit != r.Scope.UnitID || c.UnitID != unit || r.LineageID != "cases:"+caseID {
		return false, governance.ErrDenied
	}
	found := false
	for _, p := range c.Participants {
		if governance.SubjectMatches(ctx, r.SubjectID, p.ID) {
			found = true
		}
	}
	if !found {
		return false, governance.ErrDenied
	}
	return true, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	exists, e := notificationPrivacy(ctx, tx, a, r)
	if e != nil {
		return nil, e
	}
	if !exists {
		return json.RawMessage(`null`), nil
	}
	if r.Source == governance.Provider {
		return json.RawMessage(`{"state":"awaiting_external_expiry"}`), nil
	}
	raw, _, e := delivery.ReadPrivacyIntent(ctx, tx, a.OrganizationID, r.Reference)
	state := "blocked"
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if e == nil {
		var m delivery.Message
		if e = json.Unmarshal(raw, &m); e != nil {
			return nil, e
		}
		state = m.State
	}
	return json.Marshal(map[string]string{"state": state, "purpose": delivery.HandlerEscalation, "case_reference": strings.TrimPrefix(r.LineageID, "cases:")})
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	exists, e := notificationPrivacy(ctx, tx, a, r)
	if e != nil {
		return e
	}
	if !exists {
		return nil
	}
	if r.Source == governance.Provider {
		return governance.ErrExternalPending
	}
	return EraseIntent(ctx, tx, a.OrganizationID, r.Reference)
}
