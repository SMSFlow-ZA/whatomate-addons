package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// PostgresRepository stores only extension-owned records. A distinct tenant is
// mandatory; grant provisioning belongs to the host's authenticated RBAC bridge.
type PostgresRepository struct {
	db     *sql.DB
	tenant string
	tx     *sql.Tx
}

func NewPostgresRepository(db *sql.DB, tenant string) (*PostgresRepository, error) {
	if db == nil || strings.TrimSpace(tenant) == "" {
		return nil, ErrDenied
	}
	return &PostgresRepository{db: db, tenant: tenant}, nil
}
func (r *PostgresRepository) Migrate(ctx context.Context) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer r.rollback(tx)
	// Serialize this package's schema migration across instances.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(19631020)"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_knowledge_grants (
 tenant text NOT NULL,principal text NOT NULL,action text NOT NULL,scope text NOT NULL,
 PRIMARY KEY(tenant,principal,action,scope),CHECK(action IN ('publish','review','retire','read')));
 CREATE TABLE IF NOT EXISTS addon_knowledge_approvals (
 tenant text NOT NULL,document_id text NOT NULL,version text NOT NULL,
 reviewer text NOT NULL,manifest_hash text NOT NULL,approved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,document_id,version,reviewer,manifest_hash));
 CREATE TABLE IF NOT EXISTS addon_knowledge_publications (
 tenant text NOT NULL,document_id text NOT NULL,version text NOT NULL,effective_at timestamptz NOT NULL,
 document jsonb NOT NULL,evidence jsonb NOT NULL,approval jsonb NOT NULL,manifest_hash text NOT NULL,
 published_by text NOT NULL,published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 retired boolean NOT NULL DEFAULT false,retired_by text,retired_at timestamptz,
 PRIMARY KEY(tenant,document_id,version),UNIQUE(tenant,document_id,effective_at));
 ALTER TABLE addon_knowledge_publications ADD COLUMN IF NOT EXISTS withdraw_at timestamptz;
 CREATE TABLE IF NOT EXISTS addon_knowledge_pages (
 tenant text NOT NULL,document_id text NOT NULL,version text NOT NULL,page_number integer NOT NULL CHECK(page_number>0),
 page_text text NOT NULL,extraction_method text NOT NULL,extractor_revision text NOT NULL,
 search tsvector GENERATED ALWAYS AS (to_tsvector('simple',page_text)) STORED,
 PRIMARY KEY(tenant,document_id,version,page_number),
 FOREIGN KEY(tenant,document_id,version) REFERENCES addon_knowledge_publications(tenant,document_id,version) ON DELETE CASCADE);
 CREATE INDEX IF NOT EXISTS addon_knowledge_pages_search ON addon_knowledge_pages USING GIN(search);`)
	if err != nil {
		return err
	}
	return r.commit(tx)
}
func (r *PostgresRepository) authorize(ctx context.Context, tx *sql.Tx, actor, action, scope string) error {
	if strings.TrimSpace(actor) == "" {
		return ErrDenied
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM addon_knowledge_grants WHERE tenant=$1 AND principal=$2 AND action=$3 AND scope=$4 FOR SHARE`, r.tenant, actor, action, scope).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

