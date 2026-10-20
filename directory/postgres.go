package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// PostgresStore persists one tenant's directory with transactional CAS and audit.
// Construct it with an actor authenticated by the host, never a request body actor.
// The caller owns the DB pool and PostgreSQL driver registration.
type PostgresStore struct {
	db            *sql.DB
	tenant, actor string
}

func NewPostgresStore(db *sql.DB, tenant, actor string) (*PostgresStore, error) {
	if db == nil || strings.TrimSpace(tenant) == "" || strings.TrimSpace(actor) == "" || len(tenant) > 256 || len(actor) > 256 {
		return nil, errors.New("database, tenant and authenticated actor required")
	}
	return &PostgresStore{db, tenant, actor}, nil
}

// Init is an explicit migration hook for an appropriately privileged DB role.
// Runtime roles should have only the required table privileges, not schema DDL.
func (s *PostgresStore) Init(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(19631021)"); err != nil {
		return err
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS directory_snapshots (tenant text PRIMARY KEY, version bigint NOT NULL CHECK(version>0), payload jsonb NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS directory_events (tenant text NOT NULL, version bigint NOT NULL, actor text NOT NULL, occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(tenant,version))`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *PostgresStore) Load(ctx context.Context) (Snapshot, error) {
	var payload []byte
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT version,payload FROM directory_snapshots WHERE tenant=$1`, s.tenant).Scan(&version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err = json.Unmarshal(payload, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if version <= 0 || snapshot.Version != uint64(version) {
		return Snapshot{}, errors.New("corrupt directory version")
	}
	return snapshot, nil
}
func (s *PostgresStore) CompareAndSwap(ctx context.Context, expected uint64, next Snapshot) error {
	if expected >= math.MaxInt64 || next.Version != expected+1 {
		return ErrConflict
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO directory_snapshots(tenant,version,payload) VALUES($1,$2,$3::jsonb) ON CONFLICT(tenant) DO NOTHING`, s.tenant, int64(next.Version), string(payload))
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE directory_snapshots SET version=$2,payload=$3::jsonb WHERE tenant=$1 AND version=$4`, s.tenant, int64(next.Version), string(payload), int64(expected))
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO directory_events(tenant,version,actor) VALUES($1,$2,$3)`, s.tenant, int64(next.Version), s.actor); err != nil {
		return err
	}
	return tx.Commit()
}
