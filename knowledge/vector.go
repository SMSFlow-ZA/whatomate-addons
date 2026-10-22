package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var ErrVectorUnavailable = errors.New("knowledge vector prerequisites unavailable")
var ErrVectorProfile = errors.New("knowledge vector profile does not match pinned generation")

// MaxEmbeddingTextBytes is the per-text UTF-8 byte ceiling shared by governed
// indexing and production embedding adapters. Transport/request budgets remain
// independently bounded and may reject a smaller serialized request.
const MaxEmbeddingTextBytes = 16000

// VectorPin identifies an immutable embedding generation. Model must identify the
// exact reviewed model/deployment version, not an automatically moving alias.
// Changing either value requires an explicitly approved new index generation.
type VectorPin struct {
	Model      string
	Dimensions int
}

// VectorRepository stores only embeddings derived from governed knowledge pages.
// It has no endpoint accepting caller-supplied content, ACLs or scan claims.
type VectorRepository struct {
	catalog *PostgresRepository
	pin     VectorPin
}

type PageEmbedder interface {
	Embed(context.Context, []string) ([][]float64, error)
}

func NewVectorRepository(db *sql.DB, tenant string, pin VectorPin) (*VectorRepository, error) {
	if strings.TrimSpace(pin.Model) == "" || pin.Model != strings.TrimSpace(pin.Model) || len(pin.Model) > 256 || pin.Dimensions < 1 || pin.Dimensions > 2000 {
		return nil, ErrVectorProfile
	}
	catalog, err := NewPostgresRepository(db, tenant)
	if err != nil {
		return nil, err
	}
	return &VectorRepository{catalog: catalog, pin: pin}, nil
}

func (v *VectorRepository) valid() bool {
	return v != nil && v.catalog != nil && v.catalog.db != nil && v.catalog.tenant != "" && v.pin.Model != "" && v.pin.Dimensions > 0 && v.pin.Dimensions <= 2000
}

func vectorExtension(ctx context.Context, tx *sql.Tx) error {
	var present bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public')`).Scan(&present); err != nil {
		return err
	}
	if !present {
		return ErrVectorUnavailable
	}
	return nil
}

func (v *VectorRepository) verifyTx(ctx context.Context, tx *sql.Tx) error {
	if err := vectorExtension(ctx, tx); err != nil {
		return err
	}
	var model string
	var dimensions int
	if err := tx.QueryRowContext(ctx, `SELECT model,dimensions FROM addon_knowledge_vector_profiles WHERE tenant=$1 FOR SHARE`, v.catalog.tenant).Scan(&model, &dimensions); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrVectorProfile
		}
		return err
	}
	if model != v.pin.Model || dimensions != v.pin.Dimensions {
		return ErrVectorProfile
	}

	// Verify relation/column availability without reading any source vector or
	// issuing repair DDL. A retained profile alone does not prove a usable backend.
	rows, err := tx.QueryContext(ctx, `SELECT v.embedding,v.manifest_hash,v.page_hash,v.model,v.dimensions,w.enabled
 FROM addon_knowledge_vectors v LEFT JOIN addon_knowledge_vector_workers w ON w.tenant=v.tenant WHERE false`)
	if err != nil {
		return err
	}
	return rows.Close()
}

