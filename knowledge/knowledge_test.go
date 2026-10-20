package knowledge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

func fixture() Document {
	b := []byte("synthetic source")
	return Document{ID: "guide", Version: "1", Content: b, ContentHash: fmt.Sprintf("%x", sha256.Sum256(b)), Audiences: []string{"staff"}, Effective: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Pages: []Page{{Number: 1, Text: "Synthetic policy.", Method: "text", ExtractorRevision: "extract-v1"}}}
}
func verified(d Document) Evidence {
	return Evidence{ContentHash: d.ContentHash, MalwareClean: true, ScannerRevision: "scan-v1", ExtractionComplete: true}
}
func approval(d Document) Approval {
	return Approval{Reviewer: "reviewer", ManifestHash: ManifestHash(d)}
}
func TestPublicationRequiresVerifiedEvidenceAndBoundApproval(t *testing.T) {
	for _, change := range []func(*Document, *Evidence, *Approval){
		func(d *Document, e *Evidence, a *Approval) { e.MalwareClean = false },
		func(d *Document, e *Evidence, a *Approval) { e.ExtractionComplete = false },
		func(d *Document, e *Evidence, a *Approval) { e.ContentHash = "wrong" },
		func(d *Document, e *Evidence, a *Approval) { a.Reviewer = "" },
		func(d *Document, e *Evidence, a *Approval) { d.Audiences = []string{"public"} },
		func(d *Document, e *Evidence, a *Approval) {
			d.Pages[0].Method = "ocr"
			d.Pages[0].ExtractorRevision = ""
		},
	} {
		d := fixture()
		e := verified(d)
		a := approval(d)
		change(&d, &e, &a)
		if _, err := Publish(d, e, a); err == nil {
			t.Fatal("unsafe publication accepted")
		}
	}
}
func TestRetrievalRequiresCurrentAudienceAuthorizationAndWithdrawal(t *testing.T) {
	d := fixture()
	p, err := Publish(d, verified(d), approval(d))
	if err != nil {
		t.Fatal(err)
	}
	c := NewCatalog()
	if err = c.Add(p); err != nil {
		t.Fatal(err)
	}
	now := d.Effective.Add(time.Hour)
	for _, q := range []Query{{Principal: "u", Audience: "staff", Text: "policy", At: now}, {Principal: "u", Audience: "public", Text: "policy", At: now}, {Principal: "u", Audience: "staff", Text: "policy", At: d.Effective.Add(-time.Hour)}} {
		got, err := c.Retrieve(context.Background(), q, AuthorizeFunc(func(context.Context, string, string) bool { return false }))
		if err != nil || len(got) != 0 {
			t.Fatal("unauthorized source exposed")
		}
	}
	got, err := c.Retrieve(context.Background(), Query{Principal: "u", Audience: "staff", Text: "policy", At: now}, AuthorizeFunc(func(context.Context, string, string) bool { return true }))
	if err != nil || len(got) != 1 || got[0].Page.Number != 1 || got[0].ManifestHash == "" {
		t.Fatalf("missing approved provenance: %v %v", got, err)
	}
	c.Withdraw("guide", "1")
	got, _ = c.Retrieve(context.Background(), Query{Principal: "u", Audience: "staff", Text: "policy", At: now}, AuthorizeFunc(func(context.Context, string, string) bool { return true }))
	if len(got) != 0 {
		t.Fatal("withdrawn source exposed")
	}
}

func TestApprovalCannotBeMutatedAfterPublication(t *testing.T) {
	d := fixture()
	p, err := Publish(d, verified(d), approval(d))
	if err != nil {
		t.Fatal(err)
	}
	d.Pages[0].Text = "tampered"
	d.Audiences[0] = "public"
	d.Content[0] = 'X'
	c := NewCatalog()
	if err = c.Add(p); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Retrieve(context.Background(), Query{Principal: "u", Audience: "staff", Text: "policy", At: d.Effective.Add(time.Hour)}, AuthorizeFunc(func(context.Context, string, string) bool { return true }))
	if len(got) != 1 || got[0].Page.Text != "Synthetic policy." {
		t.Fatal("publication mutation leaked")
	}
}
func TestCurrentVersionAndAudienceAreEnforced(t *testing.T) {
	d := fixture()
	c := NewCatalog()
	p, _ := Publish(d, verified(d), approval(d))
	c.Add(p)
	auth := AuthorizeFunc(func(context.Context, string, string) bool { return true })
	q := Query{Principal: "u", Audience: "public", Text: "policy", At: d.Effective.Add(time.Hour)}
	got, _ := c.Retrieve(context.Background(), q, auth)
	if len(got) != 0 {
		t.Fatal("wrong audience leaked")
	}
	q.Audience = "staff"
	q.At = d.Effective.Add(-time.Second)
	got, _ = c.Retrieve(context.Background(), q, auth)
	if len(got) != 0 {
		t.Fatal("future publication leaked")
	}
	d.Version = "2"
	d.Effective = d.Effective.Add(time.Hour)
	d.WithdrawAt = d.Effective.Add(time.Hour)
	p, _ = Publish(d, verified(d), approval(d))
	if err := c.Add(p); err != nil {
		t.Fatal(err)
	}
	q.At = d.Effective.Add(time.Minute)
	got, _ = c.Retrieve(context.Background(), q, auth)
	if len(got) != 1 || got[0].Version != "2" {
		t.Fatal("superseded version retrieved")
	}
	q.At = d.WithdrawAt
	got, _ = c.Retrieve(context.Background(), q, auth)
	if len(got) != 0 {
		t.Fatal("scheduled withdrawal or supersession bypassed")
	}
}
