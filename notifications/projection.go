package notifications

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"sort"
	"strings"
	"time"
)

func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDenied
	}
	_, e := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_notifications_activation(tenant text PRIMARY KEY,configuration jsonb NOT NULL,activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),last_case text NOT NULL DEFAULT '',last_version bigint NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS addon_notifications_intent(tenant text NOT NULL,id text NOT NULL,case_id text NOT NULL,unit_id text NOT NULL,event_version bigint NOT NULL,recipient_user text NOT NULL,state text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(tenant,id),UNIQUE(tenant,case_id,event_version,recipient_user));
 CREATE TABLE IF NOT EXISTS addon_notifications_erased(tenant text NOT NULL,id text NOT NULL,PRIMARY KEY(tenant,id))`)
	return e
}
func (s Service) configuration() string {
	u := append([]string(nil), s.Units...)
	sort.Strings(u)
	b, _ := json.Marshal(struct {
		Account string
		Units   []string
	}{s.Account, u})
	return string(b)
}
func intentID(tenant, caseID string, version uint64, user string) string {
	b, _ := json.Marshal([]any{tenant, caseID, version, delivery.HandlerEscalation, user})
	h := sha256.Sum256(b)
	return "handler-" + hex.EncodeToString(h[:])
}

// Activate is explicitly called by host opt-in composition, never by Project.
// First activation fixes the current database time and route/scope forever.
// Re-enabling the same configuration preserves deduplication and the boundary.
func (s Service) Activate(ctx context.Context) error {
	if !s.valid() {
		return ErrDenied
	}
	seen := map[string]bool{}
	for _, u := range s.Units {
		if u == "" || u == "*" || strings.TrimSpace(u) != u || seen[u] {
			return ErrDenied
		}
		seen[u] = true
	}
	var same bool
	e := s.DB.QueryRowContext(ctx, `INSERT INTO addon_notifications_activation(tenant,configuration) VALUES($1,$2) ON CONFLICT(tenant) DO UPDATE SET tenant=EXCLUDED.tenant RETURNING configuration=$2::jsonb`, s.Tenant, s.configuration()).Scan(&same)
	if e != nil {
		return e
	}
	if !same {
		return ErrDenied
	}
	return nil
}

// Project atomically records bounded scan progress, one immutable intent per
// committed escalation and delivery enqueue. Rescans catch late commits without
// timestamp high-watermarks. A blocked mapping is durable and never auto-retried.
func (s Service) Project(ctx context.Context) (int, error) {
	if !s.valid() {
		return 0, ErrDenied
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	var since time.Time
	var after cases.NotificationCursor
	var same bool
	e = tx.QueryRowContext(ctx, `SELECT activated_at,last_case,last_version,configuration=$2::jsonb FROM addon_notifications_activation WHERE tenant=$1 FOR UPDATE`, s.Tenant, s.configuration()).Scan(&since, &after.CaseID, &after.Version, &same)
	if e != nil {
		return 0, e
	}
	if !same {
		return 0, ErrDenied
	}
	facts, e := cases.EscalationFacts(ctx, tx, s.scope(), since, after, 100)
	if e != nil {
		return 0, e
	}
	count := 0
	for _, f := range facts {
		after = cases.NotificationCursor{CaseID: f.CaseID, Version: f.Version}
		id := intentID(s.Tenant, f.CaseID, f.Version, f.HandlerID)
		var seen bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_notifications_intent WHERE tenant=$1 AND id=$2 UNION ALL SELECT 1 FROM addon_notifications_erased WHERE tenant=$1 AND id=$2)`, s.Tenant, id).Scan(&seen); e != nil {
			return 0, e
		}
		if seen {
			continue
		}
		state := "blocked"
		_, emp, authErr := s.resolve(ctx, f.CaseID, f.Version, f.HandlerID)
		if authErr != nil && !errors.Is(authErr, ErrDenied) && !errors.Is(authErr, api.ErrDenied) && !errors.Is(authErr, cases.ErrDenied) && !errors.Is(authErr, directory.ErrDenied) {
			return 0, authErr
		}
		if authErr == nil {
			m := delivery.Message{ID: id, Tenant: s.Tenant, Account: s.Account, Sender: emp.Phone, EmployeeID: emp.ID, CaseID: f.CaseID, Text: Notice, Purpose: delivery.HandlerEscalation, RecipientUserID: f.HandlerID, EventVersion: f.Version}
			if e = delivery.Enqueue(ctx, tx, m); e != nil {
				return 0, e
			}
			erased, err := delivery.IntentErased(ctx, tx, s.Tenant, id)
			if err != nil {
				return 0, err
			}
			if erased {
				state = "erased"
			} else {
				state = "queued"
			}
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO addon_notifications_intent(tenant,id,case_id,unit_id,event_version,recipient_user,state) VALUES($1,$2,$3,$4,$5,$6,$7)`, s.Tenant, id, f.CaseID, f.UnitID, f.Version, f.HandlerID, state); e != nil {
			return 0, e
		}
		count++
	}
	if len(facts) < 100 {
		after = cases.NotificationCursor{}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE addon_notifications_activation SET last_case=$2,last_version=$3 WHERE tenant=$1`, s.Tenant, after.CaseID, after.Version); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	return count, nil
}