// Migrate is an explicit host migration port. An administrator must install the
// actual pgvector extension beforehand; application code never creates it.
// Knowledge publication/page migrations must already have run.
func (v *VectorRepository) Migrate(ctx context.Context) error {
	if !v.valid() {
		return ErrDenied
	}
	tx, err := v.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(19631025)`); err != nil {
		return err
	}
	if err = vectorExtension(ctx, tx); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_knowledge_vector_profiles (
 tenant text PRIMARY KEY,model text NOT NULL,dimensions integer NOT NULL CHECK(dimensions BETWEEN 1 AND 2000),
 UNIQUE(tenant,model,dimensions));
 CREATE TABLE IF NOT EXISTS addon_knowledge_vector_workers (
 tenant text NOT NULL,principal text NOT NULL,enabled boolean NOT NULL DEFAULT true,
 PRIMARY KEY(tenant,principal),FOREIGN KEY(tenant) REFERENCES addon_knowledge_vector_profiles(tenant) ON DELETE CASCADE);
 CREATE TABLE IF NOT EXISTS addon_knowledge_vectors (
 tenant text NOT NULL,document_id text NOT NULL,version text NOT NULL,page_number integer NOT NULL,
 manifest_hash text NOT NULL,page_hash text NOT NULL,model text NOT NULL,dimensions integer NOT NULL,
 embedding public.vector NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,document_id,version,page_number,model,dimensions),
 FOREIGN KEY(tenant,document_id,version,page_number) REFERENCES addon_knowledge_pages(tenant,document_id,version,page_number) ON DELETE CASCADE,
 FOREIGN KEY(tenant,model,dimensions) REFERENCES addon_knowledge_vector_profiles(tenant,model,dimensions),
 CHECK(public.vector_dims(embedding)=dimensions));
 CREATE INDEX IF NOT EXISTS addon_knowledge_vectors_generation ON addon_knowledge_vectors(tenant,model,dimensions);`)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO addon_knowledge_vector_profiles(tenant,model,dimensions) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, v.catalog.tenant, v.pin.Model, v.pin.Dimensions); err != nil {
		return err
	}
	if err = v.verifyTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Verify performs no DDL. Root uses this during startup before enabling vector
