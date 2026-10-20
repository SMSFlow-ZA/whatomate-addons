package processing

import (
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"testing"
	"time"
)

func TestReceiptBinding(t *testing.T) {
	now := time.Now().UTC()
	m := knowledge.UploadMetadata{DocumentID: "synthetic", Version: "1", Filename: "synthetic.pdf", MediaType: "application/pdf", Audiences: []string{"employee"}, Effective: now}
	mb, _ := json.Marshal(m)
	j := knowledge.ProcessingJob{ID: "job1", TenantID: "tenant", ActorID: "reviewer", LeaseToken: "lease", Input: []byte("%PDF-synthetic"), Metadata: m, MetadataHash: hash(mb)}
	j.InputHash = hash(j.Input)
	o := documents.Output{Document: knowledge.Document{ID: m.DocumentID, Version: m.Version, Content: j.Input, ContentHash: j.InputHash, Audiences: m.Audiences, Effective: now, Pages: []knowledge.Page{{Number: 1, Text: "Synthetic", Method: "text", ExtractorRevision: "poppler"}}}, Evidence: knowledge.Evidence{ContentHash: j.InputHash, ScannerRevision: "clam", MalwareClean: true, ExtractionComplete: true}, Provenance: []documents.PageProvenance{{Number: 1, Method: "text", SourceHash: j.InputHash, TextHash: hash([]byte("Synthetic")), ExtractorRevision: "poppler"}}}
	dm, _ := json.Marshal(mapped(j).Metadata)
	a := documents.JobArtifact{JobManifest: documents.JobManifest{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: hash(dm)}, Output: o}
	ab, _ := json.Marshal(a)
	ab = append(ab, '\n') // the actual worker uses json.Encoder.Encode
	r := documents.Receipt{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: hash(dm), OutputHash: hash(ab), Image: "sha256:pin", DefinitionsHash: "definitions", CompletedAt: now, SignaturesValidUntil: now.Add(time.Hour)}
	if e := Verify(j, o, r, r.Image, r.DefinitionsHash, now); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*documents.Receipt){func(x *documents.Receipt) { x.ActorID = "other" }, func(x *documents.Receipt) { x.MetadataHash = j.MetadataHash }, func(x *documents.Receipt) { x.OutputHash = "substituted" }, func(x *documents.Receipt) { x.DefinitionsHash = "other" }, func(x *documents.Receipt) { x.SignaturesValidUntil = now }} {
		bad := r
		mutate(&bad)
		if Verify(j, o, bad, r.Image, r.DefinitionsHash, now) == nil {
			t.Fatal("accepted substituted receipt")
		}
	}
	o.Document.Pages[0].Text = "Changed"
	if Verify(j, o, r, r.Image, r.DefinitionsHash, now) == nil {
		t.Fatal("accepted changed output")
	}
}
