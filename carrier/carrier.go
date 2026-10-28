// Package carrier composes trusted carrier events with protected module-owned persistence.
// AcceptBatch is a trusted server port: hosts MUST authenticate raw provider payloads
// and resolve every account before calling it. No generic host tables are written.
package carrier

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
)

var ErrDenied = errors.New("protected carrier denied")

type Binding struct{ Tenant, Account, PhoneID, AppSecret string }
type Status struct {
	// ProviderClientID is immutable provider correlation evidence. IntentID may
	// be a derived local projection that disappears after governed erasure.
	ProviderClientID                                                    string
	Tenant, Account, ProviderID, IntentID, ReceiptID, Outcome, Evidence string
	ObservedAt                                                          time.Time
}
type Service struct {
	AuthorizeHandler func(context.Context, delivery.Message) error
	SMSRoutes        []SMSRoute
	SendSMSObserved  func(context.Context, delivery.Message) (delivery.Result, string, error)
	PollSMS          func(context.Context, string, string, string) (ReceiptPage, error)
	DB               *sql.DB
	Bindings         []Binding
	Conversation     *conversation.Service
	Clock            func() time.Time
	SendObserved     func(context.Context, delivery.Message) (delivery.Result, string, error)
}

func CanonicalNumber(v string) (string, error) {
	n := strings.TrimPrefix(v, "+")
	if len(n) < 8 || len(n) > 15 || n[0] == '0' {
		return "", ErrDenied
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return "", ErrDenied
		}
	}
	return "+" + n, nil
}
func (s Service) Binding(phone string) (Binding, bool) {
	var result Binding
	count := 0
	for _, b := range s.Bindings {
		if b.PhoneID == phone && b.Tenant != "" && b.Account != "" && b.AppSecret != "" {
			result = b
			count++
		}
	}
	return result, count == 1
}
func (s Service) bound(tenant, account string) bool {
	count := 0
	for _, b := range s.Bindings {
		if b.Tenant == tenant && b.Account == account {
			if _, ok := s.Binding(b.PhoneID); ok {
				count++
			}
		}
	}
	return count == 1
}
func hash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDenied
	}
	_, e := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_carrier_ingress(sequence bigserial,tenant text NOT NULL,account text NOT NULL,id text NOT NULL,fingerprint text NOT NULL,payload jsonb NOT NULL,state text NOT NULL DEFAULT 'pending',employee_id text NOT NULL DEFAULT '',unit_id text NOT NULL DEFAULT '',PRIMARY KEY(tenant,account,id));CREATE TABLE IF NOT EXISTS addon_carrier_receipt(tenant text NOT NULL,account text NOT NULL,id text NOT NULL,fingerprint text NOT NULL,payload jsonb NOT NULL,route_id text NOT NULL DEFAULT '',state text NOT NULL DEFAULT 'pending',PRIMARY KEY(tenant,account,id));CREATE TABLE IF NOT EXISTS addon_carrier_route(tenant text NOT NULL,account text NOT NULL,id text NOT NULL,employee_id text NOT NULL,unit_id text NOT NULL,PRIMARY KEY(tenant,account,id));CREATE TABLE IF NOT EXISTS addon_carrier_sms_cursor(tenant text NOT NULL,account text NOT NULL,cursor text NOT NULL,PRIMARY KEY(tenant,account));CREATE TABLE IF NOT EXISTS addon_carrier_case_scan(tenant text NOT NULL,account text NOT NULL,last_id text NOT NULL,PRIMARY KEY(tenant,account))`)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `ALTER TABLE addon_carrier_ingress ADD COLUMN IF NOT EXISTS employee_id text NOT NULL DEFAULT '';ALTER TABLE addon_carrier_ingress ADD COLUMN IF NOT EXISTS unit_id text NOT NULL DEFAULT '';ALTER TABLE addon_carrier_receipt ADD COLUMN IF NOT EXISTS route_id text NOT NULL DEFAULT '';ALTER TABLE addon_carrier_receipt ADD COLUMN IF NOT EXISTS attempted_at timestamptz NOT NULL DEFAULT 'epoch';ALTER TABLE addon_carrier_receipt ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT 'epoch';ALTER TABLE addon_carrier_route ADD COLUMN IF NOT EXISTS provider_client_id text NOT NULL DEFAULT '';ALTER TABLE addon_carrier_route ADD COLUMN IF NOT EXISTS case_id text NOT NULL DEFAULT '';CREATE UNIQUE INDEX IF NOT EXISTS addon_carrier_sms_client ON addon_carrier_route(tenant,account,provider_client_id) WHERE provider_client_id<>''`)
	return e
}

// AcceptBatch commits all authenticated message and receipt events before webhook acceptance.
// Replays with changed semantic payloads reject the complete transaction.
func (s Service) AcceptBatch(ctx context.Context, in []conversation.Inbound, statuses []Status) error {
	if s.DB == nil || s.Clock == nil || len(in)+len(statuses) == 0 || len(in)+len(statuses) > 1000 {
		return ErrDenied
	}
	employees := map[string]directory.Employee{}
	for i := range in {
		v := &in[i]
		if !s.bound(v.Tenant, v.Account) || v.ProviderTimestamp.IsZero() || v.ProviderTimestamp.After(s.Clock()) || v.ProviderMessageID == "" || len(v.ProviderMessageID) > 256 || strings.TrimSpace(v.Text) == "" || len(v.Text) > 4000 {
			return ErrDenied
		}
		n, e := CanonicalNumber(v.Sender)
		if e != nil {
			return e
		}
		v.Sender = n
		if s.Conversation == nil {
			return ErrDenied
		}
		store, e := directory.NewPostgresStore(s.DB, v.Tenant, "carrier-ingress")
		if e != nil {
			return e
		}
		snap, e := store.Load(ctx)
		if e != nil {
			return e
		}
		emp, e := directory.Match(snap, n, s.Clock(), s.Conversation.Config.Freshness)
		if e != nil || emp.Zone == "" {
			return ErrDenied
		}
		employees[v.Tenant+"\x00"+v.Account+"\x00"+v.ProviderMessageID] = emp
	}
	for _, v := range statuses {
		if !s.bound(v.Tenant, v.Account) || v.ReceiptID == "" || v.ProviderID == "" || v.ObservedAt.IsZero() || v.Evidence == "" || (v.Outcome != "delivered" && v.Outcome != "failed" && v.Outcome != "accepted") {
			return ErrDenied
		}
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	save := func(table, tenant, account, id string, v any) error {
		raw, e := json.Marshal(v)
		if e != nil {
			return e
		}
		fingerprintRaw := raw
		if st, ok := v.(Status); ok {
			st.Evidence = ""
			fingerprintRaw, e = json.Marshal(st)
			if e != nil {
				return e
			}
		}
		fp := hash(fingerprintRaw)
		var actual string
		if table == "addon_carrier_ingress" {
			emp := employees[tenant+"\x00"+account+"\x00"+id]
			e = tx.QueryRowContext(ctx, `INSERT INTO addon_carrier_ingress(tenant,account,id,fingerprint,payload,employee_id,unit_id) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant,account,id) DO UPDATE SET id=EXCLUDED.id RETURNING fingerprint`, tenant, account, id, fp, raw, emp.ID, emp.Zone).Scan(&actual)
		} else {
			e = tx.QueryRowContext(ctx, `INSERT INTO `+table+`(tenant,account,id,fingerprint,payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant,account,id) DO UPDATE SET id=EXCLUDED.id RETURNING fingerprint`, tenant, account, id, fp, raw).Scan(&actual)
		}
		if e == nil && actual != fp {
			return ErrDenied
		}
		return e
	}
	for _, v := range in {
		if e = save("addon_carrier_ingress", v.Tenant, v.Account, v.ProviderMessageID, v); e != nil {
			return e
		}
	}
	for _, v := range statuses {
		if e = persistReceipt(ctx, tx, v); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// ProcessOne serializes a tenant's ingress across workers. Receive owns semantic
// deduplication, so an unknown queue commit can safely replay the same provider ID.
func (s Service) ProcessOne(ctx context.Context, tenant string) (bool, error) {
	if !s.valid() {
		return false, ErrDenied
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var locked bool
	if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "carrier-ingress:"+tenant).Scan(&locked); e != nil || !locked {
		return false, e
	}
	var account, id, employeeID, unitID string
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT account,id,payload,employee_id,unit_id FROM addon_carrier_ingress WHERE tenant=$1 AND state='pending' ORDER BY sequence FOR UPDATE LIMIT 1`, tenant).Scan(&account, &id, &raw, &employeeID, &unitID)
	if errors.Is(e, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if e != nil {
		return false, e
	}
	var in conversation.Inbound
	if e = json.Unmarshal(raw, &in); e != nil {
		return false, e
	}
	state := "held"
	eligible := false
	if s.bound(in.Tenant, in.Account) {
		store, err := directory.NewPostgresStore(s.DB, in.Tenant, "carrier-worker")
		if err == nil {
			snap, err := store.Load(ctx)
			if err == nil {
				emp, err := directory.Match(snap, in.Sender, s.Clock(), s.Conversation.Config.Freshness)
				eligible = err == nil && emp.ID == employeeID && emp.Zone == unitID
			}
		}
	}
	if eligible {
		in.ExpectedEmployeeID = employeeID
		in.ExpectedUnitID = unitID
		_, e = s.Conversation.Receive(ctx, in)
		if e == nil {
			state = "processed"
		}
	}
	// Errors are intentionally held, with no sensitive error or message logging.
	if _, e = tx.ExecContext(ctx, `UPDATE addon_carrier_ingress SET state=$4 WHERE tenant=$1 AND account=$2 AND id=$3`, tenant, account, id, state); e != nil {
		return true, e
	}
	return true, tx.Commit()
}
func (s Service) dispatchAcknowledgement(ctx context.Context, b Binding) error {
	repo := &cases.SQLRepository{DB: s.DB}
	now := s.Clock()
	claim, ok, e := repo.ClaimOutbox(ctx, b.Tenant, "protected-carrier", now, now.Add(time.Minute))
	if e != nil || !ok {
		return e
	}
	m, e := s.Conversation.CaseAcknowledgement(ctx, claim.Message, b.Account)
	if e != nil || s.SendObserved == nil || s.AuthorizeWindow(ctx, m) != nil {
		return repo.FinishOutbox(ctx, claim, "held", "")
	}
	if e = repo.BindOutboxRoute(ctx, claim, b.Account); e != nil {
		return e
	}
	result, provider, e := s.send(ctx, m)
	state := "held_unknown"
	if e == nil && result == delivery.Accepted && provider != "" {
		state = "accepted"
	}
	if e == nil && result == delivery.VerifiedFailed {
		state = "failed"
	}
	return repo.FinishOutbox(ctx, claim, state, provider)
}

