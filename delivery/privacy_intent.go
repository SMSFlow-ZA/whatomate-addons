package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// ReadPrivacyIntent is a storage-owner port. The caller must first authorize the
// exact subject, unit and linked case; raw content never belongs in inventory.
func ReadPrivacyIntent(ctx context.Context, tx *sql.Tx, tenant, id string) (json.RawMessage, bool, error) {
	var raw []byte
	var external bool
	if tx == nil || tenant == "" || id == "" {
		return nil, false, ErrInvalid
	}
	e := tx.QueryRowContext(ctx, `SELECT jsonb_set(payload,'{state}',to_jsonb(state)),(state IN ('sending','accepted','sent','delivered','held_unknown') OR provider_id<>'' OR EXISTS(SELECT 1 FROM addon_conversation_delivery_receipt r WHERE r.tenant=d.tenant AND r.intent_id=d.id)) FROM addon_conversation_delivery d WHERE tenant=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&raw, &external)
	return raw, external, e
}

// EraseIntent removes only this authorized intent and its owned receipt copies.
func EraseIntent(ctx context.Context, tx *sql.Tx, tenant, id string) error {
	if tx == nil || tenant == "" || id == "" {
		return ErrInvalid
	}
	if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("delivery-intent:%d:%s:%s", len(tenant), tenant, id)); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO addon_delivery_erased(tenant,id) VALUES($1,$2) ON CONFLICT DO NOTHING`, tenant, id); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `DELETE FROM addon_conversation_delivery_receipt WHERE tenant=$1 AND intent_id=$2`, tenant, id); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `DELETE FROM addon_conversation_delivery WHERE tenant=$1 AND id=$2`, tenant, id)
	return e
}

// IntentErased exposes a content-free owner tombstone, never erased content.
func IntentErased(ctx context.Context, tx *sql.Tx, tenant, id string) (bool, error) {
	var erased bool
	if tx == nil || tenant == "" || id == "" {
		return false, ErrInvalid
	}
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_delivery_erased WHERE tenant=$1 AND id=$2)`, tenant, id).Scan(&erased)
	return erased, e
}
