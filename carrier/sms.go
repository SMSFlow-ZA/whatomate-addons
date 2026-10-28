package carrier

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"strings"
)

type SMSRoute struct{ Tenant, Account, WhatsAppAccount string }
type ReceiptPage struct {
	Receipts   []Status
	NextCursor string
	HasMore    bool
}

func (s Service) smsRoute(tenant, wa string) (SMSRoute, bool) {
	var route SMSRoute
	count := 0
	for _, v := range s.SMSRoutes {
		if v.Tenant == tenant && v.WhatsAppAccount == wa && v.Account != "" && s.bound(tenant, wa) {
			route = v
			count++
		}
	}
	return route, count == 1
}
func (s Service) smsAccount(tenant, account string) bool {
	count := 0
	for _, v := range s.SMSRoutes {
		if v.Tenant == tenant && v.Account == account {
			if _, ok := s.smsRoute(tenant, v.WhatsAppAccount); ok {
				count++
			}
		}
	}
	return count == 1
}
func (s Service) receiptBound(tenant, account string) bool {
	return s.bound(tenant, account) || s.smsAccount(tenant, account)
}
func (s Service) fallback(tenant, wa string) *delivery.SMSFallbackConfig {
	if route, ok := s.smsRoute(tenant, wa); ok {
		return &delivery.SMSFallbackConfig{Account: route.Account}
	}
	return nil
}
func (s Service) authorize(ctx context.Context, m delivery.Message) error {
	if s.Conversation == nil {
		return ErrDenied
	}
	if m.Channel == "sms" {
		if s.SendSMSObserved == nil || !s.smsAccount(m.Tenant, m.Account) || m.Text != delivery.MinimalSMSNotice || !strings.HasSuffix(m.ID, ":sms-fallback") {
			return ErrDenied
		}
	} else {
		if s.SendObserved == nil || s.AuthorizeWindow(ctx, m) != nil {
			return ErrDenied
		}
	}
	return s.authorizePurpose(ctx, m)
}

// Receipt reconciliation uses the current purpose policy without requiring a
// still-open sending window. It never creates a new handler fallback/send.
func (s Service) authorizePurpose(ctx context.Context, m delivery.Message) error {
	switch m.Purpose {
	case "":
		if s.Conversation == nil || m.RecipientUserID != "" || m.EventVersion != 0 {
			return ErrDenied
		}
		return s.Conversation.AuthorizeDelivery(ctx, m)
	case delivery.HandlerEscalation:
		if s.AuthorizeHandler == nil || m.Channel == "sms" {
			return ErrDenied
		}
		return s.AuthorizeHandler(ctx, m)
	default:
		return ErrDenied
	}
}
func persistReceipt(ctx context.Context, tx *sql.Tx, v Status) error {
	if v.ProviderClientID != "" {
		key, e := smsClientKey(v.ProviderClientID)
		if e != nil {
			return e
		}
		v.ProviderClientID = key
	}
	v.ObservedAt = v.ObservedAt.UTC()
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	canonical := v
	canonical.Evidence = ""
	// A provider UUID authenticates the routing meaning independently of the
	// erasable local mapping. The first durable payload/route projection is never
	// overwritten, and a different provider UUID still conflicts.
	if canonical.ProviderClientID != "" {
		canonical.IntentID = ""
	}
	semantics, e := json.Marshal(canonical)
	if e != nil {
		return e
	}
	fp := hash(semantics)
	var actual string
	e = tx.QueryRowContext(ctx, `INSERT INTO addon_carrier_receipt(tenant,account,id,fingerprint,payload,route_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant,account,id) DO UPDATE SET id=EXCLUDED.id RETURNING fingerprint`, v.Tenant, v.Account, v.ReceiptID, fp, raw, v.IntentID).Scan(&actual)
	if e != nil {
		return e
	}
	if actual != fp {
		return ErrDenied
	}
	if v.Outcome == "accepted" {
		_, e = tx.ExecContext(ctx, `UPDATE addon_carrier_receipt SET state='observed' WHERE tenant=$1 AND account=$2 AND id=$3 AND state='pending'`, v.Tenant, v.Account, v.ReceiptID)
	}
	return e
}

