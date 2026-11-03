package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// CorrectionRecoveryAdapter is optional for generic consumers. Hosts promising
// recovery MUST require this port. Payload is sensitive and must be sealed in
// independent custody in the SAME transaction, never returned by HTTP inventory.
// Implementations retain no before-image prose. Restore must authorize the
// supplied actor against the supplied transaction's actual source, and must
// verify a real postcondition even when a recovery receipt already exists.
type CorrectionRecoveryAdapter interface {
	CorrectWithRecoveryTx(context.Context, *sql.Tx, api.Actor, Record, json.RawMessage) (json.RawMessage, error)
	RestoreCorrectionTx(context.Context, *sql.Tx, api.Actor, Record, json.RawMessage) (CorrectionRestoreResult, error)
	VerifyCorrectionTx(context.Context, *sql.Tx, api.Actor, Record, json.RawMessage) (CorrectionRestoreResult, error)
	VerifyCorrectionHistoryTx(context.Context, *sql.Tx, api.Actor, Record, []json.RawMessage) (CorrectionRestoreResult, error)
}

type CorrectionRestoreResult struct {
	Fingerprint    string `json:"fingerprint"`
	Version        uint64 `json:"version"`
	AlreadyCurrent bool   `json:"already_current"`
}

// RestoreCorrectionTx uses caller-owned offline transaction. It never creates a
// transaction, commits, changes serving state or trusts a prior restore receipt.
// Host must retain independent live authority and compare custody before calling.
func (s PhysicalService) RestoreCorrectionTx(ctx context.Context, tx *sql.Tx, a api.Actor, scope Scope, request, record string, payload json.RawMessage) (CorrectionRestoreResult, error) {
	r, e := s.ValidateCorrectionRecoveryTx(ctx, tx, a, scope, request, record)
	if e != nil {
		return CorrectionRestoreResult{}, e
	}
	adapter, ok := s.adapter(r).(CorrectionRecoveryAdapter)
	if !ok {
		return CorrectionRestoreResult{}, ErrInvalid
	}
	return adapter.RestoreCorrectionTx(ctx, tx, a, r, payload)
}

// Validation also applies to verified no-op recovery: a hold or withdrawn
// authority cannot be bypassed because target bytes happen to be current.
func (s PhysicalService) ValidateCorrectionRecoveryTx(ctx context.Context, tx *sql.Tx, a api.Actor, scope Scope, request, record string) (Record, error) {
	var out Record
	if tx == nil || !scope.valid() || a.OrganizationID != scope.OrganizationID || !a.MFA || !a.Allows(string(RestoreReconcile), scope.UnitID) || !a.Allows(string(CorrectionRequest), scope.UnitID) || !a.Allows(string(JournalRead), scope.UnitID) {
		return out, ErrDenied
	}
	if e := LockScopesTx(ctx, tx, []Scope{scope}); e != nil {
		return out, e
	}
	t := &sqlTransaction{tx: tx, scope: scope}
	req, e := get[SubjectRequest](ctx, t, requestKind, request)
	if e != nil {
		return out, e
	}
	if req.Kind != Correct || !req.Approved || req.ApprovedBy == "" || req.Scope != scope {
		return out, ErrApproval
	}
	r, e := get[Record](ctx, t, recordKind, record)
	if e != nil {
		return out, e
	}
	if r.Scope != scope || r.SubjectID != req.SubjectID {
		return out, ErrDenied
	}
	holds, e := list[Hold](ctx, t, holdKind)
	if e != nil {
		return out, e
	}
	for _, h := range holds {
		if heldBy(r, h) {
			return out, ErrHold
		}
	}
	if _, ok := s.adapter(r).(CorrectionRecoveryAdapter); !ok {
		return out, ErrInvalid
	}
	return r, nil
}
