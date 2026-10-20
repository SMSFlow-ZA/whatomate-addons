package carrier

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"testing"
)

func TestCanonicalSignedNumberRejectsGuesses(t *testing.T) {
	for _, v := range []string{"0123456789", "+0123456789", "2782 1234567", "", "+27821234567x"} {
		if _, e := CanonicalNumber(v); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, v := range []string{"27821234567", "+27821234567"} {
		if n, e := CanonicalNumber(v); e != nil || n != "+27821234567" {
			t.Fatal(n, e)
		}
	}
}
func TestDisabledBindingAndInvalidBatchFailClosed(t *testing.T) {
	s := Service{}
	if _, ok := s.Binding("phone"); ok {
		t.Fatal("default binding")
	}
	if e := s.AcceptBatch(context.Background(), []conversation.Inbound{{Tenant: "wrong"}}, nil); e == nil {
		t.Fatal("accepted unbound ingress")
	}
}

func TestEmptyServiceWorkerLanesDenyWithoutPanic(t *testing.T) {
	s := Service{}
	for _, lane := range []func(context.Context) error{s.Tick, s.TickIngress, s.TickDispatch, s.TickReceipts, s.ReconcilePending} {
		if lane(context.Background()) == nil {
			t.Fatal("empty service worker accepted")
		}
	}
}
