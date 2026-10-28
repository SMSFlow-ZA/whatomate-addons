package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"time"
)

type CorrectionAdapter interface {
	Correct(context.Context, *sql.Tx, api.Actor, Record, json.RawMessage) error
}

// Correct changes an actual owned object, bound to a recorded correction
// request, current domain scope and the owner's expected-version check. An
// active lineage hold blocks destructive overwrites; release/review is explicit.
func (s PhysicalService) Correct(ctx context.Context, a api.Actor, scope Scope, requestID, recordID string, patch json.RawMessage) error {
	if len(patch) == 0 || len(patch) > 65536 {
		return ErrInvalid
	}
	return s.within(ctx, a, scope, CorrectionRequest, func(t *sqlTransaction) error {
		req, e := get[SubjectRequest](ctx, t, requestKind, requestID)
		if e != nil {
			return e
		}
		if req.Kind != Correct {
			return ErrInvalid
		}
		record, e := get[Record](ctx, t, recordKind, recordID)
		if e != nil {
			return e
		}
		if record.SubjectID != req.SubjectID {
			return ErrDenied
		}
		holds, e := list[Hold](ctx, t, holdKind)
		if e != nil {
			return e
		}
		for _, h := range holds {
			if heldBy(record, h) {
				return ErrHold
			}
		}
		adapter, ok := s.adapter(record).(CorrectionAdapter)
		if !ok {
			return ErrInvalid
		}
		if e = adapter.Correct(ctx, t.tx, a, record, patch); e != nil {
			return e
		}
		if e = put(ctx, t, "correction_receipt", requestID, map[string]any{"record_id": recordID, "corrected_at": time.Now().UTC(), "actor_id": a.UserID}); e != nil {
			return e
		}
		return audit(ctx, t, PrincipalForActor(a), scope, "correction.applied", recordID)
	})
}