// retrieval/indexing, independently from any provider or browser claim.
func (v *VectorRepository) Verify(ctx context.Context) error {
	if !v.valid() {
		return ErrDenied
	}
	tx, err := v.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = v.verifyTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// RegisterIndexer is host-bootstrap only. No HTTP adapter exposes this port.
// A disabled identity stays disabled on restart (ON CONFLICT does not regrant).
func (v *VectorRepository) RegisterIndexer(ctx context.Context, principal string) error {
	if !v.valid() || strings.TrimSpace(principal) == "" {
		return ErrDenied
	}
	_, err := v.catalog.db.ExecContext(ctx, `INSERT INTO addon_knowledge_vector_workers(tenant,principal) VALUES($1,$2) ON CONFLICT DO NOTHING`, v.catalog.tenant, principal)
	return err
}
func (v *VectorRepository) DisableIndexer(ctx context.Context, principal string) error {
	if !v.valid() || principal == "" {
		return ErrDenied
	}
	_, err := v.catalog.db.ExecContext(ctx, `UPDATE addon_knowledge_vector_workers SET enabled=false WHERE tenant=$1 AND principal=$2`, v.catalog.tenant, principal)
	return err
}
func (v *VectorRepository) authorizeWorker(ctx context.Context, tx *sql.Tx, principal string) error {
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT enabled FROM addon_knowledge_vector_workers WHERE tenant=$1 AND principal=$2 FOR SHARE`, v.catalog.tenant, principal).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !enabled) {
		return ErrDenied
	}
	return err
}

func encodeVector(values []float64, dimensions int) (string, error) {
	if len(values) != dimensions || dimensions < 1 || dimensions > 2000 {
		return "", ErrVectorProfile
	}
	var result strings.Builder
	result.WriteByte('[')
	nonzero := false
	for i, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
			return "", ErrVectorProfile
		}
		// pgvector stores float32; reject values which all underflow to zero too.
		f := float32(value)
		nonzero = nonzero || f != 0
		if i > 0 {
			result.WriteByte(',')
		}
		result.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	if !nonzero {
		return "", ErrVectorProfile
	}
	result.WriteByte(']')
	return result.String(), nil
}

func decodeVectorPage(document, evidence, approval []byte, hash string, page Page) (Source, error) {
	var d Document
	var e Evidence
	var a Approval
	if json.Unmarshal(document, &d) != nil || json.Unmarshal(evidence, &e) != nil || json.Unmarshal(approval, &a) != nil {
		return Source{}, ErrDenied
	}
	publication, err := Publish(d, e, a)
	if err != nil || hash != publication.manifestHash || page.Number < 1 || page.Number > len(d.Pages) || d.Pages[page.Number-1] != page {
		return Source{}, ErrDenied
	}
	return Source{Reference: fmt.Sprintf("%s:%d", hash, page.Number), DocumentID: d.ID, Version: d.Version, ContentHash: d.ContentHash, ManifestHash: hash, Page: page}, nil
}
func pageHash(page Page) string {
	raw, _ := json.Marshal(page)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

const vectorCurrent = `WITH current AS (
 SELECT DISTINCT ON(document_id) document_id,version,document,evidence,approval,manifest_hash,retired,withdraw_at
 FROM addon_knowledge_publications WHERE tenant=$1 AND effective_at<=$2 ORDER BY document_id,effective_at DESC)
 `

// IndexNext embeds at most one currently effective, human-approved page. The
// provider runs after the read transaction commits. The commit transaction locks
// publications against concurrent publish/retire/erase, then rechecks currentness,
// provenance, exact page bytes and the worker grant before the atomic insert.
// Duplicate workers may compute the same page, but cannot overwrite its vector.
// Caller runs this in a bounded, trusted worker loop; never from browser input.
func (v *VectorRepository) IndexNext(ctx context.Context, principal string, provider PageEmbedder) (bool, error) {
	if !v.valid() || principal == "" || provider == nil {
		return false, ErrDenied
	}
	tx, err := v.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = v.verifyTx(ctx, tx); err != nil {
		return false, err
	}
	if err = v.authorizeWorker(ctx, tx, principal); err != nil {
		return false, err
	}
	var document, evidence, approval []byte
	var hash string
	var page Page
	err = tx.QueryRowContext(ctx, vectorCurrent+`SELECT c.document,c.evidence,c.approval,c.manifest_hash,p.page_number,p.page_text,p.extraction_method,p.extractor_revision
 FROM current c JOIN addon_knowledge_pages p ON p.tenant=$1 AND p.document_id=c.document_id AND p.version=c.version
 WHERE NOT c.retired AND (c.withdraw_at IS NULL OR c.withdraw_at>$2)
 AND octet_length(p.page_text) BETWEEN 1 AND $5 AND btrim(p.page_text)<>''
 AND NOT EXISTS(SELECT 1 FROM addon_knowledge_vectors v WHERE v.tenant=p.tenant AND v.document_id=p.document_id AND v.version=p.version AND v.page_number=p.page_number AND v.model=$3 AND v.dimensions=$4)
 ORDER BY c.document_id,p.page_number LIMIT 1`, v.catalog.tenant, time.Now().UTC(), v.pin.Model, v.pin.Dimensions, MaxEmbeddingTextBytes).Scan(&document, &evidence, &approval, &hash, &page.Number, &page.Text, &page.Method, &page.ExtractorRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	source, err := decodeVectorPage(document, evidence, approval, hash, page)
	if err != nil {
		return false, err
	}
	if len(page.Text) == 0 || len(page.Text) > MaxEmbeddingTextBytes {
		return false, ErrDenied
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	vectors, err := provider.Embed(ctx, []string{page.Text})
	if err != nil {
		return false, err
	}
	if len(vectors) != 1 {
		return false, ErrVectorProfile
	}
	encoded, err := encodeVector(vectors[0], v.pin.Dimensions)
	if err != nil {
		return false, err
	}
	tx, err = v.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// SHARE conflicts with publication writes, including phantom newer editions.
	// This lock is never held while calling a provider.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE addon_knowledge_publications IN SHARE MODE`); err != nil {
		return false, err
	}
	if err = v.verifyTx(ctx, tx); err != nil {
		return false, err
	}
	if err = v.authorizeWorker(ctx, tx, principal); err != nil {
		return false, err
	}
	var currentDocument, currentEvidence, currentApproval []byte
	var currentHash string
	var currentPage Page
	err = tx.QueryRowContext(ctx, vectorCurrent+`SELECT c.document,c.evidence,c.approval,c.manifest_hash,p.page_number,p.page_text,p.extraction_method,p.extractor_revision
 FROM current c JOIN addon_knowledge_pages p ON p.tenant=$1 AND p.document_id=c.document_id AND p.version=c.version
 WHERE c.document_id=$3 AND c.version=$4 AND p.page_number=$5 AND NOT c.retired AND (c.withdraw_at IS NULL OR c.withdraw_at>$2) FOR SHARE OF p`, v.catalog.tenant, time.Now().UTC(), source.DocumentID, source.Version, page.Number).Scan(&currentDocument, &currentEvidence, &currentApproval, &currentHash, &currentPage.Number, &currentPage.Text, &currentPage.Method, &currentPage.ExtractorRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDenied
	}
	if err != nil {
		return false, err
	}
	currentSource, err := decodeVectorPage(currentDocument, currentEvidence, currentApproval, currentHash, currentPage)
	if err != nil || currentSource != source {
		return false, ErrDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO addon_knowledge_vectors(tenant,document_id,version,page_number,manifest_hash,page_hash,model,dimensions,embedding)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::public.vector) ON CONFLICT DO NOTHING`, v.catalog.tenant, source.DocumentID, source.Version, page.Number, hash, pageHash(page), v.pin.Model, v.pin.Dimensions, encoded)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RetrieveVector is exact native pgvector cosine search. It is not FTS renamed
// as vector search and makes no approximate-index/performance claim. Current
// publication, audience and durable read permission are authoritative on every
// read; a retired/latest edition never falls back to an older embedded edition.
func (v *VectorRepository) RetrieveVector(ctx context.Context, q Query, auth Authorizer, queryVector []float64) ([]Source, error) {
	if !v.valid() {
		return nil, ErrDenied
	}
	if auth == nil || q.Principal == "" || q.Audience == "" || q.At.IsZero() || strings.TrimSpace(q.Text) == "" || len(q.Text) > 4096 || !auth.Allowed(ctx, q.Principal, q.Audience) {
		return nil, nil
	}
	encoded, err := encodeVector(queryVector, v.pin.Dimensions)
	if err != nil {
		return nil, err
	}
	tx, err := v.catalog.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = v.verifyTx(ctx, tx); err != nil {
		return nil, err
	}
	if err = v.catalog.authorize(ctx, tx, q.Principal, "read", q.Audience); err != nil {
		if errors.Is(err, ErrDenied) {
			return nil, nil
		}
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE addon_knowledge_publications IN SHARE MODE`); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, vectorCurrent+`SELECT c.document,c.evidence,c.approval,c.manifest_hash,p.page_number,p.page_text,p.extraction_method,p.extractor_revision,v.manifest_hash,v.page_hash
 FROM current c JOIN addon_knowledge_pages p ON p.tenant=$1 AND p.document_id=c.document_id AND p.version=c.version
 JOIN addon_knowledge_vectors v ON v.tenant=p.tenant AND v.document_id=p.document_id AND v.version=p.version AND v.page_number=p.page_number
 WHERE NOT c.retired AND (c.withdraw_at IS NULL OR c.withdraw_at>$2) AND (c.document->'Audiences') ? $3 AND v.model=$4 AND v.dimensions=$5
 ORDER BY v.embedding OPERATOR(public.<=>) $6::public.vector,c.document_id,p.page_number LIMIT 8`, v.catalog.tenant, q.At, q.Audience, v.pin.Model, v.pin.Dimensions, encoded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := []Source{}
	for rows.Next() {
		var document, evidence, approval []byte
		var hash, vectorManifest, vectorPageHash string
		var page Page
		if err = rows.Scan(&document, &evidence, &approval, &hash, &page.Number, &page.Text, &page.Method, &page.ExtractorRevision, &vectorManifest, &vectorPageHash); err != nil {
			return nil, err
		}
		source, e := decodeVectorPage(document, evidence, approval, hash, page)
		if e != nil || vectorManifest != hash || vectorPageHash != pageHash(page) {
			return nil, ErrDenied
		}
		sources = append(sources, source)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sources, nil
}
