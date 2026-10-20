package delivery

import (
	"context"
	"database/sql"
)

// EraseEmployee is a trusted owner port, called only inside a governed SQL
// transaction after current identity, exact scope, retention and holds pass.
// It includes ordinary and fallback rows; pending external carrier outcomes
// cannot be undone and must remain independently disclosed.
func EraseEmployee(ctx context.Context, tx *sql.Tx, tenant, employeeID string) error {
	if tx == nil || tenant == "" || employeeID == "" {
		return ErrInvalid
	}

	rows, e := tx.QueryContext(ctx, `SELECT id FROM addon_conversation_delivery WHERE tenant=$1 AND payload->>'employee_id'=$2 ORDER BY id`, tenant, employeeID)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = EraseIntent(ctx, tx, tenant, id); e != nil {
			return e
		}
	}
	return nil
}
