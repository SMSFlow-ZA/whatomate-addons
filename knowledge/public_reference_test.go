package knowledge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPublicReferencePublicationRejectsWrongOriginalAndBindsProvenance(t *testing.T) {
	content := []byte("synthetic source")
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	d := Document{ID: "source", Version: "1", Content: content, ContentHash: hash, Audiences: []string{"staff"}, Effective: time.Now(), Pages: []Page{{Number: 1, Text: "Synthetic source", Method: "text", ExtractorRevision: "test"}}, PublicReference: referenceFixture()}
	ev := Evidence{ContentHash: hash, ScannerRevision: "test", MalwareClean: true, ExtractionComplete: true}
	if _, e := Publish(d, ev, Approval{Reviewer: "reviewer", ManifestHash: ManifestHash(d)}); e == nil {
		t.Fatal("mismatched original admitted")
	}
	d.PublicReference.OriginalSHA256 = hash
	approval := Approval{Reviewer: "reviewer", ManifestHash: ManifestHash(d)}
	if _, e := Publish(d, ev, approval); e != nil {
		t.Fatal(e)
	}
	d.PublicReference.ScopeNote = "Claim verified law"
	if _, e := Publish(d, ev, approval); e == nil {
		t.Fatal("post-review provenance change accepted")
	}
}

func referenceFixture() *PublicReference {
	return &PublicReference{Title: "Synthetic public instrument", SourceURL: "https://www.gov.za/example", PublishedOn: "2026-01-01", Jurisdiction: "South Africa", OriginalSHA256: strings.Repeat("a", 64), ScopeNote: "Dated reference; currentness and individual applicability require review."}
}
func TestPublicReferenceMetadataBoundary(t *testing.T) {
	m := UploadMetadata{DocumentID: "ref", Version: "1", Filename: "ref.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Now(), DocumentType: "public_legal_reference", Title: "Synthetic public instrument", PublicReference: referenceFixture()}
	if !validMetadata(m) {
		t.Fatal("valid reference rejected")
	}
	for _, kind := range []string{"policy", "form", "verified_law", ""} {
		bad := m
		bad.DocumentType = kind
		if validMetadata(bad) {
			t.Fatalf("provenance admitted as %q", kind)
		}
	}
	bad := m
	bad.PublicReference = nil
	if validMetadata(bad) {
		t.Fatal("missing provenance accepted")
	}
	for _, url := range []string{"javascript:alert(1)", "http://www.gov.za/x", "https://user:secret@www.gov.za/x", "https://127.0.0.1/x"} {
		ref := *m.PublicReference
		ref.SourceURL = url
		bad := m
		bad.PublicReference = &ref
		if validMetadata(bad) {
			t.Fatal("unsafe URL accepted")
		}
	}
}
func TestPublicReferenceManifestAndClone(t *testing.T) {
	d := Document{ID: "x"}
	before := ManifestHash(d)
	legacy := struct {
		ID, Version, ContentHash string
		Content                  []byte
		Audiences                []string
		Effective, WithdrawAt    time.Time
		Pages                    []Page
	}{ID: "x"}
	legacyBytes, _ := json.Marshal(legacy)
	if before != fmt.Sprintf("%x", sha256.Sum256(legacyBytes)) {
		t.Fatal("legacy canonical document hash changed")
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "PublicReference") {
		t.Fatal("legacy bytes changed")
	}
	d.PublicReference = referenceFixture()
	if ManifestHash(d) == before {
		t.Fatal("provenance not bound")
	}
	copy := ClonePublicReference(d.PublicReference)
	copy.ScopeNote = "forged"
	if copy.ScopeNote == d.PublicReference.ScopeNote {
		t.Fatal("shared mutable reference")
	}
	before = ManifestHash(d)
	d.PublicReference.SourceURL = "https://www.gov.za/changed"
	if ManifestHash(d) == before {
		t.Fatal("changed provenance not bound")
	}
}
