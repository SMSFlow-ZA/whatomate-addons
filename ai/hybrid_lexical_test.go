package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
)

type lexicalFixture struct {
	sources []knowledge.Source
	err     error
	calls   int
}

func (f *lexicalFixture) Retrieve(context.Context, knowledge.Query, knowledge.Authorizer) ([]knowledge.Source, error) {
	f.calls++
	return f.sources, f.err
}
func (f *lexicalFixture) RetrieveCandidates(context.Context, knowledge.Query, knowledge.Authorizer) ([]knowledge.Source, error) {
	f.calls++
	return f.sources, f.err
}

type refusedEmbedding struct{ calls int }

func (e *refusedEmbedding) Embed(context.Context, []string) ([][]float64, error) {
	e.calls++
	return nil, ErrProvider
}
func lexicalQuery() knowledge.Query {
	return knowledge.Query{Principal: "reader", Audience: "staff", Text: "What steps should a manager follow for repeated lateness?", At: time.Now().UTC()}
}

var lexicalAllow = knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return true })

func TestRankedLexicalFallbackPreservesBoundedGovernedRankWithoutEmbeddings(t *testing.T) {
	primary, candidates := &lexicalFixture{}, &lexicalFixture{}
	for i := 0; i < 20; i++ {
		candidates.sources = append(candidates.sources, knowledge.Source{Reference: fmt.Sprintf("approved:%d", i), DocumentID: "synthetic-policy", Version: "2", Page: knowledge.Page{Number: i + 1, Text: "Synthetic approved SOP: consequential disciplinary decisions require human review."}})
	}
	embed := &refusedEmbedding{}
	vectors := &vectorFixture{}
	h := HybridRetriever{Catalog: primary, Candidates: candidates, Embeddings: embed, Vectors: vectors, Fallback: FallbackRankedLexical}
	got, err := h.Retrieve(context.Background(), lexicalQuery(), lexicalAllow)
	if err != nil || len(got) != 8 || embed.calls != 0 || vectors.calls != 0 || candidates.calls != 1 {
		t.Fatalf("ranked lexical fallback: rows=%d embedding=%d vector=%d err=%v", len(got), embed.calls, vectors.calls, err)
	}
	for i, source := range got {
		if source != candidates.sources[i] {
			t.Fatal("rank/provenance changed")
		}
	}
}

func TestRankedLexicalFallbackDoesNotMaskErrorsOrDeniedQueries(t *testing.T) {
	for _, where := range []string{"primary", "candidate"} {
		t.Run(where, func(t *testing.T) {
			failure := errors.New("synthetic repository denied or unavailable")
			primary, candidates := &lexicalFixture{}, &lexicalFixture{}
			if where == "primary" {
				primary.err = failure
			} else {
				candidates.err = failure
			}
			h := HybridRetriever{Catalog: primary, Candidates: candidates, Fallback: FallbackRankedLexical}
			_, err := h.Retrieve(context.Background(), lexicalQuery(), lexicalAllow)
			if !errors.Is(err, failure) || (where == "primary" && candidates.calls != 0) {
				t.Fatal("error became fallback")
			}
		})
	}
	for _, mutate := range []func(*knowledge.Query){func(q *knowledge.Query) { q.Principal = "" }, func(q *knowledge.Query) { q.Audience = "" }, func(q *knowledge.Query) { q.At = time.Time{} }, func(q *knowledge.Query) { q.Text = " " }} {
		q := lexicalQuery()
		mutate(&q)
		candidates := &lexicalFixture{}
		h := HybridRetriever{Catalog: &lexicalFixture{}, Candidates: candidates, Fallback: FallbackRankedLexical}
		got, err := h.Retrieve(context.Background(), q, lexicalAllow)
		if err != nil || len(got) != 0 || candidates.calls != 0 {
			t.Fatal("invalid query reached candidates")
		}
	}
	for _, auth := range []knowledge.Authorizer{nil, knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return false })} {
		candidates := &lexicalFixture{}
		h := HybridRetriever{Catalog: &lexicalFixture{}, Candidates: candidates, Fallback: FallbackRankedLexical}
		got, err := h.Retrieve(context.Background(), lexicalQuery(), auth)
		if err != nil || len(got) != 0 || candidates.calls != 0 {
			t.Fatal("denied query reached candidates")
		}
	}
}

func TestFallbackModeIsExplicitAndProviderFailureNeverSwitchesStrategy(t *testing.T) {
	if !FallbackMode("").Valid() || !FallbackEmbeddings.Valid() || !FallbackRankedLexical.Valid() || FallbackMode("all_pages").Valid() {
		t.Fatal("invalid fallback modes")
	}
	for _, mode := range []FallbackMode{"", FallbackEmbeddings} {
		candidates := &lexicalFixture{sources: []knowledge.Source{{Reference: "approved:1"}}}
		embed := &refusedEmbedding{}
		h := HybridRetriever{Catalog: &lexicalFixture{}, Candidates: candidates, Embeddings: embed, Fallback: mode}
		got, err := h.Retrieve(context.Background(), lexicalQuery(), lexicalAllow)
		if !errors.Is(err, ErrProvider) || len(got) != 0 || embed.calls != 1 {
			t.Fatal("provider failure silently became lexical success")
		}
	}
	primary := &lexicalFixture{sources: []knowledge.Source{{Reference: "approved:1"}}}
	h := HybridRetriever{Catalog: primary, Fallback: "unknown"}
	if _, err := h.Retrieve(context.Background(), lexicalQuery(), lexicalAllow); !errors.Is(err, ErrProvider) || primary.calls != 0 {
		t.Fatal("unknown mode accepted")
	}
}
