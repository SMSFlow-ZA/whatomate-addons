package api

import (
	"context"
	"testing"
)

func TestAccessTransactionPortsDenyMissingTransaction(t *testing.T) {
	s := AccessStore{}
	if _, e := s.LoadTx(context.Background(), nil, Actor{OrganizationID: "org", UserID: "user"}); e == nil {
		t.Fatal("scope reader accepted missing transaction")
	}
	if _, e := s.ReplaceVersionedTx(context.Background(), nil, AccessReplacement{}, true, nil); e == nil {
		t.Fatal("nil transaction authorized")
	}
	if e := s.LinkIdentityTx(context.Background(), nil, "org", "user", "employee", true); e == nil {
		t.Fatal("nil identity transaction authorized")
	}
}
