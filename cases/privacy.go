package cases

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"io"
	"strings"
	"time"
)

// PrivacyCase is the owner port shared by derivative adapters. Privacy grants
// alone never reveal a case; current ordinary case work/triage policy also applies.
func PrivacyCase(ctx context.Context, tx *sql.Tx, a api.Actor, id string) (Case, error) {
	var raw []byte
	var c Case
	e := tx.QueryRowContext(ctx, `SELECT payload FROM addon_cases_case WHERE organization_id=$1 AND id=$2 FOR UPDATE`, a.OrganizationID, id).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return c, governance.ErrNotFound
	}
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		return c, e
	}
	p := PrincipalForActor(a)
	if !privacyOperatorRead(p, c) {
		return Case{}, governance.ErrDenied
	}
	return c, nil
}
func privacyOperatorRead(p Principal, c Case) bool {
	return operatorRead(p, c)
}

// PrivacyCases discovers only cases containing the named verified identity.
// It deliberately excludes inaccessible cases rather than disclosing counts.
func PrivacyCases(ctx context.Context, tx *sql.Tx, a api.Actor, scope governance.Scope, subjectID string) ([]Case, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id,payload FROM addon_cases_case WHERE organization_id=$1 AND unit_id=$2 ORDER BY id FOR UPDATE`, scope.OrganizationID, scope.UnitID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		var id string
		var raw []byte
		var c Case
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return nil, e
		}
		found := false
		for _, p := range c.Participants {
			if governance.SubjectMatches(ctx, subjectID, p.ID) {
				found = true
			}
		}
		if !found {
			continue
		}
		p := PrincipalForActor(a)
		if privacyOperatorRead(p, c) {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

type PrivacyAdapter struct{}

// Correct updates current narrative only. Historical event facts and creation
// dimensions remain unchanged; hold review is enforced by governance first.
func (PrivacyAdapter) Correct(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record, patch json.RawMessage) error {
	var in struct {
		Expected  uint64 `json:"expected_version"`
		Narrative string `json:"narrative"`
		Reason    string `json:"reason"`
	}
	d := json.NewDecoder(bytes.NewReader(patch))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		return governance.ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || strings.TrimSpace(in.Narrative) == "" || len(in.Narrative) > 60000 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1000 {
		return governance.ErrInvalid
	}
	c, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return e
	}
	if c.UnitID != r.Scope.UnitID || !canManage(PrincipalForActor(a), c) {
		return governance.ErrDenied
	}
	if c.Version != in.Expected {
		return governance.ErrConflict
	}
	old := c.Version
	c.Narrative = in.Narrative
	c.Version++
	now := time.Now().UTC()
	c.Events = append(c.Events, Event{Kind: "privacy_correction", Version: c.Version, ActorID: a.UserID, From: c.State, To: c.State, Reason: in.Reason, RecordedAt: now, OccurredAt: now, OrganizationID: c.OrganizationID, UnitID: c.UnitID})
	return (&sqlTransaction{tx: tx}).Save(ctx, c, old)
}

func (PrivacyAdapter) Name() string { return "cases" }
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	cs, e := PrivacyCases(ctx, tx, a, s, subject)
	if e != nil {
		return nil, e
	}
	out := []governance.Record{}
	for _, c := range cs {
		created := time.Time{}
		if len(c.Events) > 0 {
			created = c.Events[0].RecordedAt
		}
		if created.IsZero() {
			return nil, governance.ErrInvalid
		}
		id := "cases:" + c.ID
		out = append(out, governance.Record{ID: id, LineageID: id, Scope: s, SubjectID: subject, Source: governance.Case, Reference: c.ID, CreatedAt: created, Generation: 1})
		var table bool
		if e = tx.QueryRowContext(ctx, `SELECT to_regclass('addon_cases_delivery') IS NOT NULL`).Scan(&table); e != nil {
			return nil, e
		}
		if table {
			var external bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_cases_delivery d JOIN addon_cases_outbox o ON o.id=d.id WHERE d.organization_id=$1 AND o.case_id=$2 AND (d.state IN('sending','accepted','delivered','held_unknown') OR d.provider_id<>'' OR EXISTS(SELECT 1 FROM addon_cases_delivery_receipt receipt WHERE receipt.tenant=d.organization_id AND receipt.intent_id=d.id)))`, a.OrganizationID, c.ID).Scan(&external); e != nil {
				return nil, e
			}
			if external {
				out = append(out, governance.Record{ID: id + ":transport", LineageID: id, Scope: s, SubjectID: subject, Source: governance.Provider, Reference: c.ID, CreatedAt: created, Generation: 1})
			}
		}
	}
	return out, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	c, e := PrivacyCase(ctx, tx, a, r.Reference)
	if errors.Is(e, governance.ErrNotFound) {
		return json.RawMessage(`null`), nil
	}
	if e != nil {
		return nil, e
	}
	if c.UnitID != r.Scope.UnitID {
		return nil, governance.ErrDenied
	}
	if r.Source == governance.Provider {
		return json.Marshal(map[string]string{"state": "awaiting_external_expiry", "case_reference": r.Reference})
	}
	return json.Marshal(c)
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	c, e := PrivacyCase(ctx, tx, a, r.Reference)
	if errors.Is(e, governance.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if c.UnitID != r.Scope.UnitID {
		return governance.ErrDenied
	}
	// Remove actual content, event reasons, intake fingerprints and acknowledgments.
	// A retained governance journal carries only opaque references and decisions.
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT to_regclass('addon_cases_delivery') IS NOT NULL`).Scan(&exists); e != nil {
		return e
	}
	if exists {
		if _, e = tx.ExecContext(ctx, `DELETE FROM addon_cases_delivery WHERE organization_id=$1 AND id IN(SELECT id FROM addon_cases_outbox WHERE organization_id=$1 AND case_id=$2)`, a.OrganizationID, c.ID); e != nil {
			return e
		}
	}
	for _, q := range []string{`DELETE FROM addon_cases_outbox WHERE organization_id=$1 AND case_id=$2`, `DELETE FROM addon_cases_event WHERE organization_id=$1 AND case_id=$2`, `DELETE FROM addon_cases_intake WHERE organization_id=$1 AND case_id=$2`, `DELETE FROM addon_cases_case WHERE organization_id=$1 AND id=$2`} {
		if _, e = tx.ExecContext(ctx, q, a.OrganizationID, c.ID); e != nil {
			return e
		}
	}
	return nil
}
