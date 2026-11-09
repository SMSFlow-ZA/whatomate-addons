package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func assertSelectorPrefix(t *testing.T, original, selected []evidenceSpan, raw []byte) {
	t.Helper()
	if len(raw) > maxSelectorInputBytes {
		t.Fatalf("wire exceeds budget: %d", len(raw))
	}
	for i, s := range selected {
		want := original[i]
		want.Index = i
		if !reflect.DeepEqual(s, want) {
			t.Fatal("rank, source mapping or exact evidence changed")
		}
	}
	var wire struct {
		Question string         `json:"question"`
		Evidence []evidenceSpan `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire.Evidence) != len(selected) {
		t.Fatal("invalid wire", err)
	}
}
func TestLongManualEvidenceFitsSerializedSelectorBudget(t *testing.T) {
	passages := []EvidencePassage{}
	for page := 1; page <= 12; page++ {
		paragraphs := []string{}
		for para := 1; para <= 6; para++ {
			paragraphs = append(paragraphs, fmt.Sprintf("Section %d.%d. ", page, para)+strings.Repeat("Synthetic procedural evidence wording ", 8)+"Contact the review team.")
		}
		passages = append(passages, EvidencePassage{Reference: strings.Repeat("a", 64) + fmt.Sprintf(":%d", page), Title: strings.Repeat("Synthetic context ", 6), Text: strings.Join(paragraphs, "\n\n")})
	}
	ranked := passageSpans(passages)
	// Represent a completed ranking; retain its order, not original page order.
	for i, j := 0, len(ranked)-1; i < j; i, j = i+1, j-1 {
		ranked[i], ranked[j] = ranked[j], ranked[i]
	}
	before := append([]evidenceSpan(nil), ranked...)
	selected, raw, err := boundedSelectorInput("What is the stated review procedure?", ranked)
	if err != nil || len(selected) == 0 || len(selected) >= len(ranked) {
		t.Fatal("fixture must require whole-span trimming", err, len(selected))
	}
	assertSelectorPrefix(t, ranked, selected, raw)
	if !reflect.DeepEqual(before, ranked) {
		t.Fatal("mutated caller's ranked evidence")
	}
}
func TestSelectorBudgetIncludesUnicodeEscapingAndProvenance(t *testing.T) {
	ranked := []evidenceSpan{}
	for i := 0; i < 20; i++ {
		ranked = append(ranked, evidenceSpan{Index: 99 - i, Document: "ref", Context: "Public legal reference; not company policy; " + strings.Repeat("界<&>\"", 180), Text: "Exact supporting sentence.", source: i})
	}
	selected, raw, err := boundedSelectorInput("A Unicode question: 界", ranked)
	if err != nil || len(selected) == 0 || len(selected) >= len(ranked) {
		t.Fatal("provenance overhead unbounded", err)
	}
	assertSelectorPrefix(t, ranked, selected, raw)
}
func TestSelectorBudgetEmptyOversizedAndExactBoundary(t *testing.T) {
	for _, spans := range [][]evidenceSpan{nil, {{Text: strings.Repeat("x", maxSelectorInputBytes)}}} {
		selected, raw, err := boundedSelectorInput("question", spans)
		if err != nil || len(selected) != 0 || len(raw) != 0 {
			t.Fatal("unrepresentable evidence was admitted")
		}
	}
	spans := []evidenceSpan{{Text: "Exact source sentence.", source: 7}}
	_, base, err := boundedSelectorInput("", spans)
	if err != nil {
		t.Fatal(err)
	}
	question := strings.Repeat("x", maxSelectorInputBytes-len(base))
	selected, raw, err := boundedSelectorInput(question, spans)
	if err != nil || len(selected) != 1 || len(raw) != maxSelectorInputBytes {
		t.Fatal("exact budget rejected")
	}
	selected, raw, err = boundedSelectorInput(question+"x", spans)
	if err != nil || len(selected) != 0 || len(raw) != 0 {
		t.Fatal("over budget accepted")
	}
	// A nil provider proves no transport/model is touched for empty evidence.
	var provider *DevelopmentProvider
	out, err := provider.selectEvidenceWithRepair(context.Background(), Request{Question: "q"}, []evidenceSpan{{Text: strings.Repeat("x", maxSelectorInputBytes)}}, 3, false)
	if err != nil || out.Grounded || out.Answer != "" {
		t.Fatal("oversized single span must hand off without model call")
	}
}
