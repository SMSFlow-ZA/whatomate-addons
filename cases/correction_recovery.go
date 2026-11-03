package cases

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"io"
)

// Only the new narrative and original correction event are retained. The full
// original case is fingerprinted, not retained as a second personal-data copy.
type caseCorrectionRecovery struct {
	Schema                                string
	Tenant, Unit, Record, Subject, CaseID string
	ContactID                             string
	Before, After                         string
	BeforeVersion, AfterVersion           uint64
	Narrative                             string
	Event                                 Event
}

func correctionCaseHash(c Case) string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (p PrivacyAdapter) VerifyCorrectionTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record, payload json.RawMessage) (governance.CorrectionRestoreResult, error) {
	var out governance.CorrectionRestoreResult
	var v caseCorrectionRecovery
	if json.Unmarshal(payload, &v) != nil || v.Schema != "case-narrative/v1" || v.Tenant != a.OrganizationID || v.Tenant != r.Scope.OrganizationID || v.Unit != r.Scope.UnitID || v.Record != r.ID || v.Subject != r.SubjectID || v.CaseID != r.Reference {
		return out, governance.ErrInvalid
	}
	c, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return out, e
	}
	if c.UnitID != v.Unit || !canManage(PrincipalForActor(a), c) {
		return out, governance.ErrDenied
	}
	h := correctionCaseHash(c)
	if h != v.After || c.Version != v.AfterVersion {
		return out, governance.ErrConflict
	}
	return governance.CorrectionRestoreResult{Fingerprint: h, Version: c.Version, AlreadyCurrent: true}, nil
}
func (p PrivacyAdapter) CorrectWithRecoveryTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record, patch json.RawMessage) (json.RawMessage, error) {
	before, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return nil, e
	}
	if e = p.Correct(ctx, tx, a, r, patch); e != nil {
		return nil, e
	}
	after, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return nil, e
	}
	if after.Version != before.Version+1 || len(after.Events) != len(before.Events)+1 {
		return nil, governance.ErrConflict
	}
	return json.Marshal(caseCorrectionRecovery{Schema: "case-narrative/v1", Tenant: r.Scope.OrganizationID, Unit: r.Scope.UnitID, Record: r.ID, Subject: r.SubjectID, CaseID: r.Reference, ContactID: before.ContactID, Before: correctionCaseHash(before), After: correctionCaseHash(after), BeforeVersion: before.Version, AfterVersion: after.Version, Narrative: after.Narrative, Event: after.Events[len(after.Events)-1]})
}

// History verification checks the latest approved field value while preserving
// unrelated later source events. Mutation still requires the exact full before
// checkpoint in RestoreCorrectionTx and never merges a divergent snapshot.
func (p PrivacyAdapter) VerifyCorrectionHistoryTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record, history []json.RawMessage) (governance.CorrectionRestoreResult, error) {
	var out governance.CorrectionRestoreResult
	var latest caseCorrectionRecovery
	if len(history) == 0 {
		return out, governance.ErrInvalid
	}
	var previous uint64
	for _, raw := range history {
		var v caseCorrectionRecovery
		if json.Unmarshal(raw, &v) != nil || v.Schema != "case-narrative/v1" || v.Tenant != a.OrganizationID || v.Tenant != r.Scope.OrganizationID || v.Unit != r.Scope.UnitID || v.Subject != r.SubjectID || v.Record != r.ID || v.CaseID != r.Reference || v.ContactID == "" || v.BeforeVersion < previous || v.AfterVersion != v.BeforeVersion+1 {
			return out, governance.ErrInvalid
		}
		if previous != 0 && v.ContactID != latest.ContactID {
			return out, governance.ErrConflict
		}
		latest = v
		previous = v.AfterVersion
	}
	c, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return out, e
	}
	if c.UnitID != r.Scope.UnitID || !canManage(PrincipalForActor(a), c) {
		return out, governance.ErrDenied
	}
	if c.ContactID != latest.ContactID || c.Version < latest.AfterVersion || c.Narrative != latest.Narrative {
		return out, governance.ErrConflict
	}
	return governance.CorrectionRestoreResult{Fingerprint: correctionCaseHash(c), Version: c.Version, AlreadyCurrent: true}, nil
}
func (p PrivacyAdapter) RestoreCorrectionTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record, payload json.RawMessage) (governance.CorrectionRestoreResult, error) {
	var out governance.CorrectionRestoreResult
	var v caseCorrectionRecovery
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil {
		return out, governance.ErrInvalid
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return out, governance.ErrInvalid
	}
	if v.Schema != "case-narrative/v1" || v.Tenant != a.OrganizationID || v.Tenant != r.Scope.OrganizationID || v.Unit != r.Scope.UnitID || v.Record != r.ID || v.Subject != r.SubjectID || v.CaseID != r.Reference || v.AfterVersion != v.BeforeVersion+1 || len(v.Before) != 64 || len(v.After) != 64 || v.Event.Kind != "privacy_correction" || v.Event.Version != v.AfterVersion || v.Event.OrganizationID != v.Tenant || v.Event.UnitID != v.Unit {
		return out, governance.ErrInvalid
	}
	c, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return out, e
	}
	if c.UnitID != v.Unit || !canManage(PrincipalForActor(a), c) {
		return out, governance.ErrDenied
	}
	hash := correctionCaseHash(c)
	if hash == v.After && c.Version == v.AfterVersion {
		return governance.CorrectionRestoreResult{Fingerprint: hash, Version: c.Version, AlreadyCurrent: true}, nil
	}
	if hash != v.Before || c.Version != v.BeforeVersion {
		return out, governance.ErrConflict
	}
	c.Narrative = v.Narrative
	c.Version = v.AfterVersion
	c.Events = append(c.Events, v.Event)
	if correctionCaseHash(c) != v.After {
		return out, governance.ErrConflict
	}
	if e = (&sqlTransaction{tx: tx}).Save(ctx, c, v.BeforeVersion); e != nil {
		return out, e
	}
	actual, e := PrivacyCase(ctx, tx, a, r.Reference)
	if e != nil {
		return out, e
	}
	if correctionCaseHash(actual) != v.After {
		return out, governance.ErrConflict
	}
	return governance.CorrectionRestoreResult{Fingerprint: v.After, Version: v.AfterVersion}, nil
}
