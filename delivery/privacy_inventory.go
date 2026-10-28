package delivery

import (
	"context"
	"database/sql"
	"time"
)

// PrivacyInventory is content-minimal owner metadata for a governed erasure.
// It intentionally has no sender, message text, carrier ID or receipt evidence.
type PrivacyInventory struct {
	Exists           bool
	PossibleExternal bool
	CreatedAt        time.Time
	CaseIDs          []string
}

// EmployeePrivacyInventory inspects only this delivery owner's tables inside the
// governance transaction. Existing delivery rows are locked against state changes
// until discovery, local erasure and independent pending-journal writes finish.
// PossibleExternal means possible transport retention, never successful delivery.
func EmployeePrivacyInventory(ctx context.Context, tx *sql.Tx, tenant, employeeID string) (PrivacyInventory, error) {
	var out PrivacyInventory
	if tx == nil || tenant == "" || employeeID == "" {
		return out, ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT COALESCE(d.payload->>'case_id',''),d.created_at,
 (d.state IN ('sending','accepted','sent','delivered','held_unknown') OR d.provider_id<>'' OR EXISTS(
 SELECT 1 FROM addon_conversation_delivery_receipt r WHERE r.tenant=d.tenant AND r.intent_id=d.id))
	 FROM addon_conversation_delivery d WHERE d.tenant=$1 AND d.payload->>'employee_id'=$2 AND COALESCE(d.payload->>'purpose','')=''
 ORDER BY d.id FOR UPDATE OF d`, tenant, employeeID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var caseID string
		var created time.Time
		var external bool
		if e = rows.Scan(&caseID, &created, &external); e != nil {
			return PrivacyInventory{}, e
		}
		out.Exists = true
		out.PossibleExternal = out.PossibleExternal || external
		if out.CreatedAt.IsZero() || created.Before(out.CreatedAt) {
			out.CreatedAt = created
		}
		if caseID != "" && !seen[caseID] {
			seen[caseID] = true
			out.CaseIDs = append(out.CaseIDs, caseID)
		}
	}
	if e = rows.Err(); e != nil {
		return PrivacyInventory{}, e
	}
	return out, nil
}
