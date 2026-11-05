//go:build integration

package governance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"testing"
	"time"
)

// Synthetic adapter tests governance boundaries only, not a real source owner.
type revisionBoundaryAdapter struct {
	record governance.Record
	erased int
}

func (a *revisionBoundaryAdapter) Name() string { return "revision-boundary" }
func (a *revisionBoundaryAdapter) Discover(context.Context, *sql.Tx, api.Actor, governance.Scope, string) ([]governance.Record, error) {
	return []governance.Record{a.record}, nil
}
func (a *revisionBoundaryAdapter) ReviewDiscoverTx(c context.Context, x *sql.Tx, p api.Actor, s governance.Scope, u string) ([]governance.Record, error) {
	return a.Discover(c, x, p, s, u)
}
func (a *revisionBoundaryAdapter) Export(context.Context, *sql.Tx, api.Actor, governance.Record) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (a *revisionBoundaryAdapter) Erase(context.Context, *sql.Tx, api.Actor, governance.Record) error {
	a.erased++
	return nil
}
func TestSourceRevisionActualSQLImmutableAndRecoveryBoundary(t *testing.T) {
	for _, initial := range []uint64{0, 7} {
		t.Run(fmt.Sprintf("initial_%d", initial), func(t *testing.T) {
			db, actor, scope, svc := physicalFixture(t)
			ctx := context.Background()
			p := governance.PrincipalForActor(actor)
			adapter := &revisionBoundaryAdapter{record: governance.Record{ID: "revision-boundary:object", Scope: scope, SubjectID: "synthetic-subject", LineageID: "revision-boundary:object", Source: governance.Core, Reference: "synthetic:object", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Generation: 1, SourceRevision: initial}}
			physical := governance.PhysicalService{DB: db, Adapters: []governance.PhysicalAdapter{adapter}}
			records, e := physical.Discover(ctx, actor, scope, adapter.record.SubjectID)
			if e != nil || len(records) != 1 {
				t.Fatal("initial discovery", e)
			}
			saved := func() governance.Record {
				var b []byte
				if e := db.QueryRow(`SELECT payload FROM addon_governance_document WHERE organization_id=$1 AND unit_id=$2 AND kind='inventory' AND document_key=$3`, scope.OrganizationID, scope.UnitID, adapter.record.ID).Scan(&b); e != nil {
					t.Fatal(e)
				}
				var r governance.Record
				if e := json.Unmarshal(b, &r); e != nil {
					t.Fatal(e)
				}
				return r
			}
			old := saved()
			fingerprint := governance.RecordFingerprint(old)
			req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "revision-delete", Scope: scope, SubjectID: adapter.record.SubjectID, Kind: governance.Delete, Reference: "synthetic:authority"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = svc.ApproveRequest(ctx, p, scope, req.ID, req.Version); e != nil {
				t.Fatal(e)
			}
			if e = svc.SetRule(ctx, p, scope, governance.Rule{Source: governance.Core, Approved: true}); e != nil {
				t.Fatal(e)
			}
			assertUnchanged := func() {
				r := saved()
				if r.SourceRevision != initial || r.Generation != 1 || governance.RecordFingerprint(r) != fingerprint {
					t.Fatalf("rebound authority %+v", r)
				}
			}
			adapter.record.SourceRevision = initial + 1
			t.Run("ordinary_rediscovery_rejects_rebind", func(t *testing.T) {
				if _, e := physical.Discover(ctx, actor, scope, adapter.record.SubjectID); !errors.Is(e, governance.ErrConflict) {
					t.Fatal(e)
				}
				assertUnchanged()
			})
			t.Run("coordinated_rediscovery_rejects_rebind", func(t *testing.T) {
				tx, e := db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback()
				if _, e = physical.DiscoverTx(ctx, tx, actor, scope, adapter.record.SubjectID); !errors.Is(e, governance.ErrConflict) {
					t.Fatal(e)
				}
				if e = tx.Rollback(); e != nil {
					t.Fatal(e)
				}
				assertUnchanged()
			})
			t.Run("review_rejects_rebind", func(t *testing.T) {
				tx, e := db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback()
				if _, e = physical.ReviewInventoryTx(ctx, tx, actor, scope, adapter.record.SubjectID, req.ID); !errors.Is(e, governance.ErrConflict) {
					t.Fatal(e)
				}
				if e = tx.Rollback(); e != nil {
					t.Fatal(e)
				}
				assertUnchanged()
			})
			t.Run("erase_rejects_rebind_before_adapter", func(t *testing.T) {
				if _, e := physical.EraseRequest(ctx, actor, scope, req.ID); !errors.Is(e, governance.ErrConflict) {
					t.Fatal(e)
				}
				if adapter.erased != 0 {
					t.Fatal("adapter erased changed source")
				}
				assertUnchanged()
			})
			t.Run("registration_rejects_rebind", func(t *testing.T) {
				if e := svc.Register(ctx, p, adapter.record); !errors.Is(e, governance.ErrConflict) {
					t.Fatal(e)
				}
				assertUnchanged()
			})
			adapter.record.SourceRevision = initial
			t.Run("same_binding_and_recovery", func(t *testing.T) {
				if _, e := physical.Discover(ctx, actor, scope, adapter.record.SubjectID); e != nil {
					t.Fatal(e)
				}
				if _, e := svc.ReconcileRestore(ctx, p, scope, adapter.record.ID, "synthetic-recovery-1"); e != nil {
					t.Fatal(e)
				}
				r := saved()
				if r.Generation != 2 || r.SourceRevision != initial {
					t.Fatalf("recovery rewrote source binding %+v", r)
				}
				if _, e := svc.ReconcileRestore(ctx, p, scope, adapter.record.ID, "synthetic-recovery-1"); e != nil {
					t.Fatal(e)
				}
				if saved().Generation != 2 {
					t.Fatal("replay advanced generation")
				}
				rows, e := physical.Discover(ctx, actor, scope, adapter.record.SubjectID)
				if e != nil || len(rows) != 1 || rows[0].Generation != 2 || rows[0].SourceRevision != initial {
					t.Fatal("rediscovery lost recovery generation", rows, e)
				}
				if _, e := physical.ReconcileSubjectRestore(ctx, actor, scope, adapter.record.SubjectID, req.ID, "synthetic-recovery-2"); e != nil {
					t.Fatal(e)
				}
				r = saved()
				if r.Generation != 3 || r.SourceRevision != initial || adapter.erased != 1 {
					t.Fatal("physical recovery binding", r, adapter.erased)
				}
			})
		})
	}
}
