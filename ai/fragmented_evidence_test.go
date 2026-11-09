package ai

import (
	"strings"
	"testing"
)

func TestNumberedPolicyFragmentsRemainContiguousEvidence(t *testing.T) {
	text := "Shared equipment may be borrowed under these conditions4.2.1\n\nfor no more than 2 (two) working days at a time; or\n\n4.2.2\n\nwith prior written agreement from the equipment coordinator."
	spans := passageSpans([]EvidencePassage{{Reference: "equipment", Text: text}})
	if len(spans) != 1 || spans[0].Text != text || spans[0].source != 0 || spans[0].Document != "equipment" {
		t.Fatal("fragmented numbered rule lost context")
	}
	unrelated := "Keep the supply cabinet closed.\n\nReport damaged equipment to the coordinator."
	if got := passageSpans([]EvidencePassage{{Reference: "equipment", Text: unrelated}}); len(got) != 2 {
		t.Fatal("complete independent sentences coalesced")
	}
	tooLong := "Short heading about equipment2.1\n\n" + strings.Repeat("fragment ", 150) + "\n\nFinal complete instruction must remain separately bounded."
	for _, span := range passageSpans([]EvidencePassage{{Reference: "equipment", Text: tooLong}}) {
		if len(span.Text) > 900 || !strings.Contains(tooLong, span.Text) {
			t.Fatal("fragment coalescing exceeded provenance/budget")
		}
	}
}
