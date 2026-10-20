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
	_, e := tx.ExecContext(ctx, `DELETE FROM addon_conversation_delivery WHERE tenant=$1 AND payload->>'employee_id'=$2`, tenant, employeeID)
	return e
}
