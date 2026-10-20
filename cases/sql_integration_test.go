//go:build integration

package cases

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	pgxstdlib "github.com/jackc/pgx/v5/stdlib"
	"os"
	"sync"
	"testing"
	"time"
)

func integrationRepository(t *testing.T) *SQLRepository {
	t.Helper()
	dsn := os.Getenv("CASES_DATABASE_URL")
	if dsn == "" {
		t.Skip("CASES_DATABASE_URL not supplied")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	r := &SQLRepository{DB: db}
	if e = r.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestSQLConcurrentIntakeReplayAndCaseCAS(t *testing.T) {
	r := integrationRepository(t)
	s := Service{Repository: r}
	org := "test-" + time.Now().Format("150405.000000000")
	req := IntakeRequest{OrganizationID: org, UnitID: "unit", ContactID: "contact", Key: "message"}
	var wg sync.WaitGroup
	results := make(chan IntakeResult, 16)
	for range 16 {
		wg.Go(func() {
			v, e := s.Intake(context.Background(), req)
			if e != nil {
				t.Error(e)
			}
			results <- v
		})
	}
	wg.Wait()
	close(results)
	id := ""
	for v := range results {
		if v.CaseID == "" || v.Outcome != Committed {
			t.Fatalf("bad commit %+v", v)
		}
		if id != "" && id != v.CaseID {
			t.Fatal("duplicate committed case")
		}
		id = v.CaseID
	}
	for _, table := range []string{"addon_cases_case", "addon_cases_intake", "addon_cases_outbox", "addon_cases_event"} {
		var n int
		if e := r.DB.QueryRow("SELECT count(*) FROM "+table+" WHERE organization_id=$1", org).Scan(&n); e != nil || n != 1 {
			t.Fatalf("%s count %d %v", table, n, e)
		}
	}
	other := Service{Repository: &SQLRepository{DB: r.DB}}
	v, e := other.Intake(context.Background(), req)
	if e != nil || v.CaseID != id {
		t.Fatal("recreated service replay failed", v, e)
	}
	p := Principal{ID: "triage", Grants: []Grant{{OrganizationID: org, UnitID: "unit", Permission: AssignTriage}}}
	candidate := Principal{ID: "handler", Grants: []Grant{{OrganizationID: org, UnitID: "unit", Permission: WorkAssigned}}}
	wins := make(chan bool, 16)
	for range 16 {
		wg.Go(func() {
			_, e := s.Assign(context.Background(), p, id, 1, []Candidate{{Principal: candidate, Role: Handler}}, time.Now())
			wins <- e == nil
		})
	}
	wg.Wait()
	close(wins)
	n := 0
	for win := range wins {
		if win {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("CAS winners %d", n)
	}
	c, e := s.Read(context.Background(), candidate, id)
	if e != nil || c.Version != 2 || len(c.Events) != 2 {
		t.Fatalf("stored audit %+v %v", c, e)
	}
	var audit int
	if e = r.DB.QueryRow("SELECT count(*) FROM addon_cases_event WHERE case_id=$1", id).Scan(&audit); e != nil || audit != 2 {
		t.Fatalf("audit inserts %d %v", audit, e)
	}
	req.ContactID = "different"
	if v, e := s.Intake(context.Background(), req); !errors.Is(e, ErrIdempotencyConflict) || v.CaseID != "" {
		t.Fatal("changed payload accepted", v, e)
	}
}
func TestSQLRollbackLeavesNoCaseOrOutbox(t *testing.T) {
	r := integrationRepository(t)
	org := "rollback-" + time.Now().Format("150405.000000000")
	tx, e := r.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	c := fixture()
	c.ID = org
	c.OrganizationID = org
	c.Events = []Event{{Version: 1, ActorID: "contact", To: Submitted, RecordedAt: time.Now(), OccurredAt: time.Now(), OrganizationID: org, UnitID: "unit"}}
	if _, e = tx.Intake(context.Background(), org, "key", "fingerprint", c, OutboxMessage{ID: org + "-ack", OrganizationID: org, CaseID: org, ContactID: "contact", Kind: "ack", CreatedAt: time.Now()}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"addon_cases_case", "addon_cases_intake", "addon_cases_outbox", "addon_cases_event"} {
		var n int
		if e = r.DB.QueryRow("SELECT count(*) FROM "+table+" WHERE organization_id=$1", org).Scan(&n); e != nil || n != 0 {
			t.Fatalf("partial rollback in %s: %d %v", table, n, e)
		}
	}
}

// This driver exercises a real server commit followed by a lost confirmation.
// It does not replace persistence: the inner pgx transaction really commits.
var lostDriverOnce sync.Once

type lostConfirmationDriver struct{ inner driver.Driver }

func (d lostConfirmationDriver) Open(name string) (driver.Conn, error) {
	c, e := d.inner.Open(name)
	if e != nil {
		return nil, e
	}
	return lostConfirmationConn{c}, nil
}

type lostConfirmationConn struct{ driver.Conn }

func (c lostConfirmationConn) Begin() (driver.Tx, error) {
	tx, e := c.Conn.Begin()
	if e != nil {
		return nil, e
	}
	return lostConfirmationTx{tx}, nil
}
func (c lostConfirmationConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, e := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if e != nil {
		return nil, e
	}
	return lostConfirmationTx{tx}, nil
}
func (c lostConfirmationConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c lostConfirmationConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

type lostConfirmationTx struct{ driver.Tx }

func (t lostConfirmationTx) Commit() error {
	if e := t.Tx.Commit(); e != nil {
		return e
	}
	return errors.New("synthetic lost commit confirmation after server commit")
}
func TestSQLUnknownCommitDoesNotLeakIDAndReplayRecovers(t *testing.T) {
	r := integrationRepository(t)
	lostDriverOnce.Do(func() {
		sql.Register("cases-lost-confirmation", lostConfirmationDriver{inner: pgxstdlib.GetDefaultDriver()})
	})
	db, e := sql.Open("cases-lost-confirmation", os.Getenv("CASES_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	req := IntakeRequest{OrganizationID: "uncertain-" + time.Now().Format("150405.000000000"), UnitID: "unit", ContactID: "contact", Key: "message"}
	v, e := (Service{Repository: &SQLRepository{DB: db}}).Intake(context.Background(), req)
	if !errors.Is(e, ErrCommitUnknown) || v.CaseID != "" || v.Outcome != Unknown {
		t.Fatalf("false committed ID %+v %v", v, e)
	}
	v, e = (Service{Repository: r}).Intake(context.Background(), req)
	if e != nil || v.CaseID == "" || v.Outcome != Committed {
		t.Fatalf("replay recovery %+v %v", v, e)
	}
	for _, table := range []string{"addon_cases_case", "addon_cases_intake", "addon_cases_outbox", "addon_cases_event"} {
		var n int
		if e = r.DB.QueryRow("SELECT count(*) FROM "+table+" WHERE organization_id=$1", req.OrganizationID).Scan(&n); e != nil || n != 1 {
			t.Fatalf("unknown replay duplicates in %s: %d %v", table, n, e)
		}
	}
}
