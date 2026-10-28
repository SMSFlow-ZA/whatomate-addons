package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// Reconciler settles a trusted provider observation without resending the intent.
// A verified failure may enqueue exactly one configured minimal SMS fallback.
type Reconciler struct {
	DB          *sql.DB
	Verify      api.ReceiptVerifier
	SMSFallback *SMSFallbackConfig
	Authorize   func(context.Context, Message) error
}

// DeliveryStatus is a trusted internal composition port. Identity bindings are
// never employee-facing status DTO fields or evidence of consent/reading.
type DeliveryStatus struct {
	IntentID, Account, ProviderID, State string
	EmployeeID                           string `json:"-"`
	Sender                               string `json:"-"`
}

func ReadDeliveryStatus(ctx context.Context, db *sql.DB, tenant, id string) (DeliveryStatus, error) {
	if db == nil || tenant == "" || id == "" {
		return DeliveryStatus{}, ErrInvalid
	}
	var s DeliveryStatus
	e := db.QueryRowContext(ctx, `SELECT id,payload->>'account',provider_id,state,COALESCE(payload->>'employee_id',''),COALESCE(payload->>'sender','') FROM addon_conversation_delivery WHERE tenant=$1 AND id=$2`, tenant, id).Scan(&s.IntentID, &s.Account, &s.ProviderID, &s.State, &s.EmployeeID, &s.Sender)
	if errors.Is(e, sql.ErrNoRows) {
		return s, ErrInvalid
	}
	return s, e
}
func (r Reconciler) Reconcile(ctx context.Context, receipt api.Receipt) error {
	fingerprint, e := api.VerifyReceipt(ctx, receipt, r.Verify)
	if e != nil {
		return ErrInvalid
	}
	if r.DB == nil {
		return ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var raw []byte
	var provider, state string
	e = tx.QueryRowContext(ctx, `SELECT payload,provider_id,state FROM addon_conversation_delivery WHERE tenant=$1 AND id=$2 FOR UPDATE`, receipt.Tenant, receipt.IntentID).Scan(&raw, &provider, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrInvalid
	}
	if e != nil {
		return e
	}
	var m Message
	if e = json.Unmarshal(raw, &m); e != nil {
		return e
	}
	if m.Account == "" || m.Account != receipt.Account || (provider != "" && provider != receipt.ProviderID) {
		return ErrInvalid
	}
	if m.Purpose != "" && (m.Purpose != HandlerEscalation || r.Authorize == nil || r.Authorize(ctx, m) != nil) {
		return ErrInvalid
	}
	var prior string
	e = tx.QueryRowContext(ctx, `SELECT fingerprint FROM addon_conversation_delivery_receipt WHERE tenant=$1 AND account=$2 AND receipt_id=$3`, receipt.Tenant, receipt.Account, receipt.ReceiptID).Scan(&prior)
	if e == nil {
		if prior != fingerprint {
			return ErrInvalid
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if state == "delivered" || state == "failed" {
		terminal := state
		if terminal == "sent" {
			terminal = "delivered"
		}
		if terminal != receipt.Outcome {
			return ErrInvalid
		}
	} else if state != "sending" && state != "accepted" && state != "sent" && state != "held_unknown" {
		return ErrInvalid
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_conversation_delivery_receipt(tenant,account,receipt_id,intent_id,provider_id,fingerprint,outcome,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receipt.Tenant, receipt.Account, receipt.ReceiptID, receipt.IntentID, receipt.ProviderID, fingerprint, receipt.Outcome, receipt.ObservedAt)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE addon_conversation_delivery SET state=$3,provider_id=$4 WHERE tenant=$1 AND id=$2`, receipt.Tenant, receipt.IntentID, receipt.Outcome, receipt.ProviderID)
	if e != nil {
		return e
	}
	if receipt.Outcome == "failed" && state != "failed" && m.Purpose == "" && m.Channel != "sms" && r.SMSFallback != nil && r.SMSFallback.Account != "" && r.Authorize != nil && r.Authorize(ctx, m) == nil {
		fallback := m
		fallback.ID = m.ID + ":sms-fallback"
		fallback.Account = r.SMSFallback.Account
		fallback.Channel = "sms"
		fallback.Text = MinimalSMSNotice
		fallback.State = ""
		fallback.Lease = ""
		if e = Enqueue(ctx, tx, fallback); e != nil {
			return e
		}
	}
	return tx.Commit()
}
