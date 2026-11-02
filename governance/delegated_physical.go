package governance

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EraseDelegatedTx performs actual source-owned physical erasure. It never
// substitutes a scheduled reminder or changes a worker actor's authentication.
// Caller commits source/journals/delegation receipt atomically or rolls back all.
func (s PhysicalService) EraseDelegatedTx(ctx context.Context, tx *sql.Tx, permit *DelegatedAuthority) ([]JournalEntry, error) {
	actors := permit.Actors()
	if len(actors) != 2 {
		return nil, ErrDenied
	}
	a := actors[0]
	requests := permit.Requests()
	subject := permit.Subject()
	type pending struct {
		record  Record
		entry   JournalEntry
		t       *sqlTransaction
		ctx     context.Context
		adapter DelegatedPhysicalAdapter
	}
	work := []pending{}
	all := []Record{}
	for _, request := range requests {
		if e := permit.CheckDiscover(ctx, tx, a, request.Scope, subject); e != nil {
			return nil, e
		}
		t := &sqlTransaction{tx: tx, scope: request.Scope}
		req, e := get[SubjectRequest](ctx, t, requestKind, request.RequestID)
		if e != nil {
			return nil, e
		}
		if !req.Approved || req.Kind != Delete || req.SubjectID != subject || req.Scope != request.Scope {
			return nil, ErrApproval
		}
		scoped, e := s.subjectContext(ctx, a.OrganizationID, subject)
		if e != nil {
			return nil, e
		}
		records := []Record{}
		for _, source := range s.Adapters {
			adapter, ok := source.(DelegatedPhysicalAdapter)
			if !ok {
				return nil, ErrDenied
			}
			current, e := adapter.DiscoverDelegatedTx(scoped, tx, a, request.Scope, subject, permit)
			if e != nil {
				return nil, e
			}
			for _, r := range current {
				if ValidateRecord(r) != nil || r.Scope != request.Scope || r.SubjectID != subject || !strings.HasPrefix(r.ID, source.Name()+":") {
					return nil, ErrInvalid
				}
				prior, e := get[Record](ctx, t, recordKind, r.ID)
				if e == nil {
					if prior.Reference != r.Reference || prior.SubjectID != r.SubjectID || prior.Scope != r.Scope || !sameHoldLineages(prior, r) {
						return nil, ErrConflict
					}
					r = prior
				} else if e != ErrNotFound {
					return nil, e
				}
				records = append(records, r)
			}
		}
		rules, e := list[Rule](ctx, t, ruleKind)
		if e != nil {
			return nil, e
		}
		holds, e := list[Hold](ctx, t, holdKind)
		if e != nil {
			return nil, e
		}
		entries, e := Plan(req, records, rules, holds, time.Now().UTC())
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if entry.State == BlockedHold {
				return nil, ErrHold
			}
			if entry.State == BlockedPolicy || entry.State == BlockedRetention {
				return nil, ErrRetention
			}
			for _, r := range records {
				if r.ID == entry.RecordID {
					adapter, ok := s.adapter(r).(DelegatedPhysicalAdapter)
					if !ok {
						return nil, ErrDenied
					}
					work = append(work, pending{r, entry, t, scoped, adapter})
					break
				}
			}
		}
		all = append(all, records...)
	}
	if e := permit.CheckCurrentPlan(ctx, tx, all); e != nil {
		return nil, e
	}
	rank := func(r Record) int {
		if r.Source == WorkerFile {
			return -1
		}
		if r.Source == Core || r.Source == Directory {
			return 2
		}
		if strings.HasPrefix(r.ID, "cases:") {
			return 1
		}
		return 0
	}
	sort.SliceStable(work, func(i, j int) bool { return rank(work[i].record) < rank(work[j].record) })
	out := []JournalEntry{}
	for _, item := range work {
		if e := permit.CheckErase(item.ctx, tx, a, item.record); e != nil {
			return nil, e
		}
		old, e := get[JournalEntry](item.ctx, item.t, journalKind, item.entry.ID)
		if e != nil && e != ErrNotFound {
			return nil, e
		}
		if e == nil && old.Generation == item.entry.Generation && old.State == RecordedApplied {
			out = append(out, old)
			continue
		}
		entry := item.entry
		if e == nil {
			entry.Version = old.Version + 1
		}

		if item.record.Source == WorkerFile && entry.State == AwaitingExpiry {
			if s.DelegatedWorkerErasure == nil {
				return nil, ErrExternalPending
			}
			receipt, e := s.DelegatedWorkerErasure(item.ctx, tx, a, item.record, permit)
			if e != nil {
				return nil, e
			}
			if receipt.TenantID != item.record.Scope.OrganizationID || receipt.JobID != item.record.Reference || receipt.ActorID == "" || !sha256Hex(receipt.InputHash) || !sha256Hex(receipt.ManifestHash) || !sha256Hex(receipt.ReceiptHash) || receipt.DeletedAt.IsZero() || receipt.DeletedAt.After(time.Now().Add(time.Minute)) {
				return nil, ErrInvalid
			}
			receiptKey := fmt.Sprintf("%s:%d", entry.ID, entry.Generation)
			old, err := get[WorkerErasureReceipt](item.ctx, item.t, "worker_erasure_receipt", receiptKey)
			if err != nil && err != ErrNotFound {
				return nil, err
			}
			if err == nil && old.ReceiptHash != receipt.ReceiptHash {
				return nil, ErrConflict
			}
			if e = put(item.ctx, item.t, "worker_erasure_receipt", receiptKey, receipt); e != nil {
				return nil, e
			}
			entry.State = RecordedApplied
			entry.ConfirmationReference = "worker-receipt:" + receipt.ReceiptHash
		}
		if entry.State == Pending {
			if e = item.adapter.EraseDelegatedTx(item.ctx, tx, a, item.record, permit); e != nil {
				return nil, e
			}
			entry.State = RecordedApplied
			entry.ConfirmationReference = "delegated-sql-transaction:" + entry.ID
		}
		if e = put(item.ctx, item.t, journalKind, entry.ID, entry); e != nil {
			return nil, e
		}
		out = append(out, entry)
	}
	for _, r := range requests {
		if e := audit(ctx, &sqlTransaction{tx: tx, scope: r.Scope}, PrincipalForActor(a), r.Scope, "deletion.physical.delegated", r.RequestID); e != nil {
			return nil, e
		}
	}
	return out, nil
}