// Independent lanes let a host supervise slow AI, sending and reconciliation
// with separate contexts. Tick remains a convenient bounded synthetic runner.
func (s Service) Tick(ctx context.Context) error {
	if !s.valid() {
		return ErrDenied
	}
	receiptBefore := s.ReconcilePending(ctx)
	ingress := s.TickIngress(ctx)
	dispatch := s.TickDispatch(ctx)
	receipts := s.TickReceipts(ctx)
	return errors.Join(receiptBefore, ingress, dispatch, receipts)
}
func (s Service) valid() bool { return s.DB != nil && s.Conversation != nil && s.Clock != nil }
func (s Service) TickIngress(ctx context.Context) error {
	if !s.valid() {
		return ErrDenied
	}
	var failures []error
	seen := map[string]bool{}
	for _, b := range s.Bindings {
		if !s.bound(b.Tenant, b.Account) || seen[b.Tenant] {
			continue
		}
		seen[b.Tenant] = true
		for range 100 {
			more, e := s.ProcessOne(ctx, b.Tenant)
			if e != nil {
				failures = append(failures, e)
				break
			}
			if !more {
				break
			}
		}
	}
	return errors.Join(failures...)
}
func (s Service) TickDispatch(ctx context.Context) error {
	if !s.valid() {
		return ErrDenied
	}
	var failures []error
	seen := map[string]bool{}
	for _, b := range s.Bindings {
		if !s.bound(b.Tenant, b.Account) || seen[b.Tenant] {
			continue
		}
		seen[b.Tenant] = true
		if e := s.enqueueCaseFallbacks(ctx, b); e != nil {
			failures = append(failures, e)
		}
		w := delivery.Worker{DB: s.DB, Tenant: b.Tenant, Clock: s.Clock, LeaseDuration: time.Minute, Authorize: s.authorize, SendObserved: s.observedSender(), SMSFallback: s.fallback(b.Tenant, b.Account)}
		for range 100 {
			more, e := w.Dispatch(ctx)
			if e != nil {
				failures = append(failures, e)
				break
			}
			if !more {
				break
			}
		}
		if e := s.dispatchAcknowledgement(ctx, b); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s Service) TickReceipts(ctx context.Context) error {
	if !s.valid() {
		return ErrDenied
	}
	before := s.ReconcilePending(ctx)
	poll := s.PollSMSReceipts(ctx)
	after := s.ReconcilePending(ctx)
	return errors.Join(before, poll, after)
}

// ReconcilePending uses only persisted authenticated observations. Early/unmatched
// and conflicting evidence remains pending and never schedules another send.
func (s Service) ReconcilePending(ctx context.Context) error {
	if !s.valid() {
		return ErrDenied
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT payload FROM addon_carrier_receipt WHERE state='pending' AND next_attempt_at<=clock_timestamp() ORDER BY attempted_at,id LIMIT 100`)
	if e != nil {
		return e
	}
	var pending []Status
	for rows.Next() {
		var raw []byte
		var v Status
		if e = rows.Scan(&raw); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			rows.Close()
			return e
		}
		pending = append(pending, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range pending {
		if _, e = s.DB.ExecContext(ctx, `UPDATE addon_carrier_receipt SET attempted_at=clock_timestamp(),next_attempt_at=clock_timestamp()+interval '5 seconds' WHERE tenant=$1 AND account=$2 AND id=$3 AND state='pending'`, v.Tenant, v.Account, v.ReceiptID); e != nil {
			return e
		}
		if !s.receiptBound(v.Tenant, v.Account) {
			continue
		}
		id := v.IntentID
		// Provider IDs may resolve a known durable route; never infer across accounts.
		if id == "" {
			var ids []string
			rs, e := s.DB.QueryContext(ctx, `SELECT id FROM addon_conversation_delivery WHERE tenant=$1 AND payload->>'account'=$2 AND provider_id=$3 UNION ALL SELECT id FROM addon_cases_delivery WHERE organization_id=$1 AND account_id=$2 AND provider_id=$3`, v.Tenant, v.Account, v.ProviderID)
			if e != nil {
				return e
			}
			for rs.Next() {
				var x string
				if e = rs.Scan(&x); e != nil {
					rs.Close()
					return e
				}
				ids = append(ids, x)
			}
			e = rs.Err()
			rs.Close()
			if e != nil {
				return e
			}
			if len(ids) != 1 {
				continue
			}
			id = ids[0]
		}
		r := api.Receipt{Tenant: v.Tenant, Account: v.Account, IntentID: id, ProviderID: v.ProviderID, ReceiptID: v.ReceiptID, Outcome: v.Outcome, ObservedAt: v.ObservedAt, Evidence: v.Evidence}
		verify := func(_ context.Context, actual api.Receipt) error {
			if actual != r {
				return ErrDenied
			}
			return nil
		}
		// Correlation must be present before reconciliation, even for unknown submits.
		var count int
		if e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT id FROM addon_conversation_delivery WHERE tenant=$1 AND id=$2 AND payload->>'account'=$3 AND (provider_id='' OR provider_id=$4) UNION ALL SELECT id FROM addon_cases_delivery WHERE organization_id=$1 AND id=$2 AND account_id=$3 AND (provider_id='' OR provider_id=$4)) q`, v.Tenant, id, v.Account, v.ProviderID).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			continue
		}
		if _, e = delivery.ReadDeliveryStatus(ctx, s.DB, v.Tenant, id); e == nil {
			e = (delivery.Reconciler{DB: s.DB, Verify: verify, SMSFallback: s.fallback(v.Tenant, v.Account), Authorize: s.authorizePurpose}).Reconcile(ctx, r)
		} else {
			e = (&cases.SQLRepository{DB: s.DB}).ReconcileOutbox(ctx, r, verify)
		}
		if e == nil {
			if _, e = s.DB.ExecContext(ctx, `UPDATE addon_carrier_receipt SET state='reconciled',route_id=$4 WHERE tenant=$1 AND account=$2 AND id=$3 AND state<>'erased'`, v.Tenant, v.Account, v.ReceiptID, id); e != nil {
				return e
			}
		}
	}
	return nil
}

