//go:build integration

package governance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

func workerDeletion(t *testing.T) (governance.PhysicalService, governance.Service, api.Actor, governance.Scope, governance.SubjectRequest, governance.Record, governance.JournalEntry) {
	t.Helper()
	db, a, scope, svc := physicalFixture(t)
	ctx := context.Background()
	p := governance.PrincipalForActor(a)
	root := governance.Record{ID: "knowledge:job", LineageID: "knowledge:job", Scope: scope, SubjectID: "synthetic-document", Source: governance.Knowledge, Reference: "immutable-job", CreatedAt: time.Now().Add(-time.Hour), Generation: 1}
	worker := root
	worker.ID += "worker-files"
	worker.Source = governance.WorkerFile
	for _, r := range []governance.Record{root, worker} {
		if e := svc.Register(ctx, p, r); e != nil {
			t.Fatal(e)
		}
	}
	for _, source := range []governance.Source{governance.Knowledge, governance.WorkerFile} {
		if e := svc.SetRule(ctx, p, scope, governance.Rule{Source: source, Approved: true}); e != nil {
			t.Fatal(e)
		}
	}
	req, e := svc.Request(ctx, p, governance.SubjectRequest{ID: "worker-delete", Scope: scope, SubjectID: root.SubjectID, Kind: governance.Delete, Reference: "synthetic:verified-authority"})
	if e != nil {
		t.Fatal(e)
	}
	req, e = svc.ApproveRequest(ctx, p, scope, req.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := svc.PlanDeletion(ctx, p, scope, req.ID)
	if e != nil {
		t.Fatal(e)
	}
	var entry governance.JournalEntry
	for _, v := range entries {
		if v.RecordID == worker.ID {
			entry = v
		}
	}
	return governance.PhysicalService{DB: db}, svc, a, scope, req, worker, entry
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func workerReceipt(a api.Actor, r governance.Record) governance.WorkerErasureReceipt {
	return governance.WorkerErasureReceipt{JobID: r.Reference, TenantID: a.OrganizationID, ActorID: "original-uploader", InputHash: hash("synthetic-input"), ManifestHash: hash("synthetic-manifest"), ReceiptHash: hash("synthetic-erasure-receipt"), DeletedAt: time.Now().UTC()}
}

func TestWorkerCompletionRejectsTamperAndUnverifiedAbsence(t *testing.T) {
	physical, svc, a, scope, req, record, entry := workerDeletion(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "retained-original.pdf")
	if e := os.WriteFile(path, []byte("SYNTHETIC"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, tamper := range []func(*governance.WorkerErasureReceipt){func(r *governance.WorkerErasureReceipt) { r.TenantID = "other" }, func(r *governance.WorkerErasureReceipt) { r.JobID = "other" }, func(r *governance.WorkerErasureReceipt) { r.InputHash = "invalid" }, func(r *governance.WorkerErasureReceipt) { r.ManifestHash = "invalid" }, func(r *governance.WorkerErasureReceipt) { r.ReceiptHash = "invalid" }, func(r *governance.WorkerErasureReceipt) { r.DeletedAt = time.Now().Add(time.Hour) }} {
		_, e := physical.CompleteWorkerErasure(ctx, a, scope, req.ID, record.ID, entry.Version, func(context.Context, api.Actor, governance.Record) (governance.WorkerErasureReceipt, error) {
			receipt := workerReceipt(a, record)
			tamper(&receipt)
			return receipt, nil
		})
		if !errors.Is(e, governance.ErrInvalid) {
			t.Fatal("tampered receipt accepted", e)
		}
	}
	_, e := physical.CompleteWorkerErasure(ctx, a, scope, req.ID, record.ID, entry.Version, func(context.Context, api.Actor, governance.Record) (governance.WorkerErasureReceipt, error) {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			return governance.WorkerErasureReceipt{}, governance.ErrInvalid
		}
		return workerReceipt(a, record), nil
	})
	if !errors.Is(e, governance.ErrInvalid) {
		t.Fatal("existing bytes marked removed", e)
	}
	journal, e := svc.Journal(ctx, governance.PrincipalForActor(a), scope, req.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range journal {
		if v.RecordID == record.ID && v.State != governance.AwaitingExpiry {
			t.Fatal("false completion journal", v)
		}
	}
}

func TestWorkerRemovalHoldLockSerializesActualFileErasure(t *testing.T) {
	physical, svc, a, scope, req, record, entry := workerDeletion(t)
	ctx := context.Background()
	p := governance.PrincipalForActor(a)
	hold := governance.Hold{ID: "legal", Scope: scope, LineageID: record.LineageID, Reference: "synthetic:hold", Active: true}
	if e := svc.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	var invoked atomic.Bool
	controller := func(context.Context, api.Actor, governance.Record) (governance.WorkerErasureReceipt, error) {
		invoked.Store(true)
		return workerReceipt(a, record), nil
	}
	if _, e := physical.CompleteWorkerErasure(ctx, a, scope, req.ID, record.ID, entry.Version, controller); !errors.Is(e, governance.ErrHold) || invoked.Load() {
		t.Fatal("hold did not prevent side effect", e)
	}
	hold.Active = false
	if e := svc.SetHold(ctx, p, hold); e != nil {
		t.Fatal(e)
	}
	job := filepath.Join(t.TempDir(), "immutable-job")
	if e := os.Mkdir(job, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(job, "input.pdf")
	if e := os.WriteFile(file, []byte("SYNTHETIC"), 0600); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	deletionDone := make(chan error, 1)
	go func() {
		_, e := physical.CompleteWorkerErasure(ctx, a, scope, req.ID, record.ID, entry.Version, func(context.Context, api.Actor, governance.Record) (governance.WorkerErasureReceipt, error) {
			close(entered)
			<-release
			if e := os.Remove(file); e != nil {
				return governance.WorkerErasureReceipt{}, e
			}
			if e := os.Remove(job); e != nil {
				return governance.WorkerErasureReceipt{}, e
			}
			if _, e := os.Stat(job); !os.IsNotExist(e) {
				return governance.WorkerErasureReceipt{}, governance.ErrInvalid
			}
			return workerReceipt(a, record), nil
		})
		deletionDone <- e
	}()
	<-entered
	hold.Active = true
	holdDone := make(chan error, 1)
	go func() { holdDone <- svc.SetHold(ctx, p, hold) }()
	select {
	case e := <-holdDone:
		close(release)
		t.Fatal("hold bypassed in-progress governed lock", e)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if e := <-deletionDone; e != nil {
		t.Fatal(e)
	}
	if e := <-holdDone; e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(job); !os.IsNotExist(e) {
		t.Fatal("job still exists", e)
	}
	journal, e := svc.Journal(ctx, p, scope, req.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range journal {
		if v.RecordID == record.ID && v.State != governance.RecordedApplied {
			t.Fatal("verified absence unrecorded", v)
		}
	}
}

func TestSubjectRestoreRejectsMismatchedRequestBeforeAnyGenerationChange(t *testing.T) {
	physical, _, a, scope, req, record, _ := workerDeletion(t)
	ctx := context.Background()
	before, e := physical.Inventory(ctx, a, scope)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = physical.ReconcileSubjectRestore(ctx, a, scope, "different-person", req.ID, "offline-op-1"); !errors.Is(e, governance.ErrDenied) {
		t.Fatal("cross-subject restore accepted", e)
	}
	after, e := physical.Inventory(ctx, a, scope)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range after {
		if r.ID == record.ID && r.Generation != before[0].Generation {
			t.Fatal("denied restore changed generation", r)
		}
	}
}
