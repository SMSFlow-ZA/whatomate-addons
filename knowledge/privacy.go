package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"time"
)

// Knowledge privacy targets a named document, not every document uploaded by a
// person. Removing a shared policy is an explicit source-owner decision.
type PrivacyAdapter struct{}

func (PrivacyAdapter) Name() string { return "knowledge" }
func privacyAllowed(a api.Actor, m UploadMetadata, unit string) bool {
	if len(m.Audiences) == 0 {
		return false
	}
	matched := false
	for _, aud := range m.Audiences {
		if aud == unit {
			matched = true
		}
		if !a.AudienceAllowed("knowledge:retire", aud) || !a.AudienceAllowed("knowledge:read", aud) {
			return false
		}
	}
	return matched
}
func (PrivacyAdapter) Discover(ctx context.Context, tx *sql.Tx, a api.Actor, s governance.Scope, subject string) ([]governance.Record, error) {
	rows, e := tx.QueryContext(ctx, `SELECT id,metadata,created_at,(receipt IS NOT NULL AND receipt<>'null'::jsonb) FROM addon_knowledge_jobs WHERE tenant=$1 AND metadata->>'document_id'=$2 ORDER BY id FOR UPDATE`, a.OrganizationID, subject)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []governance.Record{}
	for rows.Next() {
		var id string
		var raw []byte
		var at time.Time
		var m UploadMetadata
		var workerReceipt bool
		if e = rows.Scan(&id, &raw, &at, &workerReceipt); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &m); e != nil {
			return nil, e
		}
		if !privacyAllowed(a, m, s.UnitID) {
			continue
		}
		rid := "knowledge:" + id
		out = append(out, governance.Record{ID: rid, LineageID: rid, Scope: s, SubjectID: subject, Source: governance.Knowledge, Reference: id, CreatedAt: at, Generation: 1})
		if workerReceipt {
			out = append(out, governance.Record{ID: rid + ":worker-files", LineageID: rid, Scope: s, SubjectID: subject, Source: governance.WorkerFile, Reference: id, CreatedAt: at, Generation: 1})
		}
	}
	return out, rows.Err()
}
func privacyJob(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (UploadMetadata, json.RawMessage, error) {
	var raw, metadata []byte
	var m UploadMetadata
	e := tx.QueryRowContext(ctx, `SELECT metadata,jsonb_build_object('metadata',metadata,'input_base64',encode(input,'base64'),'input_hash',input_hash,'status',status,'document',document,'evidence',evidence,'approval',approval,'receipt',receipt,'created_at',created_at) FROM addon_knowledge_jobs j WHERE tenant=$1 AND id=$2 FOR UPDATE`, a.OrganizationID, r.Reference).Scan(&metadata, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return m, json.RawMessage(`null`), nil
	}
	if e != nil {
		return m, nil, e
	}
	if e = json.Unmarshal(metadata, &m); e != nil {
		return m, nil, e
	}
	if !privacyAllowed(a, m, r.Scope.UnitID) {
		return m, nil, governance.ErrDenied
	}
	return m, raw, nil
}
func (PrivacyAdapter) Export(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (json.RawMessage, error) {
	_, raw, e := privacyJob(ctx, tx, a, r)
	if e == nil && r.Source == governance.WorkerFile && string(raw) != "null" {
		return json.Marshal(map[string]string{"state": "awaiting_external_expiry", "worker_job_reference": r.Reference})
	}
	return raw, e
}
func (PrivacyAdapter) Erase(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) error {
	m, raw, e := privacyJob(ctx, tx, a, r)
	if e != nil {
		return e
	}
	if string(raw) == "null" {
		return nil
	}
	// A duplicate upload must not authorize destruction of a different audience's
	// publication or quarantine copy with the same document/version identity.
	rows, e := tx.QueryContext(ctx, `SELECT metadata FROM addon_knowledge_jobs WHERE tenant=$1 AND metadata->>'document_id'=$2 AND metadata->>'version'=$3 FOR UPDATE`, a.OrganizationID, m.DocumentID, m.Version)
	if e != nil {
		return e
	}
	for rows.Next() {
		var metadata []byte
		var other UploadMetadata
		if e = rows.Scan(&metadata); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(metadata, &other); e != nil {
			rows.Close()
			return e
		}
		if !privacyAllowed(a, other, r.Scope.UnitID) {
			rows.Close()
			return governance.ErrDenied
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var publication []byte
	e = tx.QueryRowContext(ctx, `SELECT document FROM addon_knowledge_publications WHERE tenant=$1 AND document_id=$2 AND version=$3 FOR UPDATE`, a.OrganizationID, m.DocumentID, m.Version).Scan(&publication)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e == nil {
		var d Document
		if e = json.Unmarshal(publication, &d); e != nil {
			return e
		}
		if !privacyAllowed(a, UploadMetadata{Audiences: d.Audiences}, r.Scope.UnitID) {
			return governance.ErrDenied
		}
	}
	// Publication embeds source bytes and pages; remove it as well as its FTS page
	// rows and approvals. Pending/failed quarantine bytes and receipts are removed.
	for _, q := range []string{`DELETE FROM addon_knowledge_pages WHERE tenant=$1 AND document_id=$2 AND version=$3`, `DELETE FROM addon_knowledge_approvals WHERE tenant=$1 AND document_id=$2 AND version=$3`, `DELETE FROM addon_knowledge_publications WHERE tenant=$1 AND document_id=$2 AND version=$3`} {
		if _, e = tx.ExecContext(ctx, q, a.OrganizationID, m.DocumentID, m.Version); e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `DELETE FROM addon_knowledge_jobs WHERE tenant=$1 AND metadata->>'document_id'=$2 AND metadata->>'version'=$3`, a.OrganizationID, m.DocumentID, m.Version)
	return e
}
