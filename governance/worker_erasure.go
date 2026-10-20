package governance

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// WorkerErasureReceipt contains no path, document bytes or controller secrets.
// It is returned ONLY by a trusted controller closure after independent expected
// identity checks, actual bounded erasure and durable receipt/absence verification.
type WorkerErasureReceipt struct {
	JobID, TenantID, ActorID, InputHash, ManifestHash, ReceiptHash string
	DeletedAt                                                      time.Time
}

// WorkerErasureController must validate expected original tenant/uploader/input
// identity against authoritative pre-erasure records; current source-owner grants
// against the original audiences; immutable worker manifest/receipt; durable
// controller receipt; and actual absence of exactly that job directory. It must
// support safe reconciliation of an unknown prior SQL commit without blind retry.
// Never construct it from a browser assertion or expose it as a web endpoint.
type WorkerErasureController func(context.Context, api.Actor, Record) (WorkerErasureReceipt, error)

func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	raw, e := hex.DecodeString(s)
	return e == nil && len(raw) == 32
}

// CompleteWorkerErasure holds the same scope lock as legal-hold changes while
// invoking the trusted owner. Filesystem removal cannot roll back with SQL: on
// an unknown/failed commit, keep the application unavailable and reconcile the
// durable controller receipt/absence before recording a confirmed journal result.
func (s PhysicalService) CompleteWorkerErasure(ctx context.Context, a api.Actor, scope Scope, requestID, recordID string, expected uint64, controller WorkerErasureController) (JournalEntry, error) {
	if controller == nil || expected == 0 {
		return JournalEntry{}, ErrInvalid
	}
	var result JournalEntry
	e := s.within(ctx, a, scope, DeletionRecord, func(t *sqlTransaction) error {
		req, e := get[SubjectRequest](ctx, t, requestKind, requestID)
		if e != nil {
			return e
		}
		if !req.Approved || req.Kind != Delete {
			return ErrApproval
		}
		r, e := get[Record](ctx, t, recordKind, recordID)
		if e != nil {
			return e
		}
		if r.Source != WorkerFile || r.SubjectID != req.SubjectID {
			return ErrDenied
		}
		entry, e := get[JournalEntry](ctx, t, journalKind, journalID(requestID, recordID))
		if e != nil {
			return e
		}
		if entry.Version != expected || entry.Generation != r.Generation {
			return ErrConflict
		}
		rules, e := list[Rule](ctx, t, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, t, holdKind)
		if e != nil {
			return e
		}
		switch stateFor(r, rules, holds, time.Now().UTC()) {
		case BlockedHold:
			return ErrHold
		case BlockedPolicy, BlockedRetention:
			return ErrRetention
		case AwaitingExpiry:
		default:
			return ErrInvalid
		}
		// The controller is invoked only after authorization and hold checks, inside
		// their serialized scope transaction. It must itself reverify real absence.
		receipt, e := controller(ctx, a, r)
		if e != nil {
			return e
		}
		if receipt.JobID != r.Reference || receipt.TenantID != scope.OrganizationID || receipt.ActorID == "" || !sha256Hex(receipt.InputHash) || !sha256Hex(receipt.ManifestHash) || !sha256Hex(receipt.ReceiptHash) || receipt.DeletedAt.IsZero() || receipt.DeletedAt.After(time.Now().Add(time.Minute)) {
			return ErrInvalid
		}
		receiptKey := fmt.Sprintf("%s:%d", entry.ID, entry.Generation)
		prior, priorError := get[WorkerErasureReceipt](ctx, t, "worker_erasure_receipt", receiptKey)
		if priorError == nil && prior.ReceiptHash != receipt.ReceiptHash {
			return ErrConflict
		}
		if priorError != nil && priorError != ErrNotFound {
			return priorError
		}
		if e = put(ctx, t, "worker_erasure_receipt", receiptKey, receipt); e != nil {
			return e
		}
		entry.State = RecordedApplied
		entry.ConfirmationReference = "worker-receipt:" + receipt.ReceiptHash
		entry.Version++
		entry.UpdatedAt = time.Now().UTC()
		if e = put(ctx, t, journalKind, entry.ID, entry); e != nil {
			return e
		}
		if e = audit(ctx, t, PrincipalForActor(a), scope, "worker.erasure.verified", entry.ID); e != nil {
			return e
		}
		result = entry
		return nil
	})
	if e != nil {
		return JournalEntry{}, e
	}
	return result, nil
}
