package carrier

import (
	"context"
	"database/sql"
)

// EraseProtectedIntent is an owner port for governed derivative erasure. The
// caller must first verify current case scope, retention and lineage holds in
// this same transaction. Authenticated receipt fingerprints remain tombstones.
func EraseProtectedIntent(ctx context.Context, tx *sql.Tx, tenant, id string) error {
	if tx == nil || tenant == "" || id == "" {
		return ErrDenied
	}
	var exists bool
	if e := tx.QueryRowContext(ctx, `SELECT to_regclass('addon_carrier_route') IS NOT NULL`).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return nil
	}
	if _, e := tx.ExecContext(ctx, `UPDATE addon_carrier_receipt SET payload='{}',state='erased' WHERE tenant=$1 AND (route_id=$2 OR payload->>'IntentID'=$2)`, tenant, id); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM addon_carrier_route WHERE tenant=$1 AND id=$2`, tenant, id)
	return e
}
