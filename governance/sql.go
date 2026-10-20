package governance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
)

// SQLRepository stores reference-only governance documents and append-only audit
// events. Scope advisory locks serialize policy/hold/receipt/restore decisions.
type SQLRepository struct{ DB *sql.DB }

func (r *SQLRepository) Migrate(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(19631023)"); e != nil {
		return e
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS addon_governance_document (organization_id text NOT NULL,unit_id text NOT NULL,kind text NOT NULL,document_key text NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(organization_id,unit_id,kind,document_key))`,
		`CREATE TABLE IF NOT EXISTS addon_governance_event (id text PRIMARY KEY,organization_id text NOT NULL,unit_id text NOT NULL,actor_id text NOT NULL,action text NOT NULL,reference_id text NOT NULL,recorded_at timestamptz NOT NULL,payload jsonb NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS addon_governance_event_scope_idx ON addon_governance_event(organization_id,unit_id,recorded_at)`,
	} {
		if _, e = tx.ExecContext(ctx, statement); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *SQLRepository) Transact(ctx context.Context, scope Scope, fn func(Transaction) error) (CommitOutcome, error) {
	if r == nil || r.DB == nil || !scope.valid() || fn == nil {
		return RolledBack, ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return RolledBack, e
	}
	defer tx.Rollback()
	payload, _ := json.Marshal([3]string{"governance", scope.OrganizationID, scope.UnitID})
	hash := sha256.Sum256(payload)
	lock := int64(binary.BigEndian.Uint64(hash[:8]))
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); e != nil {
		return RolledBack, e
	}
	if e = fn(&sqlTransaction{tx: tx, scope: scope}); e != nil {
		return RolledBack, e
	}
	if e = tx.Commit(); e != nil {
		return Unknown, errors.Join(ErrCommitUnknown, e)
	}
	return Committed, nil
}

type sqlTransaction struct {
	tx    *sql.Tx
	scope Scope
}

func (t *sqlTransaction) Get(ctx context.Context, kind, key string) ([]byte, error) {
	var raw []byte
	e := t.tx.QueryRowContext(ctx, `SELECT payload FROM addon_governance_document WHERE organization_id=$1 AND unit_id=$2 AND kind=$3 AND document_key=$4`, t.scope.OrganizationID, t.scope.UnitID, kind, key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, e
}
func (t *sqlTransaction) List(ctx context.Context, kind string) ([][]byte, error) {
	rows, e := t.tx.QueryContext(ctx, `SELECT payload FROM addon_governance_document WHERE organization_id=$1 AND unit_id=$2 AND kind=$3 ORDER BY document_key`, t.scope.OrganizationID, t.scope.UnitID, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var result [][]byte
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		result = append(result, raw)
	}
	return result, rows.Err()
}
func (t *sqlTransaction) Put(ctx context.Context, kind, key string, raw []byte) error {
	if !json.Valid(raw) {
		return ErrInvalid
	}
	_, e := t.tx.ExecContext(ctx, `INSERT INTO addon_governance_document(organization_id,unit_id,kind,document_key,payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,unit_id,kind,document_key) DO UPDATE SET payload=EXCLUDED.payload`, t.scope.OrganizationID, t.scope.UnitID, kind, key, string(raw))
	return e
}
func (t *sqlTransaction) AppendEvent(ctx context.Context, event Event) error {
	if event.Scope != t.scope || event.ID == "" || event.ActorID == "" || event.RecordedAt.IsZero() {
		return ErrInvalid
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return e
	}
	_, e = t.tx.ExecContext(ctx, `INSERT INTO addon_governance_event(id,organization_id,unit_id,actor_id,action,reference_id,recorded_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, event.ID, t.scope.OrganizationID, t.scope.UnitID, event.ActorID, event.Action, event.ReferenceID, event.RecordedAt, string(raw))
	return e
}
