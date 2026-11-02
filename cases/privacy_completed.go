package cases

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

func (PrivacyAdapter) CompletedSourceAbsentTx(ctx context.Context, tx *sql.Tx, a api.Actor, r governance.Record) (bool, error) {
	if r.Scope.OrganizationID != a.OrganizationID || r.ID != "cases:"+r.Reference || (!a.Allows(string(ReadTriage), r.Scope.UnitID) && !a.Allows(string(WorkAssigned), r.Scope.UnitID)) {
		return false, governance.ErrDenied
	}
	var exists bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_cases_case WHERE organization_id=$1 AND id=$2)`, a.OrganizationID, r.Reference).Scan(&exists)
	return !exists, e
}
