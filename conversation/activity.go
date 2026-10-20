package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"time"
)

// ActivityFact is immutable, content-free usage evidence. Outcome queued is not sent.
type ActivityFact struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	UnitID         string    `json:"unit_id"`
	CaseKey        string    `json:"case_key,omitempty"`
	Zone           string    `json:"zone"`
	Department     string    `json:"department"`
	CaseType       string    `json:"case_type"`
	Kind           string    `json:"kind"`
	Outcome        string    `json:"outcome"`
	OccurredAt     time.Time `json:"occurred_at"`
}

func recordActivity(ctx context.Context, tx *sql.Tx, f ActivityFact, caseID string) error {
	if caseID != "" {
		f.CaseKey = digest(f.OrganizationID + "\x00" + caseID)
	}
	b, e := json.Marshal(f)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_conversation_activity(tenant,id,case_id,payload) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, f.OrganizationID, f.ID, caseID, b)
	return e
}

// ActivityFacts reauthorizes linked cases via the case owner's reporting ports.
// Pre-case enquiry facts require exact current reporting scope for their creation unit.
// It returns no sender, employee identity, narrative or message text.
func ActivityFacts(ctx context.Context, db *sql.DB, a api.Actor) ([]ActivityFact, error) {
	return ActivityFactsFor(ctx, db, a, a.Has("reports:aggregate"))
}

func ActivityFactsFor(ctx context.Context, db *sql.DB, a api.Actor, aggregate bool) ([]ActivityFact, error) {
	if db == nil {
		return nil, ErrDenied
	}
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil {
		return nil, ErrDenied
	}
	permission := "reports:aggregate"
	allowed := map[string]bool{}
	if aggregate {
		if !a.Has(permission) {
			return nil, ErrDenied
		}
		facts, e := cases.AggregateFacts(ctx, db, a)
		if e != nil {
			return nil, e
		}
		for _, f := range facts {
			allowed[f.CaseID] = true
		}
	} else {
		permission = "reports:operational"
		if !a.Has(permission) {
			return nil, ErrDenied
		}
		facts, e := cases.ReportFacts(ctx, db, a)
		if e != nil {
			return nil, e
		}
		for _, f := range facts {
			allowed[digest(a.OrganizationID+"\x00"+f.CaseID)] = true
		}
	}
	rows, e := db.QueryContext(ctx, `SELECT payload FROM addon_conversation_activity WHERE tenant=$1 ORDER BY id`, a.OrganizationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ActivityFact{}
	for rows.Next() {
		var b []byte
		var f ActivityFact
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &f); e != nil {
			return nil, e
		}
		if f.OrganizationID != a.OrganizationID || !a.Allows(permission, f.UnitID) || (f.CaseKey != "" && !allowed[f.CaseKey]) {
			continue
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
