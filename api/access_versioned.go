package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var ErrAccessConflict = errors.New("access version conflict")
var ErrAccessCommitUnknown = errors.New("access commit outcome unknown; reconcile before retry")

// AccessReplacement comes from trusted administration, never a public request
// actor. HostCheck must recheck the named operator, target and membership inside
// this transaction, and may update host authorization only when apply is true.
type AccessReplacement struct {
	OrganizationID, UserID, OperatorID, Action, Reason string
	ExpectedVersion                                    int64
	Grants                                             []Grant
	Metadata                                           json.RawMessage
}

type AccessSnapshot struct {
	Version int64   `json:"version"`
	Grants  []Grant `json:"grants"`
}

type AccessHostCheck func(context.Context, *sql.Tx, bool) error

// InspectVersioned is read-only and requires the same trusted host membership
// validation as a replacement. It never creates a version or audit row.
func (s AccessStore) InspectVersioned(ctx context.Context, org, user string, hostCheck AccessHostCheck) (AccessSnapshot, error) {
	if s.DB == nil || hostCheck == nil || strings.TrimSpace(org) == "" || strings.TrimSpace(user) == "" {
		return AccessSnapshot{}, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return AccessSnapshot{}, err
	}
	defer tx.Rollback()
	if err := hostCheck(ctx, tx, false); err != nil {
		return AccessSnapshot{}, err
	}
	return accessSnapshotTx(ctx, tx, org, user)
}

