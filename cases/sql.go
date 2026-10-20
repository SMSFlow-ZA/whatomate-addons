package cases

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
)

// SQLRepository persists only addon_cases_* tables in PostgreSQL. The host owns
// its connection/driver and must run Migrate explicitly before enabling cases.
// Repository transactions are internal trusted boundaries, not API handlers.
type SQLRepository struct{ DB *sql.DB }

// Migrate installs version 1 storage atomically. Deployments must restrict schema
// changes to a migration identity; normal application identities need only DML.
func (r *SQLRepository) Migrate(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(19631022)"); e != nil {
		return e
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS addon_cases_schema (version integer PRIMARY KEY, installed_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS addon_cases_case (id text PRIMARY KEY, organization_id text NOT NULL, unit_id text NOT NULL, contact_id text NOT NULL, version bigint NOT NULL CHECK(version>0), payload jsonb NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS addon_cases_contact_idx ON addon_cases_case(organization_id,contact_id)`,
		`CREATE TABLE IF NOT EXISTS addon_cases_intake (organization_id text NOT NULL, intake_key text NOT NULL, fingerprint text NOT NULL, case_id text NOT NULL REFERENCES addon_cases_case(id), PRIMARY KEY(organization_id,intake_key))`,
		`CREATE TABLE IF NOT EXISTS addon_cases_outbox (id text PRIMARY KEY, organization_id text NOT NULL, case_id text NOT NULL REFERENCES addon_cases_case(id), contact_id text NOT NULL, kind text NOT NULL, created_at timestamptz NOT NULL, payload jsonb NOT NULL, UNIQUE(case_id,kind))`,
		`CREATE TABLE IF NOT EXISTS addon_cases_event (case_id text NOT NULL REFERENCES addon_cases_case(id), version bigint NOT NULL CHECK(version>0), organization_id text NOT NULL, unit_id text NOT NULL, actor_id text NOT NULL, recorded_at timestamptz NOT NULL, occurred_at timestamptz NOT NULL, payload jsonb NOT NULL, PRIMARY KEY(case_id,version))`,
	} {
		if _, e = tx.ExecContext(ctx, statement); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO addon_cases_schema(version) VALUES(1),(2) ON CONFLICT DO NOTHING`); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *SQLRepository) Begin(ctx context.Context) (Transaction, error) {
	if r == nil || r.DB == nil {
		return nil, ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	return &sqlTransaction{tx: tx}, nil
}

type sqlTransaction struct {
	tx      *sql.Tx
	done    bool
	outcome CommitOutcome
}

func (t *sqlTransaction) Intake(ctx context.Context, org, key, fp string, c Case, m OutboxMessage) (string, error) {
	if t.done {
		return "", ErrInvalid
	}
	// Lock a hash of the structured key. Collisions only serialize independent
	// keys; the full database primary key remains the authoritative identity.
	encoded, _ := json.Marshal([2]string{org, key})
	hash := sha256.Sum256(encoded)
	lock := int64(binary.BigEndian.Uint64(hash[:8]))
	if _, e := t.tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); e != nil {
		return "", e
	}
	var oldFP, id string
	e := t.tx.QueryRowContext(ctx, `SELECT fingerprint,case_id FROM addon_cases_intake WHERE organization_id=$1 AND intake_key=$2`, org, key).Scan(&oldFP, &id)
	if e == nil {
		if oldFP != fp {
			return "", ErrIdempotencyConflict
		}
		return id, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	if c.ID == "" || c.OrganizationID != org || c.UnitID == "" || c.ContactID == "" || c.Version != 1 || len(c.Events) != 1 || m.OrganizationID != org || m.CaseID != c.ID || m.ContactID != c.ContactID || m.ID == "" || m.CreatedAt.IsZero() {
		return "", ErrInvalid
	}
	payload, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	if _, e = t.tx.ExecContext(ctx, `INSERT INTO addon_cases_case(id,organization_id,unit_id,contact_id,version,payload) VALUES($1,$2,$3,$4,$5,$6)`, c.ID, org, c.UnitID, c.ContactID, c.Version, string(payload)); e != nil {
		return "", e
	}
	if _, e = t.tx.ExecContext(ctx, `INSERT INTO addon_cases_intake(organization_id,intake_key,fingerprint,case_id) VALUES($1,$2,$3,$4)`, org, key, fp, c.ID); e != nil {
		return "", e
	}
	if e = t.appendEvent(ctx, c, c.Events[0]); e != nil {
		return "", e
	}
	payload, e = json.Marshal(m)
	if e != nil {
		return "", e
	}
	if _, e = t.tx.ExecContext(ctx, `INSERT INTO addon_cases_outbox(id,organization_id,case_id,contact_id,kind,created_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, m.ID, org, c.ID, m.ContactID, m.Kind, m.CreatedAt, string(payload)); e != nil {
		return "", e
	}
	return c.ID, nil
}
func (t *sqlTransaction) Load(ctx context.Context, id string) (Case, error) {
	if t.done {
		return Case{}, ErrInvalid
	}
	var payload []byte
	e := t.tx.QueryRowContext(ctx, `SELECT payload FROM addon_cases_case WHERE id=$1`, id).Scan(&payload)
	if errors.Is(e, sql.ErrNoRows) {
		return Case{}, ErrNotFound
	}
	if e != nil {
		return Case{}, e
	}
	var c Case
	if e = json.Unmarshal(payload, &c); e != nil {
		return Case{}, e
	}
	return c, nil
}
func (t *sqlTransaction) Save(ctx context.Context, c Case, expected uint64) error {
	if t.done {
		return ErrInvalid
	}
	if c.Version != expected+1 || len(c.Events) == 0 || c.Events[len(c.Events)-1].Version != c.Version {
		return ErrConflict
	}
	payload, e := json.Marshal(c)
	if e != nil {
		return e
	}
	result, e := t.tx.ExecContext(ctx, `UPDATE addon_cases_case SET version=$1,payload=$2 WHERE id=$3 AND version=$4 AND organization_id=$5 AND unit_id=$6 AND contact_id=$7 AND COALESCE(payload->'Snapshot','{"zone":"","department":"","case_type":""}'::jsonb)=COALESCE($2::jsonb->'Snapshot','null'::jsonb)`, c.Version, string(payload), c.ID, expected, c.OrganizationID, c.UnitID, c.ContactID)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return t.appendEvent(ctx, c, c.Events[len(c.Events)-1])
}
func (t *sqlTransaction) appendEvent(ctx context.Context, c Case, event Event) error {
	if event.Version != c.Version || event.OrganizationID != c.OrganizationID || event.UnitID != c.UnitID || event.ActorID == "" || event.RecordedAt.IsZero() || event.OccurredAt.IsZero() {
		return ErrInvalid
	}
	payload, e := json.Marshal(event)
	if e != nil {
		return e
	}
	_, e = t.tx.ExecContext(ctx, `INSERT INTO addon_cases_event(case_id,version,organization_id,unit_id,actor_id,recorded_at,occurred_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, c.ID, event.Version, c.OrganizationID, c.UnitID, event.ActorID, event.RecordedAt, event.OccurredAt, string(payload))
	return e
}
func (t *sqlTransaction) Commit(ctx context.Context) (CommitOutcome, error) {
	if t.done {
		return t.outcome, sql.ErrTxDone
	}
	if e := ctx.Err(); e != nil {
		rollback := t.tx.Rollback()
		t.done = true
		if rollback == nil {
			t.outcome = RolledBack
			return RolledBack, e
		}
		t.outcome = Unknown
		return Unknown, errors.Join(e, rollback)
	}
	e := t.tx.Commit()
	t.done = true
	if e != nil {
		t.outcome = Unknown
		return Unknown, e
	}
	t.outcome = Committed
	return Committed, nil
}
func (t *sqlTransaction) Rollback(context.Context) error {
	if t.done {
		return nil
	}
	e := t.tx.Rollback()
	t.done = true
	if e == nil {
		t.outcome = RolledBack
	} else {
		t.outcome = Unknown
	}
	return e
}
