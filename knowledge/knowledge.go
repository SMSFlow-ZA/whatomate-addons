// Package knowledge governs publication and authorized page-level retrieval.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrDenied = errors.New("knowledge operation denied")

// Page records the extraction method and revision for a page of the original
// content. OCR adapters must preserve original page numbering, including blanks.
type Page struct {
	Number                          int
	Text, Method, ExtractorRevision string
}
type Document struct {
	ID, Version, ContentHash string
	Content                  []byte
	Audiences                []string
	Effective, WithdrawAt    time.Time
	Pages                    []Page
}

// Evidence is supplied only by a trusted scanning/extraction adapter. It must
// describe this exact content hash; receiving this struct is not itself a scan.
type Evidence struct {
	ContentHash, ScannerRevision     string
	MalwareClean, ExtractionComplete bool
}

// Approval is created by a host-authorized human review adapter, never a model.
// ManifestHash binds all document metadata, audiences and extracted page text.
type Approval struct{ Reviewer, ManifestHash string }
type Publication struct {
	document     Document
	manifestHash string
}

func ManifestHash(d Document) string {
	b, _ := json.Marshal(d)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func Publish(d Document, e Evidence, a Approval) (Publication, error) {
	if d.ID == "" || d.Version == "" || len(d.Content) == 0 || d.Effective.IsZero() || len(d.Audiences) == 0 || len(d.Pages) == 0 || a.Reviewer == "" || a.ManifestHash != ManifestHash(d) || !e.MalwareClean || !e.ExtractionComplete || e.ScannerRevision == "" || e.ContentHash != d.ContentHash || d.ContentHash != fmt.Sprintf("%x", sha256.Sum256(d.Content)) || (!d.WithdrawAt.IsZero() && !d.WithdrawAt.After(d.Effective)) {
		return Publication{}, ErrDenied
	}
	audiences := map[string]bool{}
	for _, v := range d.Audiences {
		if v == "" || audiences[v] {
			return Publication{}, ErrDenied
		}
		audiences[v] = true
	}
	for i, p := range d.Pages {
		if p.Number != i+1 || p.ExtractorRevision == "" || (p.Method != "text" && p.Method != "ocr") {
			return Publication{}, ErrDenied
		}
	}
	d.Content = append([]byte(nil), d.Content...)
	d.Pages = append([]Page(nil), d.Pages...)
	d.Audiences = append([]string(nil), d.Audiences...)
	return Publication{document: d, manifestHash: a.ManifestHash}, nil
}

type Authorizer interface {
	Allowed(context.Context, string, string) bool
}
type AuthorizeFunc func(context.Context, string, string) bool

func (f AuthorizeFunc) Allowed(c context.Context, p, a string) bool { return f(c, p, a) }

type Query struct {
	Principal, Audience, Text string
	At                        time.Time
}
type Source struct {
	Reference, DocumentID, Version, ContentHash, ManifestHash string
	Page                                                      Page
}
type Catalog struct {
	mu           sync.RWMutex
	publications map[string]Publication
	withdrawn    map[string]bool
}

func NewCatalog() *Catalog {
	return &Catalog{publications: map[string]Publication{}, withdrawn: map[string]bool{}}
}
func key(id, version string) string { return fmt.Sprintf("%d:%s%s", len(id), id, version) }
func (c *Catalog) Add(p Publication) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.manifestHash == "" || p.manifestHash != ManifestHash(p.document) {
		return ErrDenied
	}
	k := key(p.document.ID, p.document.Version)
	if _, exists := c.publications[k]; exists {
		return ErrDenied
	}
	for _, v := range c.publications {
		if v.document.ID == p.document.ID && v.document.Effective.Equal(p.document.Effective) {
			return ErrDenied
		}
	}
	c.publications[k] = p
	return nil
}
func (c *Catalog) Withdraw(id, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.withdrawn[key(id, version)] = true
}

// Retrieve is intentionally bounded lexical matching, not vector search. The
// latest effective version supersedes older versions even when withdrawn.
func (c *Catalog) Retrieve(ctx context.Context, q Query, auth Authorizer) ([]Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if auth == nil || q.Principal == "" || q.Audience == "" || q.At.IsZero() || strings.TrimSpace(q.Text) == "" || len(q.Text) > 4096 || !auth.Allowed(ctx, q.Principal, q.Audience) {
		return nil, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	latest := map[string]Publication{}
	for _, p := range c.publications {
		d := p.document
		if d.Effective.After(q.At) {
			continue
		}
		old, ok := latest[d.ID]
		if !ok || d.Effective.After(old.document.Effective) {
			latest[d.ID] = p
		}
	}
	result := []Source{}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := latest[id]
		d := p.document
		if c.withdrawn[key(d.ID, d.Version)] || (!d.WithdrawAt.IsZero() && !q.At.Before(d.WithdrawAt)) {
			continue
		}
		allowed := false
		for _, a := range d.Audiences {
			if a == q.Audience {
				allowed = true
			}
		}
		if !allowed {
			continue
		}
		for _, page := range d.Pages {
			if strings.Contains(strings.ToLower(page.Text), strings.ToLower(strings.TrimSpace(q.Text))) {
				result = append(result, Source{Reference: fmt.Sprintf("%s:%d", p.manifestHash, page.Number), DocumentID: d.ID, Version: d.Version, ContentHash: d.ContentHash, ManifestHash: p.manifestHash, Page: page})
				if len(result) == 20 {
					return result, nil
				}
			}
		}
	}
	return result, nil
}
