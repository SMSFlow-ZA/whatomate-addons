package governance

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// CompletedSourceAdapter distinguishes actual erased source state from a source
// omitted merely because the current reader lost permission. Unknown owners deny.
type CompletedSourceAdapter interface {
	CompletedSourceAbsentTx(context.Context, *sql.Tx, api.Actor, Record) (bool, error)
}

// DiscoverRequestInventoryTx is the current requester projection. Discovery may
// register newly visible sources, but saved pending sources cannot disappear
// merely because the current requester lost domain permission.
func (s PhysicalService) DiscoverRequestInventoryTx(ctx context.Context, tx *sql.Tx, a api.Actor, scope Scope, subject, requestID string) ([]Record, error) {
	if tx == nil || !a.MFA || a.SessionID == "" || a.OrganizationID != scope.OrganizationID || !a.Allows(string(DeleteRequest), scope.UnitID) || !a.Allows(string(DeletionRecord), scope.UnitID) || !a.Allows(string(JournalRead), scope.UnitID) {
		return nil, ErrDenied
	}
	if e := LockScopesTx(ctx, tx, []Scope{scope}); e != nil {
		return nil, e
	}
	t := &sqlTransaction{tx: tx, scope: scope}
	req, e := get[SubjectRequest](ctx, t, requestKind, requestID)
	if e != nil {
		return nil, e
	}
	if req.SubjectID != subject || req.Scope != scope || req.Kind != Delete || !req.Approved {
		return nil, ErrApproval
	}
	current, e := s.DiscoverTx(ctx, tx, a, scope, subject)
	if e != nil {
		return nil, e
	}
	return s.completedInventory(ctx, t, a, req, current)
}

// Reconcile only immutable historical inventory from this exact approved
// request. Missing pending sources remain a conflict; no source is rediscovered,
// registered, or authorized for a new mutation by a completion receipt.
func (s PhysicalService) completedInventory(ctx context.Context, t *sqlTransaction, a api.Actor, request SubjectRequest, current []Record) ([]Record, error) {
	saved, e := list[Record](ctx, t, recordKind)
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, r := range current {
		seen[r.ID] = true
	}
	out := append([]Record(nil), current...)
	for _, r := range saved {
		if r.SubjectID != request.SubjectID || seen[r.ID] {
			continue
		}
		prior, e := get[JournalEntry](ctx, t, journalKind, journalID(request.ID, r.ID))
		if e == ErrNotFound {
			return nil, ErrConflict
		}
		if e != nil {
			return nil, e
		}
		if !appliedInventoryReceipt(request, r, prior) {
			return nil, ErrConflict
		}
		owner, ok := s.adapter(r).(CompletedSourceAdapter)
		if !ok {
			return nil, ErrDenied
		}
		absent, e := owner.CompletedSourceAbsentTx(ctx, t.tx, a, r)
		if e != nil {
			return nil, e
		}
		if !absent {
			return nil, ErrConflict
		}
		out = append(out, r)
	}
	return out, nil
}

func appliedInventoryReceipt(request SubjectRequest, r Record, j JournalEntry) bool {
	return request.Approved && request.Kind == Delete && request.Scope == r.Scope && request.SubjectID == r.SubjectID && j.Scope == r.Scope && j.RequestID == request.ID && j.RecordID == r.ID && j.Generation == r.Generation && j.State == RecordedApplied && j.ConfirmationReference != ""
}