// PollSMSReceipts commits authenticated receipt metadata and its cursor together.
// A poll/parse/commit error cannot advance the cursor past unrecorded evidence.
func (s Service) PollSMSReceipts(ctx context.Context) error {
	if s.PollSMS == nil {
		return nil
	}
	if s.DB == nil {
		return ErrDenied
	}
	var failures []error
	for _, route := range s.SMSRoutes {
		if !s.smsAccount(route.Tenant, route.Account) {
			failures = append(failures, ErrDenied)
			continue
		}
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		func() {
			defer tx.Rollback()
			if _, e = tx.ExecContext(ctx, `INSERT INTO addon_carrier_sms_cursor(tenant,account,cursor) VALUES($1,$2,'') ON CONFLICT DO NOTHING`, route.Tenant, route.Account); e != nil {
				return
			}
			var cursor string
			if e = tx.QueryRowContext(ctx, `SELECT cursor FROM addon_carrier_sms_cursor WHERE tenant=$1 AND account=$2 FOR UPDATE`, route.Tenant, route.Account).Scan(&cursor); e != nil {
				return
			}
			var page ReceiptPage
			page, e = s.PollSMS(ctx, route.Tenant, route.Account, cursor)
			if e != nil {
				return
			}
			if len(page.Receipts) > 1000 || len(page.NextCursor) > 4096 || (page.HasMore && (page.NextCursor == "" || page.NextCursor == cursor)) {
				e = ErrDenied
				return
			}
			for _, r := range page.Receipts {
				if r.Tenant != route.Tenant || r.Account != route.Account || r.ProviderID == "" || r.ReceiptID == "" || r.Evidence == "" || r.ObservedAt.IsZero() || (r.Outcome != "delivered" && r.Outcome != "failed" && r.Outcome != "accepted") {
					e = ErrDenied
					return
				}
				if e = persistReceipt(ctx, tx, r); e != nil {
					return
				}
			}
			if _, e = tx.ExecContext(ctx, `UPDATE addon_carrier_sms_cursor SET cursor=$3 WHERE tenant=$1 AND account=$2`, route.Tenant, route.Account, page.NextCursor); e != nil {
				return
			}
			e = tx.Commit()
		}()
		if e != nil {
			failures = append(failures, e)
			continue
		}
	}
	return errors.Join(failures...)
}

// enqueueCaseFallbacks scans the cases owner's known-failure port fairly. The
// scan cursor and stable SMS intent commit together; unknown cases never enter it.
func (s Service) enqueueCaseFallbacks(ctx context.Context, b Binding) error {
	route, ok := s.smsRoute(b.Tenant, b.Account)
	if !ok {
		return nil
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO addon_carrier_case_scan(tenant,account,last_id) VALUES($1,$2,'') ON CONFLICT DO NOTHING`, b.Tenant, b.Account); e != nil {
		return e
	}
	var after string
	if e = tx.QueryRowContext(ctx, `SELECT last_id FROM addon_carrier_case_scan WHERE tenant=$1 AND account=$2 FOR UPDATE`, b.Tenant, b.Account).Scan(&after); e != nil {
		return e
	}
	failed, e := (&cases.SQLRepository{DB: s.DB}).FailedAcknowledgementsPage(ctx, b.Tenant, after)
	if e != nil {
		return e
	}
	next := ""
	for _, f := range failed {
		next = f.Message.ID
		if f.Account != b.Account {
			continue
		}
		m, e := s.Conversation.CaseAcknowledgement(ctx, f.Message, b.Account)
		if e != nil {
			continue
		}
		m.ID += ":sms-fallback"
		m.Channel = "sms"
		m.Account = route.Account
		m.Text = delivery.MinimalSMSNotice
		m.State = ""
		m.Lease = ""
		if e = delivery.Enqueue(ctx, tx, m); e != nil {
			return e
		}
		erased, e := delivery.IntentErased(ctx, tx, m.Tenant, m.ID)
		if e != nil {
			return e
		}
		if erased {
			continue
		}

		store, e := directory.NewPostgresStore(s.DB, m.Tenant, "carrier-fallback")
		if e != nil {
			return e
		}
		snap, e := store.Load(ctx)
		if e != nil {
			return e
		}
		emp, e := directory.Match(snap, m.Sender, s.Clock(), s.Conversation.Config.Freshness)
		if e != nil || emp.ID != m.EmployeeID {
			return ErrDenied
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO addon_carrier_route(tenant,account,id,employee_id,unit_id,case_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, m.Tenant, m.Account, m.ID, m.EmployeeID, emp.Zone, m.CaseID); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE addon_carrier_case_scan SET last_id=$3 WHERE tenant=$1 AND account=$2`, b.Tenant, b.Account, next); e != nil {
		return e
	}
	return tx.Commit()
}
func smsClientKey(v string) (string, error) {
	v = strings.ToLower(strings.ReplaceAll(v, "-", ""))
	if len(v) != 32 {
		return "", ErrDenied
	}
	if _, e := hex.DecodeString(v); e != nil {
		return "", ErrDenied
	}
	return v, nil
}

// BindSMSCorrelation persists the opaque provider UUID before its single submit.
func (s Service) BindSMSCorrelation(ctx context.Context, m delivery.Message, providerClientID string) error {
	key, e := smsClientKey(providerClientID)
	if e != nil || s.DB == nil || !s.smsAccount(m.Tenant, m.Account) || m.ID == "" || m.EmployeeID == "" {
		return ErrDenied
	}
	result, e := s.DB.ExecContext(ctx, `UPDATE addon_carrier_route SET provider_client_id=$5 WHERE tenant=$1 AND account=$2 AND id=$3 AND employee_id=$4 AND (provider_client_id='' OR provider_client_id=$5)`, m.Tenant, m.Account, m.ID, m.EmployeeID, key)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrDenied
	}
	return nil
}
func (s Service) ResolveSMSCorrelation(ctx context.Context, tenant, account, providerClientID string) (string, error) {
	key, e := smsClientKey(providerClientID)
	if e != nil || s.DB == nil || !s.smsAccount(tenant, account) {
		return "", ErrDenied
	}
	var id string
	e = s.DB.QueryRowContext(ctx, `SELECT id FROM addon_carrier_route WHERE tenant=$1 AND account=$2 AND provider_client_id=$3`, tenant, account, key).Scan(&id)
	if e != nil {
		return "", ErrDenied
	}
	return id, nil
}
