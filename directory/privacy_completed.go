package directory

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

func (p PrivacyAdapter) CompletedSourceAbsentTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (bool, error) {
	if a.OrganizationID != r.Scope.OrganizationID || r.ID != "directory:"+r.Reference || !a.Allows("directory:privacy", r.Scope.UnitID) || !a.Has("directory:read") {
		return false, governance.ErrDenied
	}
	snapshot, e := p.privacySnapshot(ctx, tx, a, r.Scope.UnitID, r.Reference)
	if e != nil {
		return false, e
	}
	_, exists := snapshot.Employees[r.Reference]
	return !exists && snapshot.Revoked[r.Reference], nil
}
