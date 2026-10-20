// Package delivery owns durable conversation reply dispatch, never case-owned outbox tables.
package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Message struct {
	Channel    string `json:"channel,omitempty"`
	ID         string `json:"id"`
	Tenant     string `json:"tenant"`
	Account    string `json:"account"`
	Sender     string `json:"sender"`
	EmployeeID string `json:"employee_id"`
	CaseID     string `json:"case_id,omitempty"`
	Text       string `json:"text"`
	State      string `json:"state"`
	Lease      string `json:"-"`
}
type Result string

const (
	// VerifiedSent is the legacy synchronous transport acceptance label. It is
	// not carrier delivery evidence; reconciliation may still settle it as failed.
	VerifiedSent   Result = "sent"
	Accepted       Result = "accepted"
	VerifiedFailed Result = "failed"
	Unknown        Result = "held_unknown"
)

var ErrInvalid = errors.New("invalid delivery")

func Migrate(ctx context.Context, db *sql.DB) error {
	_, e := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_conversation_delivery(id text PRIMARY KEY,tenant text NOT NULL,sender text NOT NULL,payload jsonb NOT NULL,state text NOT NULL DEFAULT 'queued',lease text NOT NULL DEFAULT '',lease_until timestamptz,created_at timestamptz NOT NULL DEFAULT now());CREATE INDEX IF NOT EXISTS addon_conversation_delivery_pending ON addon_conversation_delivery(state,created_at)`)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `ALTER TABLE addon_conversation_delivery ADD COLUMN IF NOT EXISTS provider_id text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS addon_conversation_delivery_receipt(tenant text NOT NULL,account text NOT NULL,receipt_id text NOT NULL,intent_id text NOT NULL,provider_id text NOT NULL,fingerprint text NOT NULL,outcome text NOT NULL,observed_at timestamptz NOT NULL,recorded_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(tenant,account,receipt_id))`)
	return e
}

// Enqueue participates in the inbound/turn transaction. Stable IDs reject duplicate sends.
func Enqueue(ctx context.Context, tx *sql.Tx, m Message) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_conversation_delivery(id,tenant,sender,payload) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, m.ID, m.Tenant, m.Sender, b)
	return e
}

type Worker struct {
	SMSFallback *SMSFallbackConfig
	Tenant      string
	DB          *sql.DB
	Authorize   func(context.Context, Message) error
	Send        func(context.Context, Message) (Result, error)
	// SendObserved is the approved transport adapter. Acceptance is not delivery.
	SendObserved  func(context.Context, Message) (Result, string, error)
	Clock         func() time.Time
	LeaseDuration time.Duration
}

// Dispatch holds expired in-flight attempts: carrier success cannot be inferred after a crash.
// Provider calls must honor Message.ID as their idempotency key. No SMS fallback is implicit.
func (w Worker) Dispatch(ctx context.Context) (bool, error) {
	if w.DB == nil || w.Tenant == "" || w.Clock == nil || w.LeaseDuration <= 0 {
		return false, ErrInvalid
	}
	now := w.Clock()
	tx, e := w.DB.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE addon_conversation_delivery SET state='held_unknown' WHERE state='sending' AND lease_until<=$1 AND tenant=$2`, now, w.Tenant); e != nil {
		return false, e
	}
	var m Message
	var b []byte
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id,payload FROM addon_conversation_delivery WHERE state='queued' AND tenant=$1 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, w.Tenant).Scan(&id, &b)
	if errors.Is(e, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if e != nil {
		return false, e
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return false, e
	}
	lease := id + now.UTC().Format(time.RFC3339Nano)
	if _, e = tx.ExecContext(ctx, `UPDATE addon_conversation_delivery SET state='sending',lease=$2,lease_until=$3 WHERE id=$1`, id, lease, now.Add(w.LeaseDuration)); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	state := "held"
	providerID := ""
	if w.Authorize != nil && w.Authorize(ctx, m) == nil && (w.Send != nil || w.SendObserved != nil) {
		var result Result
		var sendErr error
		if w.SendObserved != nil {
			if m.Account == "" {
				result = Unknown
			} else {
				result, providerID, sendErr = w.SendObserved(ctx, m)
			}
		} else {
			result, sendErr = w.Send(ctx, m)
		}
		state = string(result)
		if sendErr != nil || (result != VerifiedSent && result != VerifiedFailed && result != Accepted) || (result == Accepted && (m.Account == "" || providerID == "")) {
			state = string(Unknown)
		}
	}
	finish, e := w.DB.BeginTx(ctx, nil)
	if e != nil {
		return true, e
	}
	defer finish.Rollback()
	updated, e := finish.ExecContext(ctx, `UPDATE addon_conversation_delivery SET state=$3,provider_id=$4 WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>clock_timestamp()`, id, lease, state, providerID)
	if e != nil {
		return true, e
	}
	n, e := updated.RowsAffected()
	if e != nil {
		return true, e
	}
	if n != 1 {
		return true, ErrInvalid
	}
	if state == string(VerifiedFailed) && m.Channel != "sms" && w.SMSFallback != nil && w.SMSFallback.Account != "" {
		fallback := m
		fallback.ID = m.ID + ":sms-fallback"
		fallback.Channel = "sms"
		fallback.Account = w.SMSFallback.Account
		fallback.Text = MinimalSMSNotice
		fallback.State = ""
		fallback.Lease = ""
		if e = Enqueue(ctx, finish, fallback); e != nil {
			return true, e
		}
	}
	return true, finish.Commit()
}
func List(ctx context.Context, db *sql.DB, tenant, sender string) ([]Message, error) {
	rows, e := db.QueryContext(ctx, `SELECT payload,state FROM addon_conversation_delivery WHERE tenant=$1 AND sender=$2 ORDER BY created_at,id`, tenant, sender)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var b []byte
		var state string
		if e = rows.Scan(&b, &state); e != nil {
			return nil, e
		}
		var m Message
		if e = json.Unmarshal(b, &m); e != nil {
			return nil, e
		}
		m.State = state
		out = append(out, m)
	}
	return out, rows.Err()
}
