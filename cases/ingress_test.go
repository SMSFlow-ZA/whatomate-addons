package cases

import (
	"context"
	"database/sql"
	"testing"
)

func TestIntakeIngressGuardRunsBeforeLocksOrWrites(t *testing.T) {
	called := false
	tx := &sqlTransaction{authorizeIntake: func(ctx context.Context, dbtx *sql.Tx, tenant, key string, c Case) error {
		called = true
		if tenant != "synthetic" || key != "original-key" || c.ContactID != "employee" {
			t.Fatal("guard identity mismatch")
		}
		return ErrDenied
	}}
	_, e := tx.Intake(context.Background(), "synthetic", "original-key", "fingerprint", Case{ContactID: "employee"}, OutboxMessage{})
	if e != ErrDenied || !called {
		t.Fatalf("ingress guard not enforced: called=%v err=%v", called, e)
	}
}
