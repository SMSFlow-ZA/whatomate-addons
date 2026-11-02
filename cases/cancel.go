package cases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// CancellationReceipt stays inside the protected case payload. EmployeeView
// intentionally exposes only the case reference, state and version.
type CancellationReceipt struct {
	ActorID         string
	KeyHash         string
	ExpectedVersion uint64
	ResultVersion   uint64
}

func cancelDraft(c Case, p Principal, expected uint64, key string, now time.Time) (Case, bool, error) {
	// Check current source authority before either replay or state disclosure.
	if !p.matches(c.ContactID) || !complainant(p, c) || subject(p, c) || !scoped(p, c, SubmitOwn) || !scoped(p, c, ReadOwn) {
		return Case{}, false, ErrDenied
	}
	if expected == 0 || strings.TrimSpace(key) == "" || len(key) > 128 || now.IsZero() {
		return Case{}, false, ErrInvalid
	}
	digest := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(digest[:])
	if c.Cancellation != nil {
		old := c.Cancellation
		if old.ActorID != p.ID || old.KeyHash != keyHash || old.ExpectedVersion != expected {
			return Case{}, false, ErrIdempotencyConflict
		}
		if c.State != Cancelled || c.Version != old.ResultVersion {
			return Case{}, false, ErrConflict
		}
		return clone(c), true, nil
	}
	if c.Version != expected {
		return Case{}, false, ErrConflict
	}
	if c.State != Draft {
		return Case{}, false, ErrInvalidTransition
	}
	next := clone(c)
	next.State = Cancelled
	next.Version++
	next.Cancellation = &CancellationReceipt{ActorID: p.ID, KeyHash: keyHash, ExpectedVersion: expected, ResultVersion: next.Version}
	next.Events = append(next.Events, Event{Kind: "draft_cancelled", Version: next.Version, ActorID: p.ID, From: Draft, To: Cancelled, RecordedAt: now.UTC(), OccurredAt: now.UTC(), OrganizationID: c.OrganizationID, UnitID: c.UnitID})
	return next, false, nil
}

// CancelDraft records a terminal withdrawal of the owner's unsubmitted draft.
// It is neither submission nor erasure and emits no notification intent.
func (s Service) CancelDraft(ctx context.Context, p Principal, id string, expected uint64, key string) (Case, error) {
	if s.Repository == nil {
		return Case{}, ErrInvalid
	}
	tx, e := s.Repository.Begin(ctx)
	if e != nil {
		return Case{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	c, e := tx.Load(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return Case{}, ErrDenied
	}
	if e != nil {
		return Case{}, e
	}
	next, replay, e := cancelDraft(c, p, expected, key, time.Now().UTC())
	if e != nil {
		return Case{}, e
	}
	if !replay {
		if e = tx.Save(ctx, next, expected); e != nil {
			return Case{}, e
		}
	}
	outcome, e := tx.Commit(ctx)
	if outcome != Committed {
		if outcome != RolledBack {
			return Case{}, errors.Join(ErrCommitUnknown, e)
		}
		if e == nil {
			e = errors.New("case transaction rolled back")
		}
		return Case{}, e
	}
	return next, e
}
