package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "directory" }
func privacySnapshot(ctx context.Context, tx *sql.Tx, a api.Actor, unit, subject string) (Snapshot, error) {
	var raw []byte
	var s Snapshot
	e := tx.QueryRowContext(ctx, `SELECT payload FROM directory_snapshots WHERE tenant=$1 FOR UPDATE`, a.OrganizationID).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(raw, &s); e != nil {
		return s, e
	}
	emp, exists := s.Employees[subject]
	if !a.Allows("directory:privacy", unit) || !a.Has("directory:read") || exists && emp.Zone != unit {
		return Snapshot{}, governance.ErrDenied
	}
	return s, nil
}
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	if !a.Allows("directory:privacy", s.UnitID) {
		return nil, nil
	}
	out := []governance.Record{}
	for _, subjectID := range governance.SubjectIDs(ctx, subject) {
		snap, e := privacySnapshot(ctx, tx, a, s.UnitID, subjectID)
		if e != nil {
			return nil, e
		}
		if _, ok := snap.Employees[subjectID]; !ok {
			continue
		}
		id := "directory:" + subjectID
		out = append(out, governance.Record{ID: id, LineageID: id, Scope: s, SubjectID: subject, Source: governance.Directory, Reference: subjectID, CreatedAt: snap.ImportedAt, Generation: 1})
	}
	return out, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	s, e := privacySnapshot(ctx, tx, a, r.Scope.UnitID, r.Reference)
	if e != nil {
		return nil, e
	}
	emp, exists := s.Employees[r.Reference]
	return json.Marshal(struct {
		Exists   bool     `json:"exists"`
		Employee Employee `json:"employee"`
		Revoked  bool     `json:"revoked"`
	}{exists, emp, s.Revoked[r.Reference]})
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	s, e := privacySnapshot(ctx, tx, a, r.Scope.UnitID, r.Reference)
	if e != nil {
		return e
	}
	if s.Version == 0 {
		return nil
	}
	delete(s.Employees, r.Reference)
	if s.Revoked == nil {
		s.Revoked = map[string]bool{}
	}
	s.Revoked[r.Reference] = true
	s.Version++
	raw, e := json.Marshal(s)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE directory_snapshots SET version=$2,payload=$3 WHERE tenant=$1`, a.OrganizationID, s.Version, raw); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO directory_events(tenant,version,actor) VALUES($1,$2,$3)`, a.OrganizationID, s.Version, a.UserID); e != nil {
		return e
	}
	// Source previews are complete immutable approval artefacts. Purging an import
	// containing this person invalidates the whole preview rather than forging its
	// original digest or leaving a replayable personal-data copy.
	_, e = tx.ExecContext(ctx, `DELETE FROM directory_previews WHERE tenant=$1 AND EXISTS(SELECT 1 FROM jsonb_array_elements(payload) e WHERE e->>'id'=$2)`, a.OrganizationID, r.Reference)
	return e
}
