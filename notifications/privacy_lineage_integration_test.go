//go:build integration

package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	"testing"
	"time"
)

type removedHoldAdapter struct{ PrivacyAdapter }

func (a removedHoldAdapter) Discover(ctx context.Context, tx *sql.Tx, actor api.Actor, scope governance.Scope, subject string) ([]governance.Record, error) {
	records, e := a.PrivacyAdapter.Discover(ctx, tx, actor, scope, subject)
	for i := range records {
		records[i].HoldLineageIDs = nil
	}
	return records, e
}

func TestAllPrivacyAdaptersHandlerRequestCannotEraseHeldCaseNotice(t *testing.T) {
	for _, home := range []string{"unit", "home-unit"} {
		t.Run(home, func(t *testing.T) {
			s, c, _ := setup(t)
			ctx := context.Background()
			for _, migrate := range []func(context.Context) error{func(ctx context.Context) error { return carrier.Migrate(ctx, s.DB) }, func(ctx context.Context) error { return conversation.Migrate(ctx, s.DB) }, func(ctx context.Context) error { return governance.MigrateHTTP(ctx, s.DB) }, func(ctx context.Context) error { return knowledge.MigrateHTTP(ctx, s.DB) }, (ai.SQLStore{DB: s.DB}).Migrate, (reports.Store{DB: s.DB}).Migrate, (&cases.SQLRepository{DB: s.DB}).MigrateOutboxDelivery} {
				if e := migrate(ctx); e != nil {
					t.Fatal(e)
				}
			}
			store, _ := directory.NewPostgresStore(s.DB, s.Tenant, "synthetic")
			snap, e := store.Load(ctx)
			if e != nil {
				t.Fatal(e)
			}
			emp := snap.Employees["employee"]
			emp.Zone = home
			snap.Employees[emp.ID] = emp
			snap.Version++
			if e = store.CompareAndSwap(ctx, snap.Version-1, snap); e != nil {
				t.Fatal(e)
			}
			if e = s.Activate(ctx); e != nil {
				t.Fatal(e)
			}
			v := escalate(t, s, c, "held-case")
			if _, e = s.Project(ctx); e != nil {
				t.Fatal(e)
			}
			m := messages(t, s)[0]
			// A normal handler chat is present, making both generic employee privacy
			// adapters relevant; carrier route mirrors actual outbound binding.
			turn, _ := json.Marshal(conversation.Turn{EmployeeID: "employee", IntakeSnapshot: cases.Classification{Zone: home}, Reply: "ordinary reply"})
			if _, e = s.DB.ExecContext(ctx, `INSERT INTO addon_conversation_turn(tenant,account,sender,payload) VALUES($1,'account',$2,$3)`, s.Tenant, emp.Phone, turn); e != nil {
				t.Fatal(e)
			}
			transport := carrier.Service{DB: s.DB, Conversation: &conversation.Service{DB: s.DB, Config: conversation.HTTPConfig{Freshness: time.Hour, Clock: time.Now}}, Clock: time.Now, Bindings: []carrier.Binding{{Tenant: s.Tenant, Account: s.Account, PhoneID: "synthetic-phone", AppSecret: "synthetic-secret"}}, AuthorizeHandler: s.Authorize, SendObserved: func(context.Context, delivery.Message) (delivery.Result, string, error) {
				return delivery.Accepted, "synthetic-provider", nil
			}}
			if e = transport.AcceptBatch(ctx, []conversation.Inbound{{Tenant: s.Tenant, Account: s.Account, Sender: emp.Phone, ProviderMessageID: "window", Text: "synthetic window", ProviderTimestamp: time.Now()}}, nil); e != nil {
				t.Fatal(e)
			}
			if e = transport.TickDispatch(ctx); e != nil {
				t.Fatal(e)
			}
			if e = transport.AcceptBatch(ctx, nil, []carrier.Status{{Tenant: s.Tenant, Account: s.Account, IntentID: m.ID, ProviderID: "synthetic-provider", ReceiptID: "synthetic-receipt", Outcome: "delivered", Evidence: "synthetic-signed", ObservedAt: time.Now()}}); e != nil {
				t.Fatal(e)
			}
			if e = transport.ReconcilePending(ctx); e != nil {
				t.Fatal(e)
			}
			a := api.Actor{OrganizationID: s.Tenant, UserID: "handler", SessionID: "synthetic", MFA: true}
			for _, permission := range []string{"conversation:privacy", string(cases.WorkAssigned), "governance:inventory.write", "governance:hold.manage", "governance:policy.manage", "governance:request.delete", "governance:request.correct", "governance:deletion.approve", "governance:deletion.record", "governance:request.export", "governance:restore.reconcile"} {
				a.Permissions = append(a.Permissions, permission)
				a.Grants = append(a.Grants, api.Grant{Permission: permission, UnitID: "unit"})
				if home != "unit" {
					a.Grants = append(a.Grants, api.Grant{Permission: permission, UnitID: home})
				}
			}
			physical := governance.PhysicalService{DB: s.DB, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}, directory.PrivacyAdapter{}, knowledge.PrivacyAdapter{}, ai.PrivacyAdapter{}, reports.PrivacyAdapter{}, conversation.PrivacyAdapter{}, carrier.PrivacyAdapter{}, PrivacyAdapter{}}, SubjectAliases: func(_ context.Context, _ string, subject string) ([]string, error) {
				if subject == "employee" {
					return []string{"handler"}, nil
				}
				return nil, nil
			}}
			scope := governance.Scope{OrganizationID: s.Tenant, UnitID: "unit"}
			svc := governance.Service{Repository: &governance.SQLRepository{DB: s.DB}}
			p := governance.PrincipalForActor(a)
			// Register reporter-owned case first and apply the real persisted case hold.
			if _, e = physical.Discover(ctx, a, scope, "reporter"); e != nil {
				t.Fatal(e)
			}
			if e = svc.SetHold(ctx, p, governance.Hold{ID: "case-hold", Scope: scope, LineageID: "cases:" + v.ID, Reference: "synthetic:hold", Active: true}); e != nil {
				t.Fatal(e)
			}
			for _, source := range []governance.Source{governance.Case, governance.Conversation, governance.Provider} {
				if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
					t.Fatal(e)
				}
			}
			if home != "unit" {
				homeScope := governance.Scope{OrganizationID: s.Tenant, UnitID: home}
				for _, source := range []governance.Source{governance.Conversation, governance.Provider} {
					if e = svc.SetRule(ctx, p, homeScope, governance.Rule{Source: source, Approved: true}); e != nil {
						t.Fatal(e)
					}
				}
				req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "home-delete", Scope: homeScope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:request"})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = svc.ApproveRequest(ctx, p, homeScope, req.ID, req.Version); e != nil {
					t.Fatal(e)
				}
				if _, e = physical.EraseRequest(ctx, a, homeScope, req.ID); e != nil {
					t.Fatalf("home-scope ordinary chat deletion: %v", e)
				}
				if got := messages(t, s); len(got) != 1 || got[0].ID != m.ID {
					t.Fatal("home-scope request erased protected case notice")
				}
			}
			// Request in the case unit, even when this handler's home roster unit differs.
			request, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "handler-delete", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:request"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = svc.ApproveRequest(ctx, p, scope, request.ID, request.Version); e != nil {
				t.Fatal(e)
			}
			if _, e = physical.EraseRequest(ctx, a, scope, request.ID); !errors.Is(e, governance.ErrHold) {
				t.Fatalf("handler request bypassed case hold or scope discovery failed: %v", e)
			}
			got := messages(t, s)
			if len(got) != 1 || got[0].ID != m.ID {
				t.Fatal("held notice erased by generic employee adapter")
			}
			var routes int
			if e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_carrier_route WHERE tenant=$1 AND id=$2`, s.Tenant, m.ID).Scan(&routes); e != nil || routes != 1 {
				t.Fatalf("held route erased: %d %v", routes, e)
			}
			inventory, e := physical.Discover(ctx, a, scope, "employee")
			if e != nil {
				t.Fatal(e)
			}
			var handlerRecord governance.Record
			for _, r := range inventory {
				if r.Source == governance.Conversation && r.Reference == m.ID {
					handlerRecord = r
				}
			}
			if handlerRecord.ID == "" || handlerRecord.LineageID != handlerRecord.ID || len(handlerRecord.HoldLineageIDs) != 1 || handlerRecord.HoldLineageIDs[0] != "cases:"+v.ID {
				t.Fatalf("recipient notice orphaned from hold: %+v", handlerRecord)
			}
			removed := handlerRecord
			removed.HoldLineageIDs = nil
			if e = svc.Register(ctx, p, removed); !errors.Is(e, governance.ErrConflict) {
				t.Fatalf("registration removed hold dependency: %v", e)
			}
			badOwner := physical
			badOwner.Adapters = []governance.PhysicalAdapter{removedHoldAdapter{}}
			if _, e = badOwner.Discover(ctx, a, scope, "employee"); !errors.Is(e, governance.ErrConflict) {
				t.Fatalf("rediscovery removed hold dependency: %v", e)
			}
			correction, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "correct-handler-notice", Scope: scope, SubjectID: "employee", Kind: governance.Correct, Reference: "synthetic:correction"})
			if e != nil {
				t.Fatal(e)
			}
			if e = physical.Correct(ctx, a, scope, correction.ID, handlerRecord.ID, json.RawMessage(`{}`)); !errors.Is(e, governance.ErrHold) {
				t.Fatalf("correction skipped dependent hold: %v", e)
			}
			if _, e = physical.ReconcileSubjectRestore(ctx, a, scope, "employee", request.ID, "restore-held-notice"); !errors.Is(e, governance.ErrHold) {
				t.Fatalf("restore bypassed recipient's case hold: %v", e)
			}
			if e = svc.SetHold(ctx, p, governance.Hold{ID: "case-hold", Scope: scope, LineageID: "cases:" + v.ID, Reference: "synthetic:release", Active: false}); e != nil {
				t.Fatal(e)
			}
			if _, e = physical.EraseRequest(ctx, a, scope, request.ID); e != nil {
				t.Fatal(e)
			}
			if len(messages(t, s)) != 0 {
				t.Fatal("recipient notice orphaned after approved erasure")
			}
			var count int
			for _, table := range []string{"addon_notifications_intent", "addon_carrier_route"} {
				if e = s.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE tenant=$1 AND id=$2", s.Tenant, m.ID).Scan(&count); e != nil || count != 0 {
					t.Fatalf("notice derivative survived in %s: %d %v", table, count, e)
				}
			}
			var receiptState, payload string
			if e = s.DB.QueryRowContext(ctx, `SELECT state,payload::text FROM addon_carrier_receipt WHERE tenant=$1 AND id='synthetic-receipt'`, s.Tenant).Scan(&receiptState, &payload); e != nil || receiptState != "erased" || payload != "{}" {
				t.Fatalf("receipt tombstone: %s %s %v", receiptState, payload, e)
			}
			if e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM addon_cases_case WHERE organization_id=$1 AND id=$2`, s.Tenant, v.ID).Scan(&count); e != nil || count != 1 {
				t.Fatalf("recipient deletion erased reporting person's case: %d %v", count, e)
			}
		})
	}
}
