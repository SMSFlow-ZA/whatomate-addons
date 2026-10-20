// Package processing joins the durable knowledge queue to the isolated document
// supervisor. It belongs in a separate trusted controller, never the web server.
package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
)

type Queue interface {
	Claim(context.Context, string, time.Duration) (knowledge.ProcessingJob, error)
	CompleteWithReceipt(context.Context, knowledge.ProcessingJob, []knowledge.Page, knowledge.Evidence, knowledge.ProcessingReceipt) error
	Fail(context.Context, knowledge.ProcessingJob, string, bool) error
}
type Processor interface {
	Process(context.Context, documents.Job) (documents.Output, documents.Receipt, error)
}
type Bridge struct {
	Queue                                       Queue
	Processor                                   Processor
	WorkerID, ProfileID, Image, DefinitionsHash string
	Lease                                       time.Duration
	Now                                         func() time.Time
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func mapped(j knowledge.ProcessingJob) documents.Job {
	return documents.Job{ID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, Input: j.Input, Metadata: documents.Metadata{ID: j.Metadata.DocumentID, Version: j.Metadata.Version, Audiences: j.Metadata.Audiences, Effective: j.Metadata.Effective, WithdrawAt: j.Metadata.WithdrawAt}}
}

// Verify binds both distinct canonical metadata representations and the exact
// worker artifact to the claim. A receipt supplied by an upload is never used.
func Verify(j knowledge.ProcessingJob, o documents.Output, r documents.Receipt, image, definitions string, now time.Time) error {
	dj := mapped(j)
	mb, _ := json.Marshal(dj.Metadata)
	km, _ := json.Marshal(j.Metadata)
	artifact := documents.JobArtifact{JobManifest: documents.JobManifest{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: hash(mb)}, Output: o}
	var artifactBytes bytes.Buffer
	if json.NewEncoder(&artifactBytes).Encode(artifact) != nil {
		return errors.New("invalid artifact")
	}
	ab := artifactBytes.Bytes()
	if j.LeaseToken == "" || j.InputHash != hash(j.Input) || j.MetadataHash != hash(km) || r.JobID != j.ID || r.TenantID != j.TenantID || r.ActorID != j.ActorID || r.InputHash != j.InputHash || r.MetadataHash != hash(mb) || r.OutputHash != hash(ab) || r.Image != image || r.DefinitionsHash != definitions || r.CompletedAt.IsZero() || r.CompletedAt.After(now.Add(time.Second)) || !r.SignaturesValidUntil.After(now) || !r.SignaturesValidUntil.After(r.CompletedAt) {
		return errors.New("processing receipt mismatch")
	}
	d := o.Document
	da, _ := json.Marshal(d.Audiences)
	ma, _ := json.Marshal(j.Metadata.Audiences)
	if !bytes.Equal(d.Content, j.Input) || d.ContentHash != j.InputHash || d.ID != j.Metadata.DocumentID || d.Version != j.Metadata.Version || !bytes.Equal(da, ma) || !d.Effective.Equal(j.Metadata.Effective) || !d.WithdrawAt.Equal(j.Metadata.WithdrawAt) || len(d.Pages) == 0 || len(d.Pages) != len(o.Provenance) {
		return errors.New("processing document mismatch")
	}
	if _, e := knowledge.Publish(d, o.Evidence, knowledge.Approval{Reviewer: "controller-validation", ManifestHash: knowledge.ManifestHash(d)}); e != nil {
		return e
	}
	for i, p := range d.Pages {
		v := o.Provenance[i]
		if v.Number != p.Number || v.SourceHash != j.InputHash || v.TextHash != hash([]byte(p.Text)) || v.Method != p.Method || v.ExtractorRevision != p.ExtractorRevision {
			return errors.New("processing provenance mismatch")
		}
	}
	return nil
}

// Once processes one durable claim. Failed or uncertain work never requeues.
// Completion failure leaves its lease to become unknown; blind retry is unsafe.
func (b Bridge) Once(ctx context.Context) error {
	if b.Queue == nil || b.Processor == nil || b.ProfileID == "" || b.Image == "" || b.DefinitionsHash == "" || b.Lease < 7*time.Minute || b.Lease > 15*time.Minute {
		return errors.New("processing controller not configured")
	}
	j, e := b.Queue.Claim(ctx, b.WorkerID, b.Lease)
	if e != nil {
		return e
	}
	o, r, e := b.Processor.Process(ctx, mapped(j))
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now()
	}
	if e == nil {
		e = Verify(j, o, r, b.Image, b.DefinitionsHash, now)
	}
	if e != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if f := b.Queue.Fail(cleanup, j, "processing_failed", ctx.Err() != nil); f != nil {
			return errors.Join(e, f)
		}
		return e
	}
	return b.Queue.CompleteWithReceipt(ctx, j, o.Document.Pages, o.Evidence, knowledge.ProcessingReceipt{JobID: j.ID, InputHash: j.InputHash, MetadataHash: j.MetadataHash, OutputHash: r.OutputHash, ProfileID: b.ProfileID, ImageDigest: r.Image, DefinitionsRevision: r.DefinitionsHash})
}
