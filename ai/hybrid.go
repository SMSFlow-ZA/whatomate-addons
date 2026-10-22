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

// HybridRetriever uses FTS first. With Vectors configured it embeds only the
// query and delegates native search/current authorization to that repository.
// Otherwise the legacy fallback reranks governed lexical candidates in memory.
type HybridRetriever struct {
	Catalog    Retriever
	Candidates CandidateRetriever
	Embeddings Embedder
	Vectors    VectorRetriever
}

func (h HybridRetriever) Retrieve(ctx context.Context, q knowledge.Query, a knowledge.Authorizer) ([]knowledge.Source, error) {
	if h.Catalog == nil {
		return nil, ErrProvider
	}
	sources, e := h.Catalog.Retrieve(ctx, q, a)
	if e != nil || len(sources) > 0 {
		return sources, e
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
