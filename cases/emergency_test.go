package cases

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestEmergencyReadRequiresExactScopeIndependentApprovalAndNoSubjectAlias(t *testing.T) {
	ctx := context.Background()
	r := NewMemoryRepository()
	c := fixture()
	c.Narrative = "protected emergency narrative"
	r.cases[c.ID] = clone(c)
	s := Service{Repository: r}
	triage := Principal{ID: "triage", Grants: []Grant{{OrganizationID: c.OrganizationID, UnitID: c.UnitID, Permission: ReadTriage}}}
	if _, e := s.Read(ctx, triage, c.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("normal read unexpectedly granted")
	}
	before, _ := json.Marshal(r.cases)
	calls := 0
	approve := func(_ context.Context, id string) error {
		calls++
		if id != c.ID {
			t.Fatal("approval not bound to exact case")
		}
		return nil
	}
	got, e := s.ReadEmergency(ctx, triage, c.ID, approve)
	if e != nil || got.Narrative != c.Narrative || calls != 1 {
		t.Fatalf("approved read: %v %+v", e, got)
	}
	after, _ := json.Marshal(r.cases)
	if string(before) != string(after) {
		t.Fatal("emergency read changed case/assignments")
	}
	for _, test := range []struct {
		name      string
		p         Principal
		id        string
		authorize func(context.Context, string) error
	}{
		{"missing approval", triage, c.ID, nil}, {"approval denied", triage, c.ID, func(context.Context, string) error { return errors.New("expired approval") }},
		{"unknown case", triage, "missing", approve}, {"wrong org", Principal{ID: "triage", Grants: []Grant{{OrganizationID: "other", UnitID: c.UnitID, Permission: ReadTriage}}}, c.ID, approve},
		{"wrong unit", Principal{ID: "triage", Grants: []Grant{{OrganizationID: c.OrganizationID, UnitID: "other", Permission: ReadTriage}}}, c.ID, approve},
		{"wrong permission", Principal{ID: "worker", Grants: []Grant{{OrganizationID: c.OrganizationID, UnitID: c.UnitID, Permission: WorkAssigned}}}, c.ID, approve},
		{"subject identity", Principal{ID: "subject", Grants: triage.Grants}, c.ID, approve}, {"subject alias", Principal{ID: "triage", Aliases: []string{"not-subject", "subject"}, Grants: triage.Grants}, c.ID, approve},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, e := s.ReadEmergency(ctx, test.p, test.id, test.authorize); !errors.Is(e, ErrDenied) {
				t.Fatalf("expected denial, got %v", e)
			}
		})
	}
	after, _ = json.Marshal(r.cases)
	if string(before) != string(after) {
		t.Fatal("denied emergency read changed case")
	}
}

type changingEmergencyRepository struct {
	Repository
	next Case
}

func (r changingEmergencyRepository) Begin(ctx context.Context) (Transaction, error) {
	tx, e := r.Repository.Begin(ctx)
	return &changingEmergencyTransaction{Transaction: tx, next: r.next}, e
}

type changingEmergencyTransaction struct {
	Transaction
	next  Case
	loads int
}

func (t *changingEmergencyTransaction) Load(ctx context.Context, id string) (Case, error) {
	t.loads++
	if t.loads > 1 {
		return clone(t.next), nil
	}
	return t.Transaction.Load(ctx, id)
}
func TestEmergencyReadRechecksAfterApprovalWithoutMutation(t *testing.T) {
	r := NewMemoryRepository()
	c := fixture()
	r.cases[c.ID] = clone(c)
	p := Principal{ID: "triage", Aliases: []string{"linked-employee"}, Grants: []Grant{{OrganizationID: c.OrganizationID, UnitID: c.UnitID, Permission: ReadTriage}}}
	next := clone(c)
	next.Participants = append(next.Participants, Participant{ID: "linked-employee", Role: Subject})
	next.Version++
	s := Service{Repository: changingEmergencyRepository{Repository: r, next: next}}
	called := false
	if _, e := s.ReadEmergency(context.Background(), p, c.ID, func(context.Context, string) error { called = true; return nil }); !errors.Is(e, ErrDenied) || !called {
		t.Fatalf("post-approval change accepted: %v", e)
	}
}
