package governance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// ScopedDeletion references an independently approved existing scoped request.
type ScopedDeletion struct {
	Scope     Scope
	RequestID string
}

// LockScopesTx serializes current policy, holds and journals across scopes.
// Call before source locks. The caller owns the supplied transaction and MUST
// rollback on any subsequent error. It must never perform external side effects.
func LockScopesTx(ctx context.Context, tx *sql.Tx, scopes []Scope) error {
	if tx == nil || len(scopes) == 0 || len(scopes) > 32 {
		return ErrInvalid
	}
	ordered := append([]Scope(nil), scopes...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].OrganizationID == ordered[j].OrganizationID {
			return ordered[i].UnitID < ordered[j].UnitID
		}
		return ordered[i].OrganizationID < ordered[j].OrganizationID
	})
	for i, scope := range ordered {
		if !scope.valid() || scope.UnitID == "*" || (i > 0 && scope == ordered[i-1]) {
			return ErrInvalid
		}
		payload, _ := json.Marshal([3]string{"governance", scope.OrganizationID, scope.UnitID})
		hash := sha256.Sum256(payload)
		if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(binary.BigEndian.Uint64(hash[:8]))); e != nil {
			return e
		}
	}
	return nil
}

// DiscoverTx is the supplied-transaction counterpart of Discover. It preserves
// owner policy checks and inventory validation. The caller owns commit/rollback.
func (s PhysicalService) DiscoverTx(ctx context.Context, tx *sql.Tx, a api.Actor, scope Scope, subject string) ([]Record, error) {
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil || a.OrganizationID != scope.OrganizationID || !a.Allows(string(InventoryWrite), scope.UnitID) || !validReference(subject) {
		return nil, ErrDenied
	}
	if e := LockScopesTx(ctx, tx, []Scope{scope}); e != nil {
		return nil, e
	}
	var e error
	ctx, e = s.subjectContext(ctx, a.OrganizationID, subject)
	if e != nil {
		return nil, e
	}
	t := &sqlTransaction{tx: tx, scope: scope}
	out, e := s.discoverTx(ctx, t, a, subject)
	if e != nil {
		return nil, e
	}
	return out, audit(ctx, t, PrincipalForActor(a), scope, "inventory.discover", subject)
}

func (s PhysicalService) discoverTx(ctx context.Context, t *sqlTransaction, a api.Actor, subject string) ([]Record, error) {
	var out []Record
	for _, adapter := range s.Adapters {
		records, e := adapter.Discover(ctx, t.tx, a, t.scope, subject)
		if e != nil {
			return nil, e
		}
		for _, r := range records {
			if ValidateRecord(r) != nil || r.Scope != t.scope || r.SubjectID != subject || !strings.HasPrefix(r.ID, adapter.Name()+":") {
				return nil, ErrInvalid
			}
			old, e := get[Record](ctx, t, recordKind, r.ID)
			if e == nil {
				if old.Scope != r.Scope || old.SubjectID != r.SubjectID || old.Reference != r.Reference || !sameHoldLineages(old, r) || old.SourceRevision != r.SourceRevision {
					return nil, ErrConflict
				}
				r = old
			} else if e != ErrNotFound {
				return nil, e
			} else if e = put(ctx, t, recordKind, r.ID, r); e != nil {
				return nil, e
			}
			out = append(out, r)
		}
	}
	for _, r := range out {
		if r.LineageID != r.ID {
			root, e := get[Record](ctx, t, recordKind, r.LineageID)
			if e != nil {
				return nil, e
			}
			if root.Scope != r.Scope || root.SubjectID != r.SubjectID || root.LineageID != root.ID {
				return nil, ErrInvalid
			}
		}
	}
	return out, nil
}

