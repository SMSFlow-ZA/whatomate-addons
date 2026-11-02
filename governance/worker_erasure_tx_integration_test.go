//go:build integration

package governance_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"testing"
)

func TestWorkerCompletionSuppliedTransactionRollbackAndCurrentScope(t *testing.T) {
	physical, _, a, scope, req, record, entry := workerDeletion(t)
	ctx := t.Context()
	called := 0
	controller := func(context.Context, *sql.Tx, api.Actor, governance.Record) (governance.WorkerErasureReceipt, error) {
		called++
		return workerReceipt(a, record), nil
	}
	tx, e := physical.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	out, e := physical.CompleteWorkerErasureTx(ctx, tx, a, scope, req.ID, record.ID, entry.Version, controller)
	if e != nil || out.State != governance.RecordedApplied {
		t.Fatal(e)
	}
	tx.Rollback()
	var count int
	if e = physical.DB.QueryRow(`SELECT count(*) FROM addon_governance_document WHERE kind='worker_erasure_receipt' AND organization_id=$1`, a.OrganizationID).Scan(&count); e != nil || count != 0 {
		t.Fatal("caller rollback lost", count, e)
	}
	tx, e = physical.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	a.Grants = nil
	if _, e = physical.CompleteWorkerErasureTx(ctx, tx, a, scope, req.ID, record.ID, entry.Version, controller); !errors.Is(e, governance.ErrDenied) || called != 1 {
		t.Fatal("unscoped callback ran", e, called)
	}
}
