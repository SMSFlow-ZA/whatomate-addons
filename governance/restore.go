package governance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// ReconcileSubjectRestore is a trusted OFFLINE restore gate, not authentication.
// The host must validate the original signed live session, MFA generation and
// current grants before supplying actor. Restore the independently authoritative
// newer governance journal before restoring/accessing business data. This method
// never manufactures missing authority or approves a request.
//
// All generation/journal resets commit together. Physical erasure then rechecks
// current holds and policies in its own transaction. Any error means the restore
// must remain unavailable; pending external copies still require their owner.
func (s PhysicalService) ReconcileSubjectRestore(ctx context.Context, a api.Actor, scope Scope, subjectID, requestID, restoreID string) ([]JournalEntry, error) {
	if !validReference(subjectID) || !validReference(requestID) || !validReference(restoreID) || len(restoreID) > 128 {
		return nil, ErrInvalid
	}
	if !a.Allows(string(DeletionRecord), scope.UnitID) {
		return nil, ErrDenied
	}
	e := s.within(ctx, a, scope, RestoreReconcile, func(t *sqlTransaction) error {
		selected, e := get[SubjectRequest](ctx, t, requestKind, requestID)
		if e != nil {
			return e
		}
		if selected.SubjectID != subjectID {
			return ErrDenied
		}
		if !selected.Approved || selected.Kind != Delete {
			return ErrApproval
		}
		records, e := list[Record](ctx, t, recordKind)
		if e != nil {
			return e
		}
		rules, e := list[Rule](ctx, t, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, t, holdKind)
		if e != nil {
			return e
		}
		requests, e := list[SubjectRequest](ctx, t, requestKind)
		if e != nil {
			return e
		}
		found := false
		for _, r := range records {
			if r.SubjectID != subjectID {
				continue
			}
			found = true
			payload, _ := json.Marshal([2]string{restoreID, r.ID})
			sum := sha256.Sum256(payload)
			key := fmt.Sprintf("subject-restore:%x", sum)
			prior, e := get[restoreKey](ctx, t, restoreKind, key)
			if e == nil {
				if prior.RecordID != r.ID {
					return ErrConflict
				}
				continue
			}
			if e != ErrNotFound {
				return e
			}
			r.Generation++
			if e = put(ctx, t, recordKind, r.ID, r); e != nil {
				return e
			}
			for _, req := range requests {
				if req.SubjectID != subjectID || req.Kind != Delete || !req.Approved {
					continue
				}
				entries, e := Plan(req, []Record{r}, rules, holds, time.Now().UTC())
				if e != nil {
					return e
				}
				for _, entry := range entries {
					old, e := get[JournalEntry](ctx, t, journalKind, entry.ID)
					if e == nil {
						entry.Version = old.Version + 1
					} else if e != ErrNotFound {
						return e
					}
					if e = put(ctx, t, journalKind, entry.ID, entry); e != nil {
						return e
					}
				}
			}
			if e = put(ctx, t, restoreKind, key, restoreKey{RecordID: r.ID}); e != nil {
				return e
			}
		}
		if !found {
			return ErrNotFound
		}
		return audit(ctx, t, PrincipalForActor(a), scope, "restore.subject.reconcile", requestID)
	})
	if e != nil {
		return nil, e
	}
	return s.EraseRequest(ctx, a, scope, requestID)
}
