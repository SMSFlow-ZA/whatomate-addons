//go:build integration

package notifications

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"strings"
	"testing"
	"time"
)

func TestNotificationPrivacyLineageHoldSubjectIsolationAndRedactedExport(t *testing.T) {
	s, c, _ := setup(t)
	ctx := context.Background()
	if e := s.Activate(ctx); e != nil {
		t.Fatal(e)
	}
	v := escalate(t, s, c, "privacy")
	if _, e := s.Project(ctx); e != nil {
		t.Fatal(e)
	}
	actor := api.Actor{OrganizationID: s.Tenant, UserID: "handler", SessionID: "synthetic", MFA: true, Permissions: []string{"conversation:privacy", string(cases.WorkAssigned)}, Grants: []api.Grant{{Permission: "conversation:privacy", UnitID: "unit"}, {Permission: string(cases.WorkAssigned), UnitID: "unit"}}}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	scope := governance.Scope{OrganizationID: s.Tenant, UnitID: "unit"}
	adapter := PrivacyAdapter{}
	records, e := adapter.Discover(ctx, tx, actor, scope, "reporter")
	if e != nil || len(records) != 1 {
		t.Fatalf("notice inventory: %+v %v", records, e)
	}
	if records[0].LineageID != "cases:"+v.ID {
		t.Fatal("case lineage missing")
	}
	other, e := adapter.Discover(ctx, tx, actor, scope, "unrelated")
	if e != nil || len(other) != 0 {
		t.Fatalf("unrelated subject inventory: %+v %v", other, e)
	}
	raw, e := adapter.Export(ctx, tx, actor, records[0])
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || len(out) != 3 || out["state"] != "queued" || out["case_reference"] != v.ID {
		t.Fatalf("redacted export shape: %s", raw)
	}
	for _, secret := range []string{"+12025550101", "employee_id", "recipient_user"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("recipient data exposed: %s", raw)
		}
	}
	for _, source := range []governance.Source{governance.Case, governance.Backup, governance.Source("unknown")} {
		bad := records[0]
		bad.Source = source
		if _, e = adapter.Export(ctx, tx, actor, bad); e == nil {
			t.Fatal("unrecognized source exported")
		}
	}
	bad := records[0]
	bad.SubjectID = "unrelated"
	if _, e = adapter.Export(ctx, tx, actor, bad); e == nil {
		t.Fatal("unrelated subject exported")
	}
	root := governance.Record{ID: "cases:" + v.ID, LineageID: "cases:" + v.ID, Scope: scope, SubjectID: "reporter", Source: governance.Case, Reference: v.ID, CreatedAt: time.Now().Add(-time.Hour), Generation: 1}
	all := append([]governance.Record{root}, records...)
	plan, e := governance.Plan(governance.SubjectRequest{ID: "delete", Scope: scope, SubjectID: "reporter", Kind: governance.Delete, Approved: true}, all, []governance.Rule{{Source: governance.Case, Approved: true}, {Source: governance.Conversation, Approved: true}}, []governance.Hold{{ID: "hold", Scope: scope, LineageID: root.ID, Active: true}}, time.Now())
	if e != nil || len(plan) != 2 {
		t.Fatalf("hold plan: %+v %v", plan, e)
	}
	for _, entry := range plan {
		if entry.State != governance.BlockedHold {
			t.Fatal("case hold did not protect notice", entry)
		}
	}
}
