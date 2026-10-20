package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// AccessStore holds exact additional unit/audience scope. It never grants a
// permission absent from the authenticated host role. Provisioning is a trusted
// deployment/administration operation, not a public HTTP request.
type AccessStore struct{ DB *sql.DB }

func (s AccessStore) Migrate(ctx context.Context) error {
	if s.DB == nil {
		return ErrDenied
	}
	_, e := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_api_grants (
 organization_id text NOT NULL,user_id text NOT NULL,permission text NOT NULL,
 unit_id text NOT NULL DEFAULT '',audience text NOT NULL DEFAULT '',
 PRIMARY KEY(organization_id,user_id,permission,unit_id,audience),
 CHECK(unit_id<>'' OR audience<>''),CHECK(unit_id<>'*' AND audience<>'*'))`)
	return e
}
func (s AccessStore) Load(ctx context.Context, a Actor) (Actor, error) {
	if s.DB == nil || a.OrganizationID == "" || a.UserID == "" {
		return Actor{}, ErrDenied
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT permission,unit_id,audience FROM addon_api_grants WHERE organization_id=$1 AND user_id=$2 ORDER BY permission,unit_id,audience`, a.OrganizationID, a.UserID)
	if e != nil {
		return Actor{}, e
	}
	defer rows.Close()
	a = clone(a)
	a.Grants = nil
	for rows.Next() {
		var g Grant
		if e = rows.Scan(&g.Permission, &g.UnitID, &g.Audience); e != nil {
			return Actor{}, e
		}
		if a.Has(g.Permission) {
			a.Grants = append(a.Grants, g)
		}
	}
	if e = rows.Err(); e != nil {
		return Actor{}, e
	}
	return a, nil
}
func (s AccessStore) Replace(ctx context.Context, org, user string, grants []Grant) error {
	if s.DB == nil || strings.TrimSpace(org) == "" || strings.TrimSpace(user) == "" {
		return ErrDenied
	}
	seen := map[Grant]bool{}
	for _, g := range grants {
		if g.Permission == "" || !strings.Contains(g.Permission, ":") || (g.UnitID == "" && g.Audience == "") || g.UnitID == "*" || g.Audience == "*" || seen[g] {
			return errors.New("invalid or duplicate exact scope grant")
		}
		seen[g] = true
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Serialize replacements for one user even when there are no existing rows.
	lockKey, _ := json.Marshal([]string{org, user})
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lockKey)); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM addon_api_grants WHERE organization_id=$1 AND user_id=$2`, org, user); e != nil {
		return e
	}
	for _, g := range grants {
		if _, e = tx.ExecContext(ctx, `INSERT INTO addon_api_grants(organization_id,user_id,permission,unit_id,audience) VALUES($1,$2,$3,$4,$5)`, org, user, g.Permission, g.UnitID, g.Audience); e != nil {
			return e
		}
	}
	return tx.Commit()
}
