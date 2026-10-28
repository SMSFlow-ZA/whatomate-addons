package carrier

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"testing"
)

func TestPurposeNeverFallsThroughToEmployeeAuthorization(t *testing.T) {
	calls := 0
	s := Service{AuthorizeHandler: func(context.Context, delivery.Message) error { calls++; return nil }}
	if s.authorizePurpose(context.Background(), delivery.Message{Purpose: "unknown"}) == nil {
		t.Fatal("unknown purpose allowed")
	}
	if s.authorizePurpose(context.Background(), delivery.Message{Purpose: delivery.HandlerEscalation, Channel: "sms"}) == nil {
		t.Fatal("handler SMS implicitly allowed")
	}
	if calls != 0 {
		t.Fatal("unapproved route reached handler policy")
	}
	if e := s.authorizePurpose(context.Background(), delivery.Message{Purpose: delivery.HandlerEscalation}); e != nil || calls != 1 {
		t.Fatal("handler port not used", e, calls)
	}
	s.AuthorizeHandler = nil
	if s.authorizePurpose(context.Background(), delivery.Message{Purpose: delivery.HandlerEscalation}) == nil {
		t.Fatal("omitted handler policy allowed")
	}
}
