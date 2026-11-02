package governance

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"strings"
)

// InventoryReview is restrictive source-discovery mode, never an authority grant.
// Only the exact authenticated existing-request review path installs it.
type inventoryReviewKey struct{}

func InventoryReview(ctx context.Context) bool {
	v, _ := ctx.Value(inventoryReviewKey{}).(bool)
	return v
}

type InventoryReviewAdapter interface {
	ReviewDiscoverTx(context.Context, *sql.Tx, api.Actor, Scope, string) ([]Record, error)
}

// ReviewInventoryTx validates existing source inventory for an authenticated
// independent reviewer. Source adapters keep their own visibility checks. It
// never grants inventory.write, creates a missing inventory record, or erases.
// Caller owns transaction/rollback and must commit only after its complete review.
func (s PhysicalService) ReviewInventoryTx(ctx context.Context, tx *sql.Tx, a api.Actor, scope Scope, subject, requestID string) ([]Record, error) {
	if tx == nil || !a.MFA || a.SessionID == "" || a.OrganizationID != scope.OrganizationID || !a.Allows(string(DeletionApprove), scope.UnitID) || !a.Allows(string(JournalRead), scope.UnitID) || !validReference(subject) {
		return nil, ErrDenied
	}
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil {
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
	req, e := get[SubjectRequest](ctx, t, requestKind, requestID)
	if e != nil || req.ID != requestID || req.Scope != scope || req.SubjectID != subject || req.Kind != Delete || !req.Approved || req.ApprovedBy == "" {
		return nil, ErrApproval
	}
	ctx = context.WithValue(ctx, inventoryReviewKey{}, true)
	current := []Record{}
	for _, adapter := range s.Adapters {
		reviewer, ok := adapter.(InventoryReviewAdapter)
		if !ok {
			return nil, ErrDenied
		}
		rows, e := reviewer.ReviewDiscoverTx(ctx, tx, a, scope, subject)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			if ValidateRecord(r) != nil || r.Scope != scope || r.SubjectID != subject || !strings.HasPrefix(r.ID, adapter.Name()+":") {
				return nil, ErrInvalid
			}
			old, e := get[Record](ctx, t, recordKind, r.ID)
			if e != nil {
				return nil, ErrConflict
			}
			if old.Scope != r.Scope || old.SubjectID != r.SubjectID || old.Reference != r.Reference || !sameHoldLineages(old, r) || RecordFingerprint(old) != RecordFingerprint(r) {
				return nil, ErrConflict
			}
			current = append(current, old)
		}
	}
	saved, e := list[Record](ctx, t, recordKind)
	if e != nil {
		return nil, e
	}
	selected := []Record{}
	for _, r := range saved {
		if r.SubjectID == subject {
			selected = append(selected, r)
		}
	}
	if !SameDelegatedInventory(current, selected) {
		return nil, ErrConflict
	}
	return current, nil
}
