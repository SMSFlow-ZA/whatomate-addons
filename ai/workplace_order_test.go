package ai

import (
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"testing"
)

func TestWorkplaceFirstBatchRetainsLegalFallback(t *testing.T) {
	r := Request{Question: "How does equipment borrowing work?", Sources: []knowledge.Source{{Reference: "policy"}, {Reference: "law", PublicReference: &knowledge.PublicReference{Title: "Dated instrument"}}}}
	ranked := []evidenceSpan{{Document: "law", source: 1}}
	for i := 0; i < 15; i++ {
		ranked = append(ranked, evidenceSpan{Index: i + 1, Document: fmt.Sprint(i), source: 0})
	}
	got := workplaceFirstBatch(r, ranked)
	if len(got) != len(ranked) || got[0].Index != 1 || got[11].Index != 12 || got[12].Document != "law" || got[12].source != 1 || ranked[0].Document != "law" {
		t.Fatal("ordering expanded, mutated or lost legal fallback")
	}
	for _, q := range []string{"What does the law say?", "Explain the court judgment", "What date is in the Gazette notice?", "How does BCEA leave work?"} {
		r.Question = q
		got = workplaceFirstBatch(r, ranked)
		if got[0].Document != "law" {
			t.Fatal("explicit legal context overridden")
		}
	}
}
