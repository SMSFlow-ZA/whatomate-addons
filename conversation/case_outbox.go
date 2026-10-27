package conversation

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
)

// CaseAcknowledgement resolves the destination from the current trusted roster.
// Root consumes ClaimOutbox, calls this immediately before its approved sender,
// then FinishOutbox. It must hold failures/unknowns, never create a second queue.
// account is host configuration, not operator request input.
func (s Service) CaseAcknowledgement(ctx context.Context, m cases.OutboxMessage, account string) (delivery.Message, error) {
	if account == "" || m.Kind != "case.intake.acknowledgement" || m.CaseID == "" || m.ContactID == "" {
		return delivery.Message{}, ErrDenied
	}
	store, e := directory.NewPostgresStore(s.DB, m.OrganizationID, "case-outbox")
	if e != nil {
		return delivery.Message{}, e
	}
	snap, e := store.Load(ctx)
	if e != nil {
		return delivery.Message{}, e
	}
	emp, ok := snap.Employees[m.ContactID]
	if !ok {
		return delivery.Message{}, ErrDenied
	}
	notice := delivery.Message{ID: m.ID, Tenant: m.OrganizationID, Account: account, Sender: emp.Phone, EmployeeID: emp.ID, CaseID: m.CaseID, Text: caseAcknowledgementReply(m.CaseID)}
	if e = s.AuthorizeDelivery(ctx, notice); e != nil {
		return delivery.Message{}, e
	}
	return notice, nil
}
