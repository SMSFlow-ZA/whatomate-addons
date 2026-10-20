//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"strings"
	"testing"
)

func TestPrivacyErasePreservesPendingExternalConversationLineage(t *testing.T) {
	for _, state := range []string{"accepted", "held_unknown", "sending", "sent", "delivered", "queued", "failed"} {
		t.Run(state, func(t *testing.T) {
			ctx, conversation, tenant := fixture(t)
			db := conversation.DB
			if e := governance.MigrateHTTP(ctx, db); e != nil {
				t.Fatal(e)
			}
			actor := api.Actor{OrganizationID: tenant, UserID: "privacy-reviewer", SessionID: "current", MFA: true}
			for _, permission := range []string{"conversation:privacy", "governance:inventory.write", "governance:request.delete", "governance:deletion.approve", "governance:deletion.record", "governance:policy.manage", "governance:journal.read", "governance:request.export"} {
				actor.Permissions = append(actor.Permissions, permission)
				actor.Grants = append(actor.Grants, api.Grant{Permission: permission, UnitID: "zone"})
			}
			turn := Turn{EmployeeID: "employee", Narrative: "PRIVATE SYNTHETIC NARRATIVE", Reply: "PRIVATE SYNTHETIC REPLY"}
			turn.IntakeSnapshot.Zone = "zone"
			raw, _ := json.Marshal(turn)
			if _, e := db.Exec(`INSERT INTO addon_conversation_turn VALUES($1,'account','+27820000001',$2)`, tenant, raw); e != nil {
				t.Fatal(e)
			}
			provider := ""
			if state == "accepted" || state == "delivered" {
				provider = "provider-proof"
			}
			message, _ := json.Marshal(map[string]string{"employee_id": "employee-alias", "text": "PRIVATE SYNTHETIC DELIVERY", "sender": "+27820000001"})
			if _, e := db.Exec(`INSERT INTO addon_conversation_delivery(id,tenant,sender,payload,state,provider_id) VALUES($1,$2,'+27820000001',$3,$4,$5)`, tenant+":delivery", tenant, message, state, provider); e != nil {
				t.Fatal(e)
			}
			if state == "accepted" {
				if _, e := db.Exec(`INSERT INTO addon_conversation_delivery_receipt(tenant,account,receipt_id,intent_id,provider_id,fingerprint,outcome,observed_at) VALUES($1,'synthetic-account','synthetic-receipt',$2,'provider-proof','synthetic-fingerprint','delivered',now())`, tenant, tenant+":delivery"); e != nil {
					t.Fatal(e)
				}
			}
			if state == "queued" {
				if _, e := db.Exec(`INSERT INTO addon_conversation_delivery(id,tenant,sender,payload,state) VALUES($1,$2,'+27820000001',$3,'accepted')`, tenant+":other", tenant+":other", message); e != nil {
					t.Fatal(e)
				}
			}
			scope := governance.Scope{OrganizationID: tenant, UnitID: "zone"}
			physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{PrivacyAdapter{}}, SubjectAliases: func(_ctx context.Context, _org, _subject string) ([]string, error) {
				return []string{"employee-alias"}, nil
			}}
			records, e := physical.Discover(ctx, actor, scope, "employee")
			if e != nil {
				t.Fatal(e)
			}
			external := state != "queued" && state != "failed"
			want := 1
			if external {
				want = 2
			}
			if len(records) != want {
				t.Fatalf("state=%s inventory=%+v want %d", state, records, want)
			}
			service := governance.Service{Repository: &governance.SQLRepository{DB: db}}
			p := governance.PrincipalForActor(actor)
			for _, source := range []governance.Source{governance.Conversation, governance.Provider} {
				if e = service.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
					t.Fatal(e)
				}
			}
			request, e := service.Request(ctx, p, governance.SubjectRequest{ID: "delete", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Reference: "synthetic:verified-authority"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = service.ApproveRequest(ctx, p, scope, request.ID, request.Version); e != nil {
				t.Fatal(e)
			}
			entries, e := physical.EraseRequest(ctx, actor, scope, request.ID)
			if e != nil || len(entries) != want {
				t.Fatal(entries, e)
			}
			for _, entry := range entries {
				if entry.Source == governance.Provider && (entry.State != governance.AwaitingExpiry || entry.ConfirmationReference != "") {
					t.Fatal("external expiry falsely confirmed", entry)
				}
				if entry.Source == governance.Conversation && entry.State != governance.RecordedApplied {
					t.Fatal(entry)
				}
			}
			for _, table := range []string{"addon_conversation_turn", "addon_conversation_delivery"} {
				var n int
				if e = db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s WHERE tenant=$1", table), tenant).Scan(&n); e != nil || n != 0 {
					t.Fatal("local payload not erased", table, n, e)
				}
			}
			journal, e := service.Journal(ctx, p, scope, request.ID)
			if e != nil || len(journal) != want {
				t.Fatal(journal, e)
			}
			objects, e := physical.ExportSubject(ctx, actor, scope, "employee")
			if e != nil {
				t.Fatal(e)
			}
			visible, e := physical.VisibleInventory(ctx, actor, scope)
			if e != nil || len(visible) != want {
				t.Fatal(visible, e)
			}
			all, _ := json.Marshal([]any{records, journal, objects, visible})
			for _, sensitive := range []string{"PRIVATE SYNTHETIC", "+27820000001", "provider-proof", "synthetic-fingerprint", "synthetic-receipt"} {
				if strings.Contains(string(all), sensitive) {
					t.Fatal("sensitive content leaked to retained lineage")
				}
			}
		})
	}
}
