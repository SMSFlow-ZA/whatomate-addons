package cases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type OutboxClaim struct {
	Message         OutboxMessage
	WorkerID, Token string
	LeaseUntil      time.Time
}

// Dispatch state is separate from the immutable intake message. Neither an
// expired attempt nor provider acceptance implies delivery or permits replay.
func (r *SQLRepository) MigrateOutboxDelivery(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return ErrInvalid
	}
	_, e := r.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_cases_delivery(
 id text PRIMARY KEY REFERENCES addon_cases_outbox(id),organization_id text NOT NULL,
 state text NOT NULL DEFAULT 'pending',worker text NOT NULL DEFAULT '',token text NOT NULL DEFAULT '',
 lease_until timestamptz,provider_id text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state IN ('pending','sending','accepted','delivered','failed','held','held_unknown')));
 CREATE INDEX IF NOT EXISTS addon_cases_delivery_pending ON addon_cases_delivery(organization_id,state);`)
	if e != nil {
		return e
	}
	_, e = r.DB.ExecContext(ctx, `ALTER TABLE addon_cases_delivery ADD COLUMN IF NOT EXISTS account_id text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS addon_cases_delivery_receipt(tenant text NOT NULL,account text NOT NULL,receipt_id text NOT NULL,intent_id text NOT NULL,provider_id text NOT NULL,fingerprint text NOT NULL,outcome text NOT NULL,observed_at timestamptz NOT NULL,recorded_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(tenant,account,receipt_id))`)
	return e
}

func (r *SQLRepository) ClaimOutbox(ctx context.Context, tenant, worker string, now, leaseUntil time.Time) (OutboxClaim, bool, error) {
	if r == nil || r.DB == nil || tenant == "" || worker == "" || now.IsZero() || !leaseUntil.After(now) || leaseUntil.Sub(now) > 15*time.Minute {
		return OutboxClaim{}, false, ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return OutboxClaim{}, false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO addon_cases_delivery(id,organization_id) SELECT id,organization_id FROM addon_cases_outbox WHERE organization_id=$1 ON CONFLICT DO NOTHING`, tenant); e != nil {
		return OutboxClaim{}, false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE addon_cases_delivery SET state='held_unknown',updated_at=$2 WHERE organization_id=$1 AND state='sending' AND lease_until<=$2`, tenant, now); e != nil {
		return OutboxClaim{}, false, e
	}
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT o.payload FROM addon_cases_delivery d JOIN addon_cases_outbox o ON o.id=d.id AND o.organization_id=d.organization_id WHERE d.organization_id=$1 AND d.state='pending' ORDER BY o.created_at,o.id FOR UPDATE OF d SKIP LOCKED LIMIT 1`, tenant).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return OutboxClaim{}, false, tx.Commit()
	}
	if e != nil {
		return OutboxClaim{}, false, e
	}
	claim := OutboxClaim{WorkerID: worker, LeaseUntil: leaseUntil}
	if e = json.Unmarshal(raw, &claim.Message); e != nil {
		return OutboxClaim{}, false, e
	}
	if claim.Message.OrganizationID != tenant {
		return OutboxClaim{}, false, ErrDenied
	}
	if claim.Token, e = randomID(); e != nil {
		return OutboxClaim{}, false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE addon_cases_delivery SET state='sending',worker=$3,token=$4,lease_until=$5,updated_at=$6 WHERE id=$1 AND organization_id=$2`, claim.Message.ID, tenant, worker, claim.Token, leaseUntil, now); e != nil {
		return OutboxClaim{}, false, e
	}
	if e = tx.Commit(); e != nil {
		return OutboxClaim{}, false, e
	}
	return claim, true, nil
}

// FinishOutbox records the observed outcome once. Provider acceptance must be
// reconciled with a verified receipt by the carrier adapter before calling it
// delivered. The owner port does not invent a carrier receipt or retry policy.
func (r *SQLRepository) FinishOutbox(ctx context.Context, c OutboxClaim, state, providerID string) error {
	switch state {
	case "accepted":
		if providerID == "" {
			return ErrInvalid
		}
	case "failed", "held", "held_unknown":
	default:
		return ErrInvalid
	}
	if r == nil || r.DB == nil || c.Message.OrganizationID == "" || c.Token == "" || c.WorkerID == "" {
		return ErrInvalid
	}
	result, e := r.DB.ExecContext(ctx, `UPDATE addon_cases_delivery SET state=$5,provider_id=$6,updated_at=now() WHERE id=$1 AND organization_id=$2 AND worker=$3 AND token=$4 AND state='sending' AND lease_until>clock_timestamp() AND ($5 NOT IN ('accepted','delivered') OR account_id<>'')`, c.Message.ID, c.Message.OrganizationID, c.WorkerID, c.Token, state, providerID)
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
	return nil
}
