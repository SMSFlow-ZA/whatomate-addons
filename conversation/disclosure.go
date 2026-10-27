package conversation

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
)

// ProcessingDisclosure is trusted server configuration for the model-processing
// notice, never request input or evidence of employee consent.
type ProcessingDisclosure struct {
	Revision string
	Text     string
}

// processingDisclosureKey validates trusted configuration for both initial and
// explicitly requested notices. Request text never supplies notice contents.
func processingDisclosureKey(notice *ProcessingDisclosure) (string, error) {
	if notice == nil || strings.TrimSpace(notice.Revision) == "" || len(notice.Revision) > 128 || strings.TrimSpace(notice.Text) == "" || len(notice.Text) > 2000 || !utf8.ValidString(notice.Revision) || !utf8.ValidString(notice.Text) {
		return "", ErrDenied
	}
	return digest("model-processing-notice\x00" + notice.Revision + "\x00" + notice.Text), nil
}

// setProcessingDisclosure records a fresh notice in the inbound transaction.
// It neither retries nor changes any earlier delivery, including unknown ones.
func setProcessingDisclosure(in Inbound, turn *Turn, notice *ProcessingDisclosure, key string) {
	turn.ProcessingDisclosureKey = key
	turn.ProcessingDisclosureIntentID = digest(in.Tenant + "\x00" + in.Account + "\x00" + in.ProviderMessageID)
	turn.Reply = notice.Text + "\n\nPlease resend your SOP question after this notice is delivered. For human assistance, reply LOG CASE."
}

func (s Service) requestProcessingDisclosure(in Inbound, turn *Turn) error {
	notice := s.Config.ProcessingDisclosure
	key, err := processingDisclosureKey(notice)
	if err != nil {
		return err
	}
	setProcessingDisclosure(in, turn, notice, key)
	return nil
}

// advisoryDisclosureReady binds actual delivery of the exact current notice
// to the current employee/account and effective configured revision/text. A
// queued/accepted/sent/failed/unknown notice or SMS fallback cannot unlock AI.
func (s Service) advisoryDisclosureReady(ctx context.Context, in Inbound, turn *Turn) (bool, error) {
	notice := s.Config.ProcessingDisclosure
	if notice == nil {
		return true, nil
	}
	key, err := processingDisclosureKey(notice)
	if err != nil {
		return false, err
	}
	if turn.ProcessingDisclosureKey != key || turn.ProcessingDisclosureIntentID == "" {
		setProcessingDisclosure(in, turn, notice, key)
		return false, nil
	}
	status, err := delivery.ReadDeliveryStatus(ctx, s.DB, in.Tenant, turn.ProcessingDisclosureIntentID)
	if err != nil || status.IntentID != turn.ProcessingDisclosureIntentID || status.Account != in.Account || status.EmployeeID != turn.EmployeeID || status.Sender != in.Sender || status.ProviderID == "" || status.State != "delivered" {
		turn.Reply = "Delivery of the processing notice has not been confirmed. Please wait before resending your SOP question, or reply LOG CASE for human assistance."
		return false, nil
	}
	return true, nil
}
