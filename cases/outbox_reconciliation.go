package cases

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// BindOutboxRoute records the approved provider account before any carrier call.
// Route changes after binding are forbidden, including after an uncertain send.
func (r *SQLRepository) BindOutboxRoute(ctx context.Context, c OutboxClaim, account string) error {
	if r == nil || r.DB == nil || account == "" || len(account) > 512 {
		return ErrInvalid
	}
	result, e := r.DB.ExecContext(ctx, `UPDATE addon_cases_delivery SET account_id=$5 WHERE id=$1 AND organization_id=$2 AND worker=$3 AND token=$4 AND state='sending' AND lease_until>clock_timestamp() AND (account_id='' OR account_id=$5)`, c.Message.ID, c.Message.OrganizationID, c.WorkerID, c.Token, account)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n != 1 {
		return ErrConflict
	}
	return e
}

type OutboxDeliveryStatus struct{ IntentID, Account, ProviderID, State string }

func (r *SQLRepository) OutboxDeliveryStatus(ctx context.Context, tenant, id string) (OutboxDeliveryStatus, error) {
	if r == nil || r.DB == nil || tenant == "" || id == "" {
		return OutboxDeliveryStatus{}, ErrInvalid
	}
	var s OutboxDeliveryStatus
	e := r.DB.QueryRowContext(ctx, `SELECT id,account_id,provider_id,state FROM addon_cases_delivery WHERE organization_id=$1 AND id=$2`, tenant, id).Scan(&s.IntentID, &s.Account, &s.ProviderID, &s.State)
	if errors.Is(e, sql.ErrNoRows) {
		return s, ErrDenied
	}
	return s, e
}

// ReconcileOutbox never sends. Verified receipts settle accepted/unknown attempts
// atomically with immutable audit evidence. Replayed evidence is idempotent;
// altered receipt IDs and conflicting terminal outcomes are denied.
func (r *SQLRepository) ReconcileOutbox(ctx context.Context, receipt api.Receipt, verify api.ReceiptVerifier) error {
	fingerprint, e := api.VerifyReceipt(ctx, receipt, verify)
	if e != nil {
		return ErrDenied
	}
	if r == nil || r.DB == nil {
		return ErrInvalid
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var account, provider, state string
	e = tx.QueryRowContext(ctx, `SELECT account_id,provider_id,state FROM addon_cases_delivery WHERE organization_id=$1 AND id=$2 FOR UPDATE`, receipt.Tenant, receipt.IntentID).Scan(&account, &provider, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrDenied
	}
	if e != nil {
		return e
	}
	if account == "" || account != receipt.Account || (provider != "" && provider != receipt.ProviderID) {
		return ErrDenied
	}
	var prior string
	e = tx.QueryRowContext(ctx, `SELECT fingerprint FROM addon_cases_delivery_receipt WHERE tenant=$1 AND account=$2 AND receipt_id=$3`, receipt.Tenant, receipt.Account, receipt.ReceiptID).Scan(&prior)
	if e == nil {
		if prior != fingerprint {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if state == "delivered" || state == "failed" {
		if state != receipt.Outcome {
			return ErrConflict
		}
	} else if state != "sending" && state != "accepted" && state != "held_unknown" {
		return ErrConflict
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_cases_delivery_receipt(tenant,account,receipt_id,intent_id,provider_id,fingerprint,outcome,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receipt.Tenant, receipt.Account, receipt.ReceiptID, receipt.IntentID, receipt.ProviderID, fingerprint, receipt.Outcome, receipt.ObservedAt)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE addon_cases_delivery SET state=$3,provider_id=$4,updated_at=now() WHERE organization_id=$1 AND id=$2`, receipt.Tenant, receipt.IntentID, receipt.Outcome, receipt.ProviderID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
