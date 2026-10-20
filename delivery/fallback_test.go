//go:build integration

package delivery

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMinimalSMSFallbackOnlyAfterKnownFailureAndRechecksEligibility(t *testing.T) {
	dsn := os.Getenv("CONVERSATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONVERSATION_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	tenant := fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	enqueue := func(id string) {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = Enqueue(ctx, tx, Message{ID: tenant + id, Tenant: tenant, Sender: "+12025550101", EmployeeID: "e", CaseID: "case", Text: "CONFIDENTIAL-NARRATIVE"}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	eligible := true
	calls := []Message{}
	w := Worker{DB: db, Tenant: tenant, Clock: time.Now, LeaseDuration: time.Minute, SMSFallback: &SMSFallbackConfig{Account: "approved-test-sms"}, Authorize: func(context.Context, Message) error {
		if !eligible {
			return ErrInvalid
		}
		return nil
	}, Send: func(_ context.Context, m Message) (Result, error) {
		calls = append(calls, m)
		return VerifiedFailed, nil
	}}
	enqueue("known")
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	eligible = false
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if len(calls) != 1 {
		t.Fatal("revoked fallback sent", calls)
	}
	eligible = true
	enqueue("unknown")
	w.Send = func(_ context.Context, m Message) (Result, error) { calls = append(calls, m); return Unknown, nil }
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if more, e := w.Dispatch(ctx); e != nil || more {
		t.Fatal("unknown retried or fallback", more, e)
	}
	enqueue("known-two")
	w.Send = func(_ context.Context, m Message) (Result, error) {
		calls = append(calls, m)
		if m.Channel == "sms" {
			return VerifiedSent, nil
		}
		return VerifiedFailed, nil
	}
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	last := calls[len(calls)-1]
	if last.Channel != "sms" || last.Account != "approved-test-sms" || last.Text != MinimalSMSNotice {
		t.Fatal("nonminimal fallback", last)
	}
	if more, e := w.Dispatch(ctx); e != nil || more {
		t.Fatal("duplicate fallback", more, e)
	}
}
