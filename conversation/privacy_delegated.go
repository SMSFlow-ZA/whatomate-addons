package conversation

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

// DiscoverDelegatedTx validates the exact permit and preserves each source's
// current case/conflict/audience/unit checks for both independent signatories.
func (p PrivacyAdapter) DiscoverDelegatedTx(ctx context.Context, tx *sql.Tx, a api.Actor, scope governance.Scope, subject string, permit *governance.DelegatedAuthority) ([]governance.Record, error) {
	if e := permit.CheckDiscover(ctx, tx, a, scope, subject); e != nil {
		return nil, e
	}

	var out []governance.Record
	for i, actor := range permit.Actors() {
		if e := permit.CheckDiscover(ctx, tx, actor, scope, subject); e != nil {
			return nil, e
		}
		records, e := p.Discover(ctx, tx, actor, scope, subject)
		if e != nil {
			return nil, e
		}
		if i == 0 {
			out = records
		} else if !governance.SameDelegatedInventory(out, records) {
			return nil, governance.ErrDenied
		}
	}
	return out, nil
}
func (p PrivacyAdapter) EraseDelegatedTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record, permit *governance.DelegatedAuthority) error {
	if e := permit.CheckErase(ctx, tx, a, r); e != nil {
		return e
	}

	for _, actor := range permit.Actors() {
		if e := permit.CheckErase(ctx, tx, actor, r); e != nil {
			return e
		}
		if _, e := p.Export(ctx, tx, actor, r); e != nil {
			return e
		}
	}
	return p.Erase(ctx, tx, a, r)
}
