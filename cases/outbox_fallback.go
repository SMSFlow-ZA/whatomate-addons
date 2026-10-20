package cases

import (
	"context"
	"encoding/json"
)

// FailedAcknowledgement is a trusted worker read port owned by cases. It exposes
// only acknowledgements with known failure, never pending or unknown attempts.
type FailedAcknowledgement struct {
	Message OutboxMessage
	Account string
}

// FailedAcknowledgementsPage uses a stable keyset. The composition must retain
// its scan position durably, wrap at the end, and deduplicate fallback intents.
func (r *SQLRepository) FailedAcknowledgementsPage(ctx context.Context, tenant, after string) ([]FailedAcknowledgement, error) {
	if r == nil || r.DB == nil || tenant == "" {
		return nil, ErrInvalid
	}
	rows, e := r.DB.QueryContext(ctx, `SELECT o.payload,d.account_id FROM addon_cases_delivery d JOIN addon_cases_outbox o ON o.id=d.id AND o.organization_id=d.organization_id WHERE d.organization_id=$1 AND d.state='failed' AND d.account_id<>'' AND d.id>$2 ORDER BY d.id LIMIT 100`, tenant, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FailedAcknowledgement{}
	for rows.Next() {
		var raw []byte
		var v FailedAcknowledgement
		if e = rows.Scan(&raw, &v.Account); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v.Message); e != nil {
			return nil, e
		}
		if v.Message.OrganizationID != tenant {
			return nil, ErrDenied
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
