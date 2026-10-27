package ai

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"math"
	"sort"
	"strings"
)

type CandidateRetriever interface {
	RetrieveCandidates(context.Context, knowledge.Query, knowledge.Authorizer) ([]knowledge.Source, error)
}
type Embedder interface {
	Embed(context.Context, []string) ([][]float64, error)
}

type VectorRetriever interface {
	RetrieveVector(context.Context, knowledge.Query, knowledge.Authorizer, []float64) ([]knowledge.Source, error)
}

// FallbackMode is trusted host configuration, never a request-selected strategy.
type FallbackMode string

const (
	FallbackEmbeddings    FallbackMode = "embeddings"
	FallbackRankedLexical FallbackMode = "ranked_lexical"
)

// The empty mode preserves existing embedding behavior. Ranked lexical recall
// must be explicitly configured; errors never switch a request between modes.
func (m FallbackMode) Valid() bool {
	return m == "" || m == FallbackEmbeddings || m == FallbackRankedLexical
}

// HybridRetriever uses FTS first. Explicit ranked lexical fallback retains the
// governed candidate order without embedding. Otherwise, configured Vectors
// receive only an embedded query; the legacy fallback embeds and reranks the
// governed lexical candidates in memory.
type HybridRetriever struct {
	Catalog    Retriever
	Candidates CandidateRetriever
	Embeddings Embedder
	Vectors    VectorRetriever
	Fallback   FallbackMode
}

func (h HybridRetriever) Retrieve(ctx context.Context, q knowledge.Query, a knowledge.Authorizer) ([]knowledge.Source, error) {
	if h.Catalog == nil || !h.Fallback.Valid() {
		return nil, ErrProvider
	}
	sources, e := h.Catalog.Retrieve(ctx, q, a)
	if e != nil || len(sources) > 0 {
		return sources, e
	}
	if h.Fallback == FallbackRankedLexical {
		if a == nil || q.Principal == "" || q.Audience == "" || q.At.IsZero() || strings.TrimSpace(q.Text) == "" || len(q.Text) > 4096 || !a.Allowed(ctx, q.Principal, q.Audience) {
			return nil, nil
		}
		if h.Candidates == nil {
			return nil, ErrProvider
		}
		// Candidates retain database relevance order and all publication/current
		// authorization checks. Do not embed approved policy text through the
		// query embedding contract or substitute unrelated pages on an error.
		sources, e = h.Candidates.RetrieveCandidates(ctx, q, a)
		if e != nil {
			return nil, e
		}
		if len(sources) > 8 {
			sources = sources[:8]
		}
		return sources, nil
	}
	if h.Vectors != nil {
		if a == nil || q.Principal == "" || q.Audience == "" || q.At.IsZero() || strings.TrimSpace(q.Text) == "" || len(q.Text) > 4096 || !a.Allowed(ctx, q.Principal, q.Audience) {
			return nil, nil
		}
		if h.Embeddings == nil {
			return nil, ErrProvider
		}
		vectors, err := h.Embeddings.Embed(ctx, []string{q.Text})
		if err != nil || len(vectors) != 1 {
			return nil, ErrProvider
		}
		return h.Vectors.RetrieveVector(ctx, q, a, vectors[0])
	}
	if h.Candidates == nil || h.Embeddings == nil {
		return sources, nil
	}
	sources, e = h.Candidates.RetrieveCandidates(ctx, q, a)
	if e != nil || len(sources) == 0 {
		return sources, e
	}
	if len(sources) > 19 {
		sources = sources[:19]
	}
	texts := []string{q.Text}
	for _, s := range sources {
		texts = append(texts, s.Page.Text)
	}
	vectors, e := h.Embeddings.Embed(ctx, texts)
	if e != nil || len(vectors) != len(texts) {
		return nil, ErrProvider
	}
	scores := map[string]float64{}
	for i, s := range sources {
		scores[s.Reference] = cosine(vectors[0], vectors[i+1])
	}
	sort.SliceStable(sources, func(i, j int) bool { return scores[sources[i].Reference] > scores[sources[j].Reference] })
	if len(sources) > 8 {
		sources = sources[:8]
	}
	return sources, nil
}
func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return -1
	}
	var dot, x, y float64
	for i, v := range a {
		dot += v * b[i]
		x += v * v
		y += b[i] * b[i]
	}
	if x == 0 || y == 0 {
		return -1
	}
	return dot / math.Sqrt(x*y)
}
