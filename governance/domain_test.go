package governance

import (
	"errors"
	"testing"
	"time"
)

func scope() Scope { return Scope{OrganizationID: "org", UnitID: "unit"} }
func record(id string, source Source) Record {
	return Record{ID: id, Scope: scope(), SubjectID: "person", LineageID: "root", Source: source, Reference: "ref:" + id, CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Generation: 1}
}
func TestPermissionsDefaultDenyExactScopeAndKind(t *testing.T) {
	p := Principal{ID: "admin"}
	if Can(p, scope(), DeleteRequest) {
		t.Fatal("implicit administrator bypass")
	}
	p.Grants = []Grant{{Scope: scope(), Permission: ExportRequest}}
	if !Can(p, scope(), ExportRequest) || Can(p, scope(), DeleteRequest) {
		t.Fatal("permission kinds conflated")
	}
	if Can(p, Scope{OrganizationID: "other", UnitID: "unit"}, ExportRequest) {
		t.Fatal("cross-organization bypass")
	}
	if _, e := PermissionFor(RequestKind("unknown")); !errors.Is(e, ErrInvalid) {
		t.Fatal("unknown request allowed")
	}
}
func TestPlanRequiresApprovalAndHoldsBlockDerivedCopies(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	req := SubjectRequest{ID: "request", Scope: scope(), SubjectID: "person", Kind: Delete}
	records := []Record{record("root", Case), record("vector", Vector)}
	rules := []Rule{{Source: Case, Approved: true}, {Source: Vector, Approved: true}}
	if _, e := Plan(req, records, rules, nil, now); !errors.Is(e, ErrApproval) {
		t.Fatal("unapproved deletion planned")
	}
	req.Approved = true
	holds := []Hold{{ID: "hold", Scope: scope(), LineageID: "root", Active: true}}
	list, e := Plan(req, records, rules, holds, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(list) != 2 || list[0].State != BlockedHold || list[1].State != BlockedHold {
		t.Fatalf("derived copy escaped hold %+v", list)
	}
}
func TestPlanDefaultsToRetentionPolicyAndPendingExternalExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	req := SubjectRequest{ID: "r", Scope: scope(), SubjectID: "person", Kind: Delete, Approved: true}
	records := []Record{record("core", Core), record("audit", Audit), record("backup", Backup), record("provider", Provider)}
	records[1].RetainUntil = now.Add(time.Hour)
	rules := []Rule{{Source: Audit, Approved: true}, {Source: Backup, Approved: true}, {Source: Provider, Approved: true}}
	got, e := Plan(req, records, rules, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	wants := []JournalState{BlockedPolicy, BlockedRetention, AwaitingExpiry, AwaitingExpiry}
	for i, want := range wants {
		if got[i].State != want {
			t.Fatalf("%s state %s want %s", records[i].Source, got[i].State, want)
		}
	}
}
func TestReferenceOnlyValidationRejectsNarrativeAndUnknownSources(t *testing.T) {
	r := record("root", Knowledge)
	r.Reference = "employee narrative contains spaces"
	if e := ValidateRecord(r); !errors.Is(e, ErrInvalid) {
		t.Fatal("narrative accepted", e)
	}
	r.Reference = "doc:synthetic-123"
	r.Source = "arbitrary"
	if e := ValidateRecord(r); !errors.Is(e, ErrInvalid) {
		t.Fatal("unknown source accepted", e)
	}
	r.Source = Knowledge
	if e := ValidateRecord(r); e != nil {
		t.Fatal(e)
	}
}

func TestJournalIdentityDoesNotCollideAcrossStructuredKeys(t *testing.T) {
	now := time.Now()
	req := SubjectRequest{ID: "a:b", Scope: scope(), SubjectID: "person", Kind: Delete, Approved: true}
	r := record("c", Core)
	r.LineageID = r.ID
	a, e := Plan(req, []Record{r}, []Rule{{Source: Core, Approved: true}}, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	req.ID = "a"
	r.ID = "b:c"
	r.LineageID = r.ID
	b, e := Plan(req, []Record{r}, []Rule{{Source: Core, Approved: true}}, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	if a[0].ID == b[0].ID {
		t.Fatal("journal key concatenation collided")
	}
}
