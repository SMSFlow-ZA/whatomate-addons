package reports

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

// ReviewDiscoverTx is the explicit non-registering source review port. It
// preserves source access checks and refuses calls outside restricted review.
func (a PrivacyAdapter) ReviewDiscoverTx(ctx context.Context, tx *sql.Tx, actor api.Actor, scope governance.Scope, subject string) ([]governance.Record, error) {
	if !governance.InventoryReview(ctx) {
		return nil, governance.ErrDenied
	}
	return a.Discover(ctx, tx, actor, scope, subject)
}
