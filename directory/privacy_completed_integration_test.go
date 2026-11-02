//go:build integration

package directory

import (
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"testing"
)

func TestCompletedDirectoryRequiresRevocationAndCurrentExactScope(t *testing.T) {
	db, _, binding, _ := identityFixture(t)
	ctx := t.Context()
	if e := MigrateHTTP(ctx, db); e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: "tenant", UserID: "privacy-reader", Permissions: []string{"directory:read", "directory:privacy"}, Grants: []api.Grant{{Permission: "directory:privacy", UnitID: "north"}}}
	r := governance.Record{ID: "directory:" + binding.EmployeeID, Reference: binding.EmployeeID, Scope: governance.Scope{OrganizationID: "tenant", UnitID: "north"}}
	p := PrivacyAdapter{}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if absent, e := p.CompletedSourceAbsentTx(ctx, tx, a, r); e != nil || absent {
		t.Fatal("present source reported absent", absent, e)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE directory_snapshots SET payload=jsonb_set(payload,'{Employees}',(payload->'Employees')-$2) WHERE tenant=$1`, a.OrganizationID, binding.EmployeeID); e != nil {
		t.Fatal(e)
	}
	if absent, e := p.CompletedSourceAbsentTx(ctx, tx, a, r); e != nil || absent {
		t.Fatal("unrevoked absence accepted", absent, e)
	}
	if e = p.Erase(ctx, tx, a, r); e != nil {
		t.Fatal(e)
	}
	if absent, e := p.CompletedSourceAbsentTx(ctx, tx, a, r); e != nil || !absent {
		t.Fatal("actual revoked erasure not recognized", absent, e)
	}
	for _, change := range []func(*api.Actor){func(v *api.Actor) { v.Grants = nil }, func(v *api.Actor) { v.Permissions = nil }, func(v *api.Actor) { v.OrganizationID = "foreign" }} {
		other := a
		change(&other)
		if absent, e := p.CompletedSourceAbsentTx(ctx, tx, other, r); e == nil || absent {
			t.Fatal("unauthorized completion read", absent, e)
		}
	}
	bad := r
	bad.Scope.UnitID = "foreign"
	if absent, e := p.CompletedSourceAbsentTx(ctx, tx, a, bad); e == nil || absent {
		t.Fatal("wrong scope accepted", e)
	}
	bad = r
	bad.Reference = "other"
	if absent, e := p.CompletedSourceAbsentTx(ctx, tx, a, bad); e == nil || absent {
		t.Fatal("wrong reference accepted", e)
	}
}