// AuthorizeWindow is based on signed provider time, never queue processing time.
func (s Service) AuthorizeWindow(ctx context.Context, m delivery.Message) error {
	if s.DB == nil || s.Clock == nil || !s.bound(m.Tenant, m.Account) {
		return ErrDenied
	}
	var stamp sql.NullTime
	e := s.DB.QueryRowContext(ctx, `SELECT max((payload->>'ProviderTimestamp')::timestamptz) FROM addon_carrier_ingress WHERE tenant=$1 AND account=$2 AND payload->>'Sender'=$3`, m.Tenant, m.Account, m.Sender).Scan(&stamp)
	if e != nil {
		return e
	}
	now := s.Clock()
	if !stamp.Valid || stamp.Time.After(now) || !stamp.Time.Add(24*time.Hour).After(now) {
		return ErrDenied
	}
	return nil
}

func (s Service) observedSender() func(context.Context, delivery.Message) (delivery.Result, string, error) {
	if s.SendObserved == nil && s.SendSMSObserved == nil {
		return nil
	}
	return s.send
}
func (s Service) send(ctx context.Context, m delivery.Message) (delivery.Result, string, error) {
	if s.authorize(ctx, m) != nil {
		return delivery.Unknown, "", ErrDenied
	}
	store, e := directory.NewPostgresStore(s.DB, m.Tenant, "carrier-dispatch")
	if e != nil {
		return delivery.Unknown, "", e
	}
	snap, e := store.Load(ctx)
	if e != nil {
		return delivery.Unknown, "", e
	}
	emp, e := directory.Match(snap, m.Sender, s.Clock(), s.Conversation.Config.Freshness)
	if e != nil || emp.ID != m.EmployeeID {
		return delivery.Unknown, "", ErrDenied
	}
	_, e = s.DB.ExecContext(ctx, `INSERT INTO addon_carrier_route(tenant,account,id,employee_id,unit_id,case_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, m.Tenant, m.Account, m.ID, emp.ID, emp.Zone, m.CaseID)
	if e != nil {
		return delivery.Unknown, "", e
	}
	if m.Channel == "sms" {
		return s.SendSMSObserved(ctx, m)
	}
	return s.SendObserved(ctx, m)
}
