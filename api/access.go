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
 CHECK(unit_id<>'' OR audience<>''),CHECK(unit_id<>'*' AND audience<>'*'));
 CREATE TABLE IF NOT EXISTS addon_api_identity_links (
 organization_id text NOT NULL,employee_id text NOT NULL,user_id text NOT NULL,
 linked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,employee_id))`)
	if e != nil {
		return e
	}
	return s.migrateVersioned(ctx)
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
	if e = rows.Close(); e != nil {
		return Actor{}, e
	}
	links, e := s.DB.QueryContext(ctx, `SELECT employee_id FROM addon_api_identity_links WHERE organization_id=$1 AND user_id=$2 ORDER BY employee_id`, a.OrganizationID, a.UserID)
	if e != nil {
		return Actor{}, e
	}
	defer links.Close()
	a.Aliases = nil
	for links.Next() {
		var id string
		if e = links.Scan(&id); e != nil {
			return Actor{}, e
		}
		a.Aliases = append(a.Aliases, id)
	}
	if e = links.Err(); e != nil {
		return Actor{}, e
	}
	return a, nil
}

// LinkIdentity is append-only. A roster ID cannot be silently reassigned or
// removed to evade a historical case conflict. Correcting a bad link requires a
// separately reviewed migration; no public endpoint can call this method.
func (s AccessStore) LinkIdentity(ctx context.Context, org, user, employee string) error {
	if s.DB == nil || org == "" || user == "" || employee == "" || len(employee) > 256 || employee == "*" {
		return ErrDenied
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO addon_api_identity_links(organization_id,employee_id,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, org, employee, user); e != nil {
		return e
	}
	var owner string
	if e = tx.QueryRowContext(ctx, `SELECT user_id FROM addon_api_identity_links WHERE organization_id=$1 AND employee_id=$2`, org, employee).Scan(&owner); e != nil {
		return e
	}
	if owner != user {
		return ErrDenied
	}
	return tx.Commit()
}

// EmployeeAliases resolves only identity equivalence after the channel has
// verified current roster eligibility. It confers no operator permission or MFA.
func (s AccessStore) EmployeeAliases(ctx context.Context, org, employee string) ([]string, error) {
	if s.DB == nil || org == "" || employee == "" {
		return nil, ErrDenied
	}
	var user string
	e := s.DB.QueryRowContext(ctx, `SELECT user_id FROM addon_api_identity_links WHERE organization_id=$1 AND employee_id=$2`, org, employee).Scan(&user)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT employee_id FROM addon_api_identity_links WHERE organization_id=$1 AND user_id=$2 ORDER BY employee_id`, org, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{user}
	for rows.Next() {
		var alias string
		if e = rows.Scan(&alias); e != nil {
			return nil, e
		}
		if alias != employee {
			out = append(out, alias)
		}
	}
	return out, rows.Err()
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
	before, e := accessSnapshotTx(ctx, tx, org, user)
	if e != nil {
		return e
	}
	change := AccessReplacement{OrganizationID: org, UserID: user, OperatorID: "legacy-provisioner", Action: "legacy-replace", Reason: "legacy trusted provisioning", ExpectedVersion: before.Version, Grants: grants}
	if e = writeAccessReplacement(ctx, tx, change, before); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return ErrAccessCommitUnknown
	}
	return nil
}
