//go:build integration

package delivery

import (
	"context"
	"database/sql"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

func TestPostgresLeaseUnknownAndCurrentAuthorization(t *testing.T) {
	url := os.Getenv("CONVERSATION_DATABASE_URL")
	if url == "" {
		t.Skip("CONVERSATION_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	tenant := fmt.Sprintf("delivery-%d", time.Now().UnixNano())
	enqueue := func(id string) {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = Enqueue(ctx, tx, Message{ID: tenant + id, Tenant: tenant, Sender: "+27820000001", EmployeeID: "employee", Text: "minimal notice"}); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	enqueue("revoked")
	now := time.Now().UTC()
	sent := 0
	w := Worker{DB: db, Tenant: tenant, Clock: func() time.Time { return now }, LeaseDuration: time.Minute, Authorize: func(context.Context, Message) error { return ErrInvalid }, Send: func(context.Context, Message) (Result, error) { sent++; return VerifiedSent, nil }}
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if sent != 0 {
		t.Fatal("unauthorized send")
	}
	enqueue("unknown")
	w.Authorize = func(context.Context, Message) error { return nil }
	w.Send = func(context.Context, Message) (Result, error) { sent++; return Unknown, nil }
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if sent != 1 {
		t.Fatal(sent)
	}
	enqueue("crashed")
	if _, e = db.Exec(`UPDATE addon_conversation_delivery SET state='sending',lease='dead',lease_until=$2 WHERE id=$1`, tenant+"crashed", now.Add(-time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, e = w.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	if sent != 1 {
		t.Fatal("blind resend after crash")
	}
	rows, e := List(ctx, db, tenant, "+27820000001")
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range rows {
		if m.EmployeeID != "employee" || m.Tenant != tenant {
			t.Fatal("lost trusted identity")
		}
		if m.ID == tenant+"crashed" && m.State != "held_unknown" {
			t.Fatal(m.State)
		}
	}
}
