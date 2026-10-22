package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// IdentityLinksTx reads historical conflict aliases in the caller transaction.
// No caller may erase or replace these links as part of changing access grants.
func (s AccessStore) IdentityLinksTx(ctx context.Context, tx *sql.Tx, org, user string) ([]string, error) {
	if tx == nil || strings.TrimSpace(org) == "" || strings.TrimSpace(user) == "" {
		return nil, ErrDenied
	}
	var crossOrganization bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_api_identity_links WHERE user_id=$1 AND organization_id<>$2)`, user, org).Scan(&crossOrganization); err != nil {
		return nil, err
	}
	if crossOrganization {
		return nil, ErrDenied
	}
	rows, err := tx.QueryContext(ctx, `SELECT employee_id FROM addon_api_identity_links WHERE organization_id=$1 AND user_id=$2 ORDER BY employee_id`, org, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// LinkIdentityTx appends one owner-validated identity in the existing
// ReplaceVersioned transaction. The host must first obtain the directory
// owner's locked proof. The outer access audit records the proof reference and
// roster version. Preview validates ownership but does not create a link.
func (s AccessStore) LinkIdentityTx(ctx context.Context, tx *sql.Tx, org, user, employee string, apply bool) error {
	for _, v := range []string{org, user, employee} {
		if v == "" || strings.TrimSpace(v) != v || len(v) > 256 || strings.ContainsAny(v, "*\x00\r\n") {
			return ErrDenied
		}
	}
	if tx == nil {
		return ErrDenied
	}
	// Include organization deliberately in employee ownership. User UUIDs are
	// host-global: an existing link for this same account in another tenant
	// cannot be silently treated as a current membership in this tenant.
	lock, _ := json.Marshal([]string{"identity-user", user})
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lock)); err != nil {
		return err
	}
	var crossOrganization bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_api_identity_links WHERE user_id=$1 AND organization_id<>$2)`, user, org).Scan(&crossOrganization); err != nil {
		return err
	}
	if crossOrganization {
		return ErrDenied
	}
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM addon_api_identity_links WHERE organization_id=$1 AND employee_id=$2`, org, employee).Scan(&owner)
	if err == nil {
		if owner != user {
			return ErrDenied
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if !apply {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO addon_api_identity_links(organization_id,employee_id,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, org, employee, user); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM addon_api_identity_links WHERE organization_id=$1 AND employee_id=$2`, org, employee).Scan(&owner); err != nil {
		return err
	}
	if owner != user {
		return ErrDenied
	}
	return nil
}
