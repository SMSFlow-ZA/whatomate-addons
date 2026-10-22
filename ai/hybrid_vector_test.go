package ai

import (
	"context"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"testing"
	"time"
)

type vectorFixture struct {
	calls   int
	err     error
	sources []knowledge.Source
}

func (v *vectorFixture) RetrieveVector(context.Context, knowledge.Query, knowledge.Authorizer, []float64) ([]knowledge.Source, error) {
	v.calls++
	return v.sources, v.err
}

type queryEmbedder struct {
	calls int
	count int
}

func (e *queryEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	e.calls++
	e.count = len(texts)
	return [][]float64{{1, 0}}, nil
}
func TestHybridNativeVectorUsesQueryOnlyAndPropagatesFailures(t *testing.T) {
	e := &queryEmbedder{}
	v := &vectorFixture{sources: []knowledge.Source{{Reference: "approved"}}}
	h := HybridRetriever{Catalog: fakeRetriever{}, Vectors: v, Embeddings: e}
	q := knowledge.Query{Principal: "reader", Audience: "staff", Text: "synthetic query", At: time.Now().UTC()}
	allow := knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return true })
	got, err := h.Retrieve(context.Background(), q, allow)
	if err != nil || len(got) != 1 || e.count != 1 || v.calls != 1 {
		t.Fatal("native vector path must embed query only")
	}
	v.err = errors.New("synthetic database unavailable")
	if _, err = h.Retrieve(context.Background(), q, allow); err == nil {
		t.Fatal("vector failure silently fell back")
	}
	h.Catalog = fakeRetriever{sources: []knowledge.Source{{Reference: "lexical"}}}
	before := v.calls
	if _, err = h.Retrieve(context.Background(), knowledge.Query{}, nil); err != nil || v.calls != before {
		t.Fatal("FTS priority lost")
	}
}

func TestHybridNativeVectorDeniedQueryNeverReachesProvider(t *testing.T) {
	e := &queryEmbedder{}
	v := &vectorFixture{}
	h := HybridRetriever{Catalog: fakeRetriever{}, Vectors: v, Embeddings: e}
	q := knowledge.Query{Principal: "reader", Audience: "staff", Text: "synthetic private query", At: time.Now().UTC()}
	if _, err := h.Retrieve(context.Background(), q, knowledge.AuthorizeFunc(func(context.Context, string, string) bool { return false })); err != nil || e.calls != 0 || v.calls != 0 {
		t.Fatal("unauthorized query reached embedding provider")
	}
}