// Publish checks both the publisher and named human reviewer's current grants
// inside the same transaction as persistence. FOR SHARE serializes revocation.
func (r *PostgresRepository) Publish(ctx context.Context, actor string, d Document, e Evidence, a Approval) error {
	p, err := Publish(d, e, a)
	if err != nil {
		return err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer r.rollback(tx)
	if err = r.authorize(ctx, tx, actor, "publish", "*"); err != nil {
		return err
	}
	if err = r.authorize(ctx, tx, a.Reviewer, "review", "*"); err != nil {
		return err
	}
	var approved int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM addon_knowledge_approvals WHERE tenant=$1 AND document_id=$2 AND version=$3 AND reviewer=$4 AND manifest_hash=$5 FOR SHARE`, r.tenant, d.ID, d.Version, a.Reviewer, a.ManifestHash).Scan(&approved)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	document, err := json.Marshal(p.document)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(e)
	if err != nil {
		return err
	}
	approval, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var withdrawAt any
	if !d.WithdrawAt.IsZero() {
		withdrawAt = d.WithdrawAt
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO addon_knowledge_publications(tenant,document_id,version,effective_at,document,evidence,approval,manifest_hash,published_by,withdraw_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, r.tenant, d.ID, d.Version, d.Effective, document, evidence, approval, p.manifestHash, actor, withdrawAt)
	if err != nil {
		return err
	}
	for _, page := range p.document.Pages {
		if _, err = tx.ExecContext(ctx, `INSERT INTO addon_knowledge_pages(tenant,document_id,version,page_number,page_text,extraction_method,extractor_revision) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.tenant, d.ID, d.Version, page.Number, page.Text, page.Method, page.ExtractorRevision); err != nil {
			return err
		}
	}
	return r.commit(tx)
}
func (r *PostgresRepository) Retire(ctx context.Context, actor, id, version string) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer r.rollback(tx)
	if err = r.authorize(ctx, tx, actor, "retire", "*"); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE addon_knowledge_publications SET retired=true,retired_by=$1,retired_at=clock_timestamp() WHERE tenant=$2 AND document_id=$3 AND version=$4 AND NOT retired`, actor, r.tenant, id, version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDenied
	}
	return r.commit(tx)
}

// Retrieve checks a transaction-locked durable read grant as well as the host
// authorizer. Current versions are chosen before withdrawal/audience filtering.
func (r *PostgresRepository) Retrieve(ctx context.Context, q Query, auth Authorizer) ([]Source, error) {
	return r.retrieve(ctx, q, auth, false)
}

// RetrieveCandidates broadens lexical recall for questions while preserving
// every authoritative access/current-version/provenance check. It is bounded
// lexical retrieval, not an embedding or semantic search implementation.
func (r *PostgresRepository) RetrieveCandidates(ctx context.Context, q Query, auth Authorizer) ([]Source, error) {
	return r.retrieve(ctx, q, auth, true)
}

func (r *PostgresRepository) retrieve(ctx context.Context, q Query, auth Authorizer, candidates bool) ([]Source, error) {
	if auth == nil || q.Principal == "" || q.Audience == "" || q.At.IsZero() || strings.TrimSpace(q.Text) == "" || len(q.Text) > 4096 || !auth.Allowed(ctx, q.Principal, q.Audience) {
		return nil, nil
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer r.rollback(tx)
	if err = r.authorize(ctx, tx, q.Principal, "read", q.Audience); err != nil {
		if errors.Is(err, ErrDenied) {
			return nil, nil
		}
		return nil, err
	}
	queryText := q.Text
	queryFunction := "plainto_tsquery"
	if candidates {
		stop := map[string]bool{}
		for _, word := range strings.Fields("a an and are as at be by can could did do does for from had has have how i in is it me my of on or our should that the their them there these they this to us was we were what when where which who why will with would you your") {
			stop[word] = true
		}
		terms := []string{}
		seen := map[string]bool{}
		for _, word := range strings.FieldsFunc(strings.ToLower(q.Text), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
			if stop[word] || seen[word] {
				continue
			}
			seen[word] = true
			terms = append(terms, "'"+word+"'")
			if len(terms) == 64 {
				break
			}
		}
		if len(terms) == 0 {
			return []Source{}, nil
		}
		queryText = strings.Join(terms, " | ")
		queryFunction = "to_tsquery"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`WITH current AS (
 SELECT DISTINCT ON(document_id) document_id,version,document,evidence,approval,manifest_hash,retired,withdraw_at
 FROM addon_knowledge_publications WHERE tenant=$1 AND effective_at <= $2 ORDER BY document_id,effective_at DESC)
 SELECT c.document,c.evidence,c.approval,c.manifest_hash,p.page_number,p.page_text,p.extraction_method,p.extractor_revision
 FROM current c JOIN addon_knowledge_pages p ON p.tenant=$1 AND p.document_id=c.document_id AND p.version=c.version
 WHERE NOT c.retired AND (c.withdraw_at IS NULL OR c.withdraw_at>$2) AND (c.document->'Audiences') ? $3
 AND p.search @@ %s('simple',$4)
 ORDER BY ts_rank_cd(p.search,%s('simple',$4)) DESC,c.document_id,p.page_number LIMIT 20`, queryFunction, queryFunction), r.tenant, q.At, q.Audience, queryText)
	if err != nil {
		return nil, err
	}
	sources := []Source{}
	for rows.Next() {
		var document, evidence, approval []byte
		var hash string
		var page Page
		if err = rows.Scan(&document, &evidence, &approval, &hash, &page.Number, &page.Text, &page.Method, &page.ExtractorRevision); err != nil {
			rows.Close()
			return nil, err
		}
		var d Document
		var e Evidence
		var a Approval
		if json.Unmarshal(document, &d) != nil || json.Unmarshal(evidence, &e) != nil || json.Unmarshal(approval, &a) != nil {
			rows.Close()
			return nil, ErrDenied
		}
		p, publishErr := Publish(d, e, a)
		if publishErr != nil || hash != p.manifestHash {
			rows.Close()
			return nil, ErrDenied
		}
		if page.Number < 1 || page.Number > len(d.Pages) || d.Pages[page.Number-1] != page {
			rows.Close()
			return nil, ErrDenied
		}
		sources = append(sources, Source{Reference: fmt.Sprintf("%s:%d", p.manifestHash, page.Number), DocumentID: d.ID, Version: d.Version, ContentHash: d.ContentHash, ManifestHash: p.manifestHash, Page: page})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = r.commit(tx); err != nil {
		return nil, err
	}
	return sources, nil
}

// Approve must receive a principal established by the host authentication layer.
// Publication cannot substitute a reviewer name for this durable approval.
func (r *PostgresRepository) Approve(ctx context.Context, reviewer string, d Document) (Approval, error) {
	if d.ID == "" || d.Version == "" {
		return Approval{}, ErrDenied
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return Approval{}, err
	}
	defer r.rollback(tx)
	if err = r.authorize(ctx, tx, reviewer, "review", "*"); err != nil {
		return Approval{}, err
	}
	a := Approval{Reviewer: reviewer, ManifestHash: ManifestHash(d)}
	_, err = tx.ExecContext(ctx, `INSERT INTO addon_knowledge_approvals(tenant,document_id,version,reviewer,manifest_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.tenant, d.ID, d.Version, reviewer, a.ManifestHash)
	if err != nil {
		return Approval{}, err
	}
	if err = r.commit(tx); err != nil {
		return Approval{}, err
	}
	return a, nil
}

// HTTP lifecycle operations share the job's transaction so artifact state and
// publication/approval/retirement commit atomically.
func (r *PostgresRepository) begin(ctx context.Context) (*sql.Tx, error) {
	if r.tx != nil {
		return r.tx, nil
	}
	return r.db.BeginTx(ctx, nil)
}
func (r *PostgresRepository) rollback(tx *sql.Tx) {
	if r.tx == nil {
		_ = tx.Rollback()
	}
}
func (r *PostgresRepository) commit(tx *sql.Tx) error {
	if r.tx != nil {
		return nil
	}
	return tx.Commit()
}
