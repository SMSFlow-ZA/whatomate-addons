package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// LockVersionedTx serializes a current owner authorization decision with both
// legacy Replace and ReplaceVersioned. Call before locking host membership and
// role/grant rows, and retain the transaction until the protected write commits.
func (s AccessStore) LockVersionedTx(ctx context.Context, tx *sql.Tx, org, user string) error {
	if s.DB == nil || tx == nil || strings.TrimSpace(org) == "" || strings.TrimSpace(user) == "" {
		return ErrDenied
	}
	key, _ := json.Marshal([]string{org, user})
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(key))
	return err
}