func (s AccessStore) migrateVersioned(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_api_access_versions (
 organization_id text NOT NULL,user_id text NOT NULL,version bigint NOT NULL CHECK(version>=0),
 PRIMARY KEY(organization_id,user_id));
 CREATE TABLE IF NOT EXISTS addon_api_access_audit (
 id bigserial PRIMARY KEY,organization_id text NOT NULL,user_id text NOT NULL,operator_id text NOT NULL,
 action text NOT NULL,reason text NOT NULL,previous_version bigint NOT NULL,new_version bigint NOT NULL,
 before_grants jsonb NOT NULL,after_grants jsonb NOT NULL,metadata jsonb NOT NULL,
 changed_at timestamptz NOT NULL DEFAULT clock_timestamp());
 CREATE OR REPLACE FUNCTION addon_api_access_audit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'access audit is append only'; END $$;
 DROP TRIGGER IF EXISTS addon_api_access_audit_row ON addon_api_access_audit;
 CREATE TRIGGER addon_api_access_audit_row BEFORE UPDATE OR DELETE ON addon_api_access_audit
 FOR EACH ROW EXECUTE FUNCTION addon_api_access_audit_immutable();
 DROP TRIGGER IF EXISTS addon_api_access_audit_truncate ON addon_api_access_audit;
 CREATE TRIGGER addon_api_access_audit_truncate BEFORE TRUNCATE ON addon_api_access_audit
 FOR EACH STATEMENT EXECUTE FUNCTION addon_api_access_audit_immutable()`)
	return err
}

func validateReplacement(c AccessReplacement) error {
	for _, v := range []string{c.OrganizationID, c.UserID, c.OperatorID, c.Reason} {
		if strings.TrimSpace(v) == "" || len(v) > 1024 || strings.Contains(v, "*") {
			return ErrDenied
		}
	}
	if c.Action != "replace" && c.Action != "revoke" || c.ExpectedVersion < 0 || c.ExpectedVersion == math.MaxInt64 || len(c.Grants) > 512 {
		return ErrDenied
	}
	if c.Action == "revoke" && len(c.Grants) != 0 || c.Action == "replace" && len(c.Grants) == 0 {
		return ErrDenied
	}
	if len(c.Metadata) > 65536 || len(c.Metadata) != 0 && !json.Valid(c.Metadata) {
		return ErrDenied
	}
	seen := map[Grant]bool{}
	for _, g := range c.Grants {
		if strings.TrimSpace(g.Permission) != g.Permission || strings.Count(g.Permission, ":") != 1 || strings.HasPrefix(g.Permission, ":") || strings.HasSuffix(g.Permission, ":") || strings.Contains(g.Permission+g.UnitID+g.Audience, "*") || g.UnitID == "" && g.Audience == "" || len(g.Permission) > 128 || len(g.UnitID) > 256 || len(g.Audience) > 256 || seen[g] {
			return ErrDenied
		}
		seen[g] = true
	}
	return nil
}

// ReplaceVersioned previews by default when apply is false. Even a preview
// rechecks the expected version and host policy. HostCheck is mandatory: this
// storage package cannot authenticate a host operator or infer membership.
// Apply replaces all scopes; it never merges grants. Host writes, grant changes,
// version advancement and the append-only audit commit or roll back together.
func (s AccessStore) ReplaceVersioned(ctx context.Context, change AccessReplacement, apply bool, hostCheck AccessHostCheck) (AccessSnapshot, error) {
	if s.DB == nil || hostCheck == nil {
		return AccessSnapshot{}, ErrDenied
	}
	if err := validateReplacement(change); err != nil {
		return AccessSnapshot{}, err
	}
	// Preview is rollback-only rather than PostgreSQL READ ONLY: owner policy
	// needs SELECT FOR SHARE to exercise the same current-roster fence. No write
	// branch executes for apply=false, and the transaction never commits.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return AccessSnapshot{}, err
	}
	defer tx.Rollback()
	if err = s.LockVersionedTx(ctx, tx, change.OrganizationID, change.UserID); err != nil {
		return AccessSnapshot{}, err
	}
	before, err := accessSnapshotTx(ctx, tx, change.OrganizationID, change.UserID)
	if err != nil {
		return AccessSnapshot{}, err
	}
	if before.Version != change.ExpectedVersion {
		return AccessSnapshot{}, ErrAccessConflict
	}
	if err = hostCheck(ctx, tx, apply); err != nil {
		return AccessSnapshot{}, err
	}
	if !apply {
		return before, nil
	}
	if err = writeAccessReplacement(ctx, tx, change, before); err != nil {
		return AccessSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return AccessSnapshot{}, ErrAccessCommitUnknown
	}
	return AccessSnapshot{Version: before.Version + 1, Grants: append([]Grant{}, change.Grants...)}, nil
}

func accessSnapshotTx(ctx context.Context, tx *sql.Tx, org, user string) (AccessSnapshot, error) {
	var out AccessSnapshot
	err := tx.QueryRowContext(ctx, `SELECT version FROM addon_api_access_versions WHERE organization_id=$1 AND user_id=$2`, org, user).Scan(&out.Version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Grants = []Grant{}
	rows, err := tx.QueryContext(ctx, `SELECT permission,unit_id,audience FROM addon_api_grants WHERE organization_id=$1 AND user_id=$2 ORDER BY permission,unit_id,audience`, org, user)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Permission, &g.UnitID, &g.Audience); err != nil {
			return out, err
		}
		out.Grants = append(out.Grants, g)
	}
	return out, rows.Err()
}

func writeAccessReplacement(ctx context.Context, tx *sql.Tx, c AccessReplacement, before AccessSnapshot) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM addon_api_grants WHERE organization_id=$1 AND user_id=$2`, c.OrganizationID, c.UserID); err != nil {
		return err
	}
	for _, g := range c.Grants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO addon_api_grants(organization_id,user_id,permission,unit_id,audience) VALUES($1,$2,$3,$4,$5)`, c.OrganizationID, c.UserID, g.Permission, g.UnitID, g.Audience); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO addon_api_access_versions(organization_id,user_id,version) VALUES($1,$2,$3) ON CONFLICT(organization_id,user_id) DO UPDATE SET version=EXCLUDED.version`, c.OrganizationID, c.UserID, before.Version+1); err != nil {
		return err
	}
	oldJSON, _ := json.Marshal(before.Grants)
	newJSON, _ := json.Marshal(append([]Grant{}, c.Grants...))
	metadata := c.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO addon_api_access_audit(organization_id,user_id,operator_id,action,reason,previous_version,new_version,before_grants,after_grants,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, c.OrganizationID, c.UserID, c.OperatorID, c.Action, c.Reason, before.Version, before.Version+1, oldJSON, newJSON, []byte(metadata))
	return err
}
