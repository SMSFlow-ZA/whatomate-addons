package cases

// submissionAcknowledgement is created in the same transaction as the first
// confirmed submission. Historical already-sent acknowledgements are not erased.
func submissionAcknowledgement(c Case) OutboxMessage {
	event := c.Events[len(c.Events)-1]
	return OutboxMessage{ID: c.ID + "-ack", OrganizationID: c.OrganizationID, CaseID: c.ID, ContactID: c.ContactID, Kind: "case.intake.acknowledgement", CreatedAt: event.RecordedAt}
}