// EraseRequestsTx checks EVERY scoped approval, current owner discovery,
// retention rule and hold before any physical erasure. Source changes and all
// journals share the caller's transaction. It never commits. On error the caller
// must rollback. Scope locks must be obtained before any caller source locks.
// Caller authorization must additionally bind the complete affected scope set;
// this generic port cannot infer application-specific shared identity ownership.
func (s PhysicalService) EraseRequestsTx(ctx context.Context, tx *sql.Tx, a api.Actor, requests []ScopedDeletion) ([]JournalEntry, error) {
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil {
		return nil, ErrDenied
	}
	scopes := make([]Scope, len(requests))
	for i, r := range requests {
		scopes[i] = r.Scope
		if r.Scope.OrganizationID != a.OrganizationID || !a.Allows(string(DeletionRecord), r.Scope.UnitID) || !validReference(r.RequestID) {
			return nil, ErrDenied
		}
	}
	if e := LockScopesTx(ctx, tx, scopes); e != nil {
		return nil, e
	}
	type item struct {
		t      *sqlTransaction
		ctx    context.Context
		record Record
		entry  JournalEntry
	}
	items := []item{}
	subject := ""
	for _, request := range requests {
		t := &sqlTransaction{tx: tx, scope: request.Scope}
		req, e := get[SubjectRequest](ctx, t, requestKind, request.RequestID)
		if e != nil {
			return nil, e
		}
		if !req.Approved || req.Kind != Delete {
			return nil, ErrApproval
		}
		if req.Scope != request.Scope || req.SubjectID == "" || (subject != "" && subject != req.SubjectID) {
			return nil, ErrInvalid
		}
		subject = req.SubjectID
		scopedCtx, e := s.subjectContext(ctx, a.OrganizationID, req.SubjectID)
		if e != nil {
			return nil, e
		}
		if _, e = s.discoverTx(scopedCtx, t, a, req.SubjectID); e != nil {
			return nil, e
		}
		records, e := list[Record](scopedCtx, t, recordKind)
		if e != nil {
			return nil, e
		}
		rules, e := list[Rule](scopedCtx, t, ruleKind)
		if e != nil {
			return nil, e
		}
		holds, e := list[Hold](scopedCtx, t, holdKind)
		if e != nil {
			return nil, e
		}
		entries, e := Plan(req, records, rules, holds, time.Now().UTC())
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			switch entry.State {
			case BlockedHold:
				return nil, ErrHold
			case BlockedPolicy, BlockedRetention:
				return nil, ErrRetention
			}
			for _, record := range records {
				if record.ID == entry.RecordID {
					items = append(items, item{t, scopedCtx, record, entry})
					break
				}
			}
		}
	}
	// All derivative checks precede parent erasure across the entire batch.
	// Shared core identity and directory rows are erased last.
	rank := func(r Record) int {
		if r.Source == Core || r.Source == Directory {
			return 2
		}
		if strings.HasPrefix(r.ID, "cases:") {
			return 1
		}
		return 0
	}
	sort.SliceStable(items, func(i, j int) bool { return rank(items[i].record) < rank(items[j].record) })
	out := []JournalEntry{}
	for _, v := range items {
		entry := v.entry
		old, e := get[JournalEntry](v.ctx, v.t, journalKind, entry.ID)
		if e != nil && e != ErrNotFound {
			return nil, e
		}
		if e == nil && old.Generation == entry.Generation && old.State == RecordedApplied {
			out = append(out, old)
			continue
		}
		if e == nil {
			entry.Version = old.Version + 1
		}
		adapter := s.adapter(v.record)
		if entry.State == Pending && adapter != nil {
			if e = adapter.Erase(v.ctx, tx, a, v.record); e != nil {
				return nil, e
			}
			entry.State = RecordedApplied
			entry.ConfirmationReference = "sql-transaction:" + entry.ID
		} else if entry.State == Pending {
			entry.State = AwaitingExpiry
		}
		if e = put(v.ctx, v.t, journalKind, entry.ID, entry); e != nil {
			return nil, e
		}
		out = append(out, entry)
	}
	for _, r := range requests {
		if e := audit(ctx, &sqlTransaction{tx: tx, scope: r.Scope}, PrincipalForActor(a), r.Scope, "deletion.physical.coordinated", r.RequestID); e != nil {
			return nil, e
		}
	}
	return out, nil
}

// ReconcileRequestsTx resets restored generations and applies all scoped
// deletions atomically. This is an offline-only trusted port; the caller must
// validate independent current journals and keep the target inaccessible.
func (s PhysicalService) ReconcileRequestsTx(ctx context.Context, tx *sql.Tx, a api.Actor, requests []ScopedDeletion, subject, restoreID string) ([]JournalEntry, error) {
	if !validReference(subject) || !validReference(restoreID) || len(restoreID) > 128 {
		return nil, ErrInvalid
	}
	scopes := make([]Scope, len(requests))
	for i, r := range requests {
		scopes[i] = r.Scope
		if a.OrganizationID != r.Scope.OrganizationID || !a.Allows(string(RestoreReconcile), r.Scope.UnitID) || !a.Allows(string(DeletionRecord), r.Scope.UnitID) {
			return nil, ErrDenied
		}
	}
	if e := LockScopesTx(ctx, tx, scopes); e != nil {
		return nil, e
	}
	for _, request := range requests {
		t := &sqlTransaction{tx: tx, scope: request.Scope}
		req, e := get[SubjectRequest](ctx, t, requestKind, request.RequestID)
		if e != nil {
			return nil, e
		}
		if req.SubjectID != subject || !req.Approved || req.Kind != Delete {
			return nil, ErrApproval
		}
		records, e := list[Record](ctx, t, recordKind)
		if e != nil {
			return nil, e
		}
		for _, r := range records {
			if r.SubjectID != subject {
				continue
			}
			payload, _ := json.Marshal([2]string{restoreID, r.ID})
			sum := sha256.Sum256(payload)
			key := fmt.Sprintf("subject-restore:%x", sum)
			prior, e := get[restoreKey](ctx, t, restoreKind, key)
			if e == nil {
				if prior.RecordID != r.ID {
					return nil, ErrConflict
				}
				continue
			}
			if e != ErrNotFound {
				return nil, e
			}
			r.Generation++
			if e = put(ctx, t, recordKind, r.ID, r); e != nil {
				return nil, e
			}
			if e = put(ctx, t, restoreKind, key, restoreKey{RecordID: r.ID}); e != nil {
				return nil, e
			}
		}
		if e = audit(ctx, t, PrincipalForActor(a), request.Scope, "restore.subject.coordinated", request.RequestID); e != nil {
			return nil, e
		}
	}
	return s.EraseRequestsTx(ctx, tx, a, requests)
}
