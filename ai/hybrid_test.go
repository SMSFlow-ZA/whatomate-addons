package ai

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"testing"
)

type fakeRetriever struct{ sources []knowledge.Source }

func (f fakeRetriever) Retrieve(context.Context, knowledge.Query, knowledge.Authorizer) ([]knowledge.Source, error) {
	return f.sources, nil
}
func (f fakeRetriever) RetrieveCandidates(context.Context, knowledge.Query, knowledge.Authorizer) ([]knowledge.Source, error) {
	return f.sources, nil
}

type fakeEmbedder struct{ calls int }

func (f *fakeEmbedder) Embed(context.Context, []string) ([][]float64, error) {
	f.calls++
	return [][]float64{{1, 0}, {0, 1}, {1, 0}}, nil
}
func TestHybridKeepsFTSAndOnlyRanksGovernedCandidates(t *testing.T) {
	e := &fakeEmbedder{}
	a := knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return true })
	src := []knowledge.Source{{Reference: "a"}, {Reference: "b"}}
	h := HybridRetriever{Catalog: fakeRetriever{src}, Candidates: fakeRetriever{src}, Embeddings: e}
	r, err := h.Retrieve(context.Background(), knowledge.Query{}, a)
	if err != nil || len(r) != 2 || e.calls != 0 {
		t.Fatal("FTS should remain primary")
	}
	h.Catalog = fakeRetriever{}
	r, err = h.Retrieve(context.Background(), knowledge.Query{}, a)
	if err != nil || r[0].Reference != "b" || e.calls != 1 {
		t.Fatal("governed semantic fallback failed")
	}
}
