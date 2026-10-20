package knowledge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxUploadBytes = 20 << 20

type UploadMetadata struct {
	DocumentID string    `json:"document_id"`
	Version    string    `json:"version"`
	Filename   string    `json:"filename"`
	MediaType  string    `json:"media_type"`
	Audiences  []string  `json:"audiences"`
	Effective  time.Time `json:"effective_at"`
	WithdrawAt time.Time `json:"withdraw_at,omitempty"`
}

// ProcessingJob is a trusted controller port, never an HTTP input. Claim reads
// immutable bytes from quarantine. LeaseToken authorizes exactly one completion.
type ProcessingJob struct {
	ID, TenantID, ActorID, InputHash, MetadataHash, LeaseToken string
	Input                                                      []byte
	Metadata                                                   UploadMetadata
}
type ProcessingQueue struct{ db *sql.DB }

func NewProcessingQueue(db *sql.DB) *ProcessingQueue { return &ProcessingQueue{db: db} }
func digest(b []byte) string                         { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func randomID() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func MigrateHTTP(ctx context.Context, db *sql.DB) error {
	r, e := NewPostgresRepository(db, "migration")
	if e != nil {
		return e
	}
	if e = r.Migrate(ctx); e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_knowledge_jobs (
 id text PRIMARY KEY,tenant text NOT NULL,actor text NOT NULL,input bytea NOT NULL,input_hash text NOT NULL,
 metadata jsonb NOT NULL,metadata_hash text NOT NULL,status text NOT NULL DEFAULT 'queued',version bigint NOT NULL DEFAULT 1,
 lease_token text,lease_until timestamptz,worker text,receipt jsonb,document jsonb,evidence jsonb,approval jsonb,
 failure text,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(status IN ('queued','processing','processed','approved','published','failed','unknown','retired')));
 ALTER TABLE addon_knowledge_jobs ADD COLUMN IF NOT EXISTS receipt jsonb;
 CREATE INDEX IF NOT EXISTS addon_knowledge_jobs_pending ON addon_knowledge_jobs(status,created_at);`)
	return e
}
func validMetadata(m UploadMetadata) bool {
	if m.DocumentID == "" || len(m.DocumentID) > 200 || m.Version == "" || len(m.Version) > 100 || m.Filename == "" || len(m.Filename) > 255 || len(m.MediaType) > 100 || m.Effective.IsZero() || len(m.Audiences) == 0 || len(m.Audiences) > 32 || (!m.WithdrawAt.IsZero() && !m.WithdrawAt.After(m.Effective)) {
		return false
	}
	seen := map[string]bool{}
	for _, a := range m.Audiences {
		if a == "" || a == "*" || len(a) > 200 || seen[a] {
			return false
		}
		seen[a] = true
	}
	return true
}
func (q *ProcessingQueue) enqueue(ctx context.Context, tenant, actor string, input []byte, m UploadMetadata) (string, error) {
	if q.db == nil || tenant == "" || actor == "" || len(input) == 0 || len(input) > MaxUploadBytes || !validMetadata(m) {
		return "", ErrDenied
	}
	b, _ := json.Marshal(m)
	id := randomID()
	_, e := q.db.ExecContext(ctx, `INSERT INTO addon_knowledge_jobs(id,tenant,actor,input,input_hash,metadata,metadata_hash)VALUES($1,$2,$3,$4,$5,$6,$7)`, id, tenant, actor, input, digest(input), b, digest(b))
	return id, e
}

// Expired claims become unknown, never queued: a crashed controller's worker
// outcome cannot be inferred, and automatic replay may hide an unknown outcome.
func (q *ProcessingQueue) Claim(ctx context.Context, worker string, lease time.Duration) (ProcessingJob, error) {
	if q.db == nil || worker == "" || lease < time.Second || lease > 15*time.Minute {
		return ProcessingJob{}, ErrDenied
	}
	tx, e := q.db.BeginTx(ctx, nil)
	if e != nil {
		return ProcessingJob{}, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE addon_knowledge_jobs SET status='unknown',version=version+1,failure='lease_expired',updated_at=clock_timestamp() WHERE status='processing' AND lease_until<=clock_timestamp()`); e != nil {
		return ProcessingJob{}, e
	}
	var j ProcessingJob
	var metadata []byte
	e = tx.QueryRowContext(ctx, `SELECT id,tenant,actor,input,input_hash,metadata,metadata_hash FROM addon_knowledge_jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&j.ID, &j.TenantID, &j.ActorID, &j.Input, &j.InputHash, &metadata, &j.MetadataHash)
	if errors.Is(e, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return ProcessingJob{}, err
		}
		return ProcessingJob{}, sql.ErrNoRows
	}
	if e != nil {
		return ProcessingJob{}, e
	}
	if json.Unmarshal(metadata, &j.Metadata) != nil {
		return ProcessingJob{}, ErrDenied
	}
	canonical, _ := json.Marshal(j.Metadata)
	if digest(j.Input) != j.InputHash || digest(canonical) != j.MetadataHash {
		return ProcessingJob{}, ErrDenied
	}
	j.LeaseToken = randomID()
	_, e = tx.ExecContext(ctx, `UPDATE addon_knowledge_jobs SET status='processing',worker=$2,lease_token=$3,lease_until=clock_timestamp()+($4 * interval '1 second'),version=version+1,updated_at=clock_timestamp() WHERE id=$1`, j.ID, worker, j.LeaseToken, int(lease.Seconds()))
	if e != nil {
		return ProcessingJob{}, e
	}
	if e = tx.Commit(); e != nil {
		return ProcessingJob{}, e
	}
	return j, nil
}
func (q *ProcessingQueue) lockedJob(ctx context.Context, tx *sql.Tx, j ProcessingJob) (UploadMetadata, []byte, error) {
	var input, metadata []byte
	var ih, mh string
	e := tx.QueryRowContext(ctx, `SELECT input,input_hash,metadata,metadata_hash FROM addon_knowledge_jobs WHERE id=$1 AND tenant=$2 AND actor=$3 AND status='processing' AND lease_token=$4 AND lease_until>clock_timestamp() FOR UPDATE`, j.ID, j.TenantID, j.ActorID, j.LeaseToken).Scan(&input, &ih, &metadata, &mh)
	if errors.Is(e, sql.ErrNoRows) {
		return UploadMetadata{}, nil, ErrDenied
	}
	if e != nil {
		return UploadMetadata{}, nil, e
	}
	var m UploadMetadata
	if json.Unmarshal(metadata, &m) != nil {
		return m, nil, ErrDenied
	}
	canonical, _ := json.Marshal(m)
	submitted, _ := json.Marshal(j.Metadata)
	if ih != j.InputHash || mh != j.MetadataHash || digest(input) != ih || digest(j.Input) != ih || digest(canonical) != mh || digest(submitted) != mh {
		return m, nil, ErrDenied
	}
	return m, input, nil
}

// Complete accepts only trusted worker output after the controller verifies its
// signed/pinned worker receipt. The web handler has no completion route.
func (q *ProcessingQueue) Complete(ctx context.Context, j ProcessingJob, pages []Page, e Evidence) error {
	return q.complete(ctx, j, pages, e, nil)
}
func (q *ProcessingQueue) complete(ctx context.Context, j ProcessingJob, pages []Page, e Evidence, receipt *ProcessingReceipt) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, input, err := q.lockedJob(ctx, tx, j)
	if err != nil {
		return err
	}
	total := 0
	for _, p := range pages {
		total += len(p.Text)
	}
	if total > 20<<20 || len(pages) > 10000 {
		return ErrDenied
	}
	d := Document{ID: m.DocumentID, Version: m.Version, Content: input, ContentHash: j.InputHash, Audiences: m.Audiences, Effective: m.Effective, WithdrawAt: m.WithdrawAt, Pages: pages}
	if _, err = Publish(d, e, Approval{Reviewer: "worker-validation", ManifestHash: ManifestHash(d)}); err != nil {
		return err
	}
	receiptJSON, _ := json.Marshal(receipt)
	doc, _ := json.Marshal(d)
	ev, _ := json.Marshal(e)
	_, err = tx.ExecContext(ctx, `UPDATE addon_knowledge_jobs SET status='processed',document=$2,evidence=$3,receipt=$4,version=version+1,lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`, j.ID, doc, ev, receiptJSON)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (q *ProcessingQueue) Fail(ctx context.Context, j ProcessingJob, reason string, unknown bool) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err = q.lockedJob(ctx, tx, j); err != nil {
		return err
	}
	// Do not persist worker stdout/errors, which can contain extracted content.
	code := "processing_failed"
	if unknown {
		code = "outcome_unknown"
	}
	_ = reason
	status := "failed"
	if unknown {
		status = "unknown"
	}
	_, err = tx.ExecContext(ctx, `UPDATE addon_knowledge_jobs SET status=$2,failure=$3,version=version+1,lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`, j.ID, status, code)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ProcessingReceipt retains the controller-verified worker identity and immutable
// linkage. MetadataHash is the knowledge UploadMetadata hash; the controller
// separately verifies the worker's format-specific metadata hash.
type ProcessingReceipt struct {
	JobID               string `json:"job_id"`
	InputHash           string `json:"input_hash"`
	MetadataHash        string `json:"metadata_hash"`
	OutputHash          string `json:"output_hash"`
	ProfileID           string `json:"profile_id"`
	ImageDigest         string `json:"image_digest"`
	DefinitionsRevision string `json:"definitions_revision"`
}

func (q *ProcessingQueue) CompleteWithReceipt(ctx context.Context, j ProcessingJob, pages []Page, e Evidence, receipt ProcessingReceipt) error {
	imageAt := strings.LastIndex(receipt.ImageDigest, "@sha256:")
	imageHash := ""
	if imageAt > 0 {
		imageHash = receipt.ImageDigest[imageAt+8:]
	} else if strings.HasPrefix(receipt.ImageDigest, "sha256:") {
		imageHash = strings.TrimPrefix(receipt.ImageDigest, "sha256:")
	}
	_, outputErr := hex.DecodeString(receipt.OutputHash)
	_, imageErr := hex.DecodeString(imageHash)
	if receipt.JobID != j.ID || receipt.InputHash != j.InputHash || receipt.MetadataHash != j.MetadataHash || len(receipt.OutputHash) != 64 || outputErr != nil || receipt.ProfileID == "" || len(imageHash) != 64 || imageErr != nil || receipt.DefinitionsRevision == "" {
		return ErrDenied
	}
	return q.complete(ctx, j, pages, e, &receipt)
}
