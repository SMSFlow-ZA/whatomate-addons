// Package notifications owns opt-in, content-minimal protected handler notices.
package notifications

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"time"
)

var ErrDenied = errors.New("handler notification denied")

const Notice = "An ER case is ready for your review. Open the protected ER workspace to view the case."

// Identity is supplied only by a trusted host resolver. It must load CURRENT
// active same-organization membership and role permissions intersected with
// exact unit grants, plus ALL historical identity aliases and employee links.
// No MFA/session claim is created: accessing the workspace still requires login.
type Identity struct {
	OrganizationID, UserID string
	Active                 bool
	Principal              cases.Principal
	EmployeeIDs            []string
}
type Service struct {
	DB              *sql.DB
	Tenant, Account string
	Units           []string
	Clock           func() time.Time
	Freshness       time.Duration
	Resolve         func(context.Context, string, string) (Identity, error)
}

func (s Service) scope() cases.NotificationScope {
	return cases.NotificationScope{OrganizationID: s.Tenant, UnitIDs: s.Units}
}
func (s Service) valid() bool {
	return s.DB != nil && s.Tenant != "" && s.Account != "" && len(s.Units) > 0 && s.Clock != nil && s.Freshness > 0 && s.Resolve != nil
}

func recipient(c cases.Case, user string, i Identity, d directory.Snapshot, now time.Time, freshness time.Duration) (directory.Employee, error) {
	if !i.Active || i.OrganizationID != c.OrganizationID || i.UserID != user || i.Principal.ID != user || !cases.CanNotifyHandler(i.Principal, c) {
		return directory.Employee{}, ErrDenied
	}
	return identityEmployee(i, d, now, freshness)
}
func identityEmployee(i Identity, d directory.Snapshot, now time.Time, freshness time.Duration) (directory.Employee, error) {
	var selected directory.Employee
	count := 0
	seen := map[string]bool{}
	for _, id := range i.EmployeeIDs {
		if id == "" || seen[id] {
			return selected, ErrDenied
		}
		seen[id] = true
		alias := false
		for _, a := range i.Principal.Aliases {
			if a == id {
				alias = true
			}
		}
		if !alias {
			return selected, ErrDenied
		}
		employee, exists := d.Employees[id]
		if !exists {
			continue
		}
		matched, e := directory.Match(d, employee.Phone, now, freshness)
		if e == nil && matched.ID == id {
			selected = matched
			count++
		}
	}
	if count != 1 {
		return directory.Employee{}, ErrDenied
	}
	return selected, nil
}
func (s Service) resolve(ctx context.Context, caseID string, version uint64, user string) (cases.EscalationFact, directory.Employee, error) {
	if !s.valid() || user == "" {
		return cases.EscalationFact{}, directory.Employee{}, ErrDenied
	}
	i, e := s.Resolve(ctx, s.Tenant, user)
	if e != nil {
		return cases.EscalationFact{}, directory.Employee{}, e
	}
	if !i.Active || i.OrganizationID != s.Tenant || i.UserID != user || i.Principal.ID != user {
		return cases.EscalationFact{}, directory.Employee{}, ErrDenied
	}
	fact, e := cases.CurrentNotification(ctx, s.DB, s.scope(), i.Principal, caseID, version)
	if e != nil {
		return fact, directory.Employee{}, e
	}
	store, e := directory.NewPostgresStore(s.DB, s.Tenant, "handler-notification")
	if e != nil {
		return fact, directory.Employee{}, e
	}
	snapshot, e := store.Load(ctx)
	if e != nil {
		return fact, directory.Employee{}, e
	}
	emp, e := identityEmployee(i, snapshot, s.Clock(), s.Freshness)
	return fact, emp, e
}

// Authorize is used at dispatch and receipt boundaries. Only the immutable
// projected intent is authorized; no arbitrary phone, text, route or case ID.
func (s Service) Authorize(ctx context.Context, m delivery.Message) error {
	if !s.valid() || m.Purpose != delivery.HandlerEscalation || m.Tenant != s.Tenant || m.Account != s.Account || m.Channel != "" && m.Channel != "whatsapp" || m.Text != Notice || m.RecipientUserID == "" || m.EventVersion == 0 || m.ID != intentID(m.Tenant, m.CaseID, m.EventVersion, m.RecipientUserID) {
		return ErrDenied
	}
	var active bool
	if e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM addon_notifications_intent n JOIN addon_notifications_activation a ON a.tenant=n.tenant WHERE n.tenant=$1 AND n.id=$2 AND n.state='queued' AND a.configuration=$3::jsonb AND NOT EXISTS(SELECT 1 FROM addon_notifications_erased x WHERE x.tenant=n.tenant AND x.id=n.id))`, s.Tenant, m.ID, s.configuration()).Scan(&active); e != nil || !active {
		if e != nil {
			return errors.Join(delivery.ErrAuthorizationRetryable, e)
		}
		return ErrDenied
	}
	_, emp, e := s.resolve(ctx, m.CaseID, m.EventVersion, m.RecipientUserID)
	if e != nil && !errors.Is(e, ErrDenied) && !errors.Is(e, api.ErrDenied) && !errors.Is(e, cases.ErrDenied) && !errors.Is(e, directory.ErrDenied) {
		return errors.Join(delivery.ErrAuthorizationRetryable, e)
	}
	if e != nil || emp.ID != m.EmployeeID || emp.Phone != m.Sender {
		return ErrDenied
	}
	return nil
}
