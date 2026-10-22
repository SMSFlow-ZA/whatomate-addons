package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductionEmbeddingTextBoundPreservesSixteenThousandBytes(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(`{"model":"pinned-embed","data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	provider, err := newProductionProvider(productionFixture(server.URL), EmbeddingConfig{Endpoint: server.URL + "/embed", Model: "pinned-embed", Dimensions: 2}, server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{strings.Repeat("x", 16001), strings.Repeat("é", 8001)} {
		if _, err = provider.Embed(context.Background(), []string{text}); err == nil {
			t.Fatal("expanded provider text budget")
		}
	}
	if calls != 0 {
		t.Fatal("oversize text reached network")
	}
	if _, err = provider.Embed(context.Background(), []string{strings.Repeat("x", 16000)}); err != nil || calls != 1 {
		t.Fatal("existing exact byte boundary changed")
	}
}
