package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuthorizedQueueTenantAndCurrentGrant(t *testing.T) {
	db, _ := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if err := MigrateHTTP(ctx, db); err != nil {
		t.Fatal(err)
	}
	metadata := UploadMetadata{DocumentID: "doc", Version: "v1", Filename: "a.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now().UTC()}
	legacy := NewProcessingQueue(db)
	other, err := legacy.enqueue(ctx, "other", "actor", []byte("%PDF-fixture"), metadata)
	if err != nil {
		t.Fatal(err)
	}
	id, err := legacy.enqueue(ctx, "owner", "actor", []byte("%PDF-fixture"), metadata)
	if err != nil {
		t.Fatal(err)
	}
	allowed := false
	checks := 0
	q := NewAuthorizedProcessingQueue(db, "owner", func(ctx context.Context, tx *sql.Tx, j ProcessingJob) error {
		checks++
		if j.TenantID != "owner" || !allowed {
			return ErrDenied
		}
		return nil
	})
	if _, err := q.Claim(ctx, "worker", 8*time.Minute); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied claim: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM addon_knowledge_jobs WHERE id=$1`, id).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("mutated denied claim %s %v", status, err)
	}
	allowed = true
	j, err := q.Claim(ctx, "worker", 8*time.Minute)
	if err != nil || j.ID != id {
		t.Fatalf("tenant claim %s %v", j.ID, err)
	}
	allowed = false
	receipt := ProcessingReceipt{JobID: j.ID, InputHash: j.InputHash, MetadataHash: j.MetadataHash, OutputHash: strings.Repeat("a", 64), ProfileID: "production", ImageDigest: "sha256:" + strings.Repeat("b", 64), DefinitionsRevision: "definitions"}
	if err := q.CompleteWithReceipt(ctx, j, nil, Evidence{}, receipt); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked completion: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM addon_knowledge_jobs WHERE id=$1`, other).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("foreign mutated %s %v", status, err)
	}
	// Safe failure marking is allowed after access revocation; never requeue.
	if err := q.Fail(ctx, j, "ignored private body", false); err != nil {
		t.Fatal(err)
	}
	if checks != 3 {
		t.Fatalf("expected claim+claim+commit authorization, got %d", checks)
	}
}

func TestAuthorizedQueueCannotFallBackWithoutAuthorizer(t *testing.T) {
	db, _ := knowledgeHTTPFixture(t)
	for _, q := range []*ProcessingQueue{NewAuthorizedProcessingQueue(db, "owner", nil), NewAuthorizedProcessingQueue(db, "", func(context.Context, *sql.Tx, ProcessingJob) error { return nil })} {
		if _, err := q.Claim(context.Background(), "worker", time.Minute); !errors.Is(err, ErrDenied) {
			t.Fatalf("unguarded constructor: %v", err)
		}
	}
}

func TestRevokedUploaderQuarantinesOnlyExactJobAndUnblocksNext(t *testing.T) {
	db, _ := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if err := MigrateHTTP(ctx, db); err != nil {
		t.Fatal(err)
	}
	metadata := UploadMetadata{DocumentID: "synthetic", Version: "1", Filename: "fixture.pdf", Audiences: []string{"staff"}, Effective: time.Now().UTC()}
	queue := NewProcessingQueue(db)
	first, err := queue.enqueue(ctx, "owner", "revoked", []byte("%PDF-synthetic"), metadata)
	if err != nil {
		t.Fatal(err)
	}
	next, err := queue.enqueue(ctx, "owner", "active", []byte("%PDF-synthetic"), metadata)
	if err != nil {
		t.Fatal(err)
	}
	transient := errors.New("temporary authorization store outage")
	q := NewAuthorizedProcessingQueue(db, "owner", func(context.Context, *sql.Tx, ProcessingJob) error { return transient })
	if _, err = q.Claim(ctx, "worker", time.Minute); !errors.Is(err, transient) {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow(`SELECT status FROM addon_knowledge_jobs WHERE id=$1`, first).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("transient changed job: %s %v", state, err)
	}
	q = NewAuthorizedProcessingQueue(db, "owner", func(_ context.Context, _ *sql.Tx, j ProcessingJob) error {
		if j.ActorID == "revoked" {
			return ErrProcessingAuthorizationRevoked
		}
		return nil
	})
	if _, err = q.Claim(ctx, "worker", time.Minute); !errors.Is(err, ErrProcessingAuthorizationRevoked) {
		t.Fatalf("revocation: %v", err)
	}
	var reason string
	var clean bool
	if err = db.QueryRow(`SELECT status,failure,evidence IS NOT NULL FROM addon_knowledge_jobs WHERE id=$1`, first).Scan(&state, &reason, &clean); err != nil || state != "failed" || reason != "authorization_revoked" || clean {
		t.Fatalf("invalid quarantine %s %s %v %v", state, reason, clean, err)
	}
	claimed, err := q.Claim(ctx, "worker", time.Minute)
	if err != nil || claimed.ID != next {
		t.Fatalf("next authorized job blocked: %s %v", claimed.ID, err)
	}
}
