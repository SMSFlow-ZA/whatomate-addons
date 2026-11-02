package cases

import (
	"context"
	"errors"
	"testing"
)

type cancellationUnknownRepository struct {
	Repository
	once bool
}
type cancellationUnknownTx struct {
	Transaction
	owner *cancellationUnknownRepository
}

func (r *cancellationUnknownRepository) Begin(ctx context.Context) (Transaction, error) {
	tx, e := r.Repository.Begin(ctx)
	return &cancellationUnknownTx{Transaction: tx, owner: r}, e
}
func (tx *cancellationUnknownTx) Commit(ctx context.Context) (CommitOutcome, error) {
	out, e := tx.Transaction.Commit(ctx)
	if out == Committed && !tx.owner.once {
		tx.owner.once = true
		return Unknown, errors.New("synthetic lost commit response")
	}
	return out, e
}
func TestCancelledDraftUnknownCommitReconcilesWithoutSecondEvent(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	s := Service{Repository: repo}
	r, e := s.Intake(ctx, IntakeRequest{OrganizationID: "org", UnitID: "unit", ContactID: "employee", Key: "draft", Draft: true})
	if e != nil {
		t.Fatal(e)
	}
	p := Principal{ID: "employee", Grants: []Grant{{OrganizationID: "org", UnitID: "unit", Permission: SubmitOwn}, {OrganizationID: "org", UnitID: "unit", Permission: ReadOwn}}}
	s.Repository = &cancellationUnknownRepository{Repository: repo}
	if _, e = s.CancelDraft(ctx, p, r.CaseID, 1, "cancel"); !errors.Is(e, ErrCommitUnknown) {
		t.Fatal("unknown commit concealed", e)
	}
	c, e := s.CancelDraft(ctx, p, r.CaseID, 1, "cancel")
	if e != nil || c.Version != 2 || len(c.Events) != 2 {
		t.Fatal("unknown replay duplicated event", e)
	}
}
