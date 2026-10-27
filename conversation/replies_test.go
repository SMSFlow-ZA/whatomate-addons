package conversation

import (
	"strings"
	"testing"
)

func TestSupportedReplyPreservesExactEvidenceAndNoAutomaticHandoff(t *testing.T) {
	quote := "Record the concern\nRecord the observed dates and times."
	answer := Answer{Text: quote + "\n\nSources:\n- DEMO-SOP | version 1 | page 1 | reference", Citations: []string{"reference"}}
	reply, ok := supportedReply(answer)
	if !ok || !strings.Contains(reply, answer.Text) || strings.Count(reply, answer.Text) != 1 {
		t.Fatal("exact validated quote and citation block must survive once")
	}
	if !strings.HasPrefix(reply, "Thanks for your question.") || !strings.Contains(reply, "reply LOG CASE") || !strings.Contains(reply, "confirm before submitting") {
		t.Fatal("guidance must have an acknowledgement and accurate optional human path")
	}
	if strings.Contains(reply, "has been escalated") || strings.Contains(reply, "disciplinary action") {
		t.Fatal("framing must not add a policy decision or claim an automatic escalation")
	}
}

func TestSupportedReplyFailsClosedWithoutClippingEvidence(t *testing.T) {
	for _, answer := range []Answer{
		{Text: "unverified text"},
		{Text: "", Citations: []string{"ref"}},
		{Text: "   ", Citations: []string{"ref"}},
		{Text: "withheld", Citations: []string{"ref"}, Handoff: true},
		{Text: string([]byte{0xff}), Citations: []string{"ref"}},
		{Text: strings.Repeat("x", 4096), Citations: []string{"ref"}},
	} {
		if reply, ok := supportedReply(answer); ok || reply != "" {
			t.Fatal("invalid/oversized evidence must hand off, never be clipped")
		}
	}
	// The channel bound applies to the complete UTF-8 message, not rune count.
	if _, ok := supportedReply(Answer{Text: strings.Repeat("界", 1365), Citations: []string{"ref"}}); ok {
		t.Fatal("multibyte quote exceeded complete reply byte limit")
	}
}

func TestAcknowledgementSharesExactReferenceWithoutPromisingResponseTime(t *testing.T) {
	reply := caseAcknowledgementReply("case-test")
	if !strings.Contains(reply, "Reference: case-test") || !strings.Contains(reply, "Reply STATUS") || !strings.HasPrefix(reply, "Thank you.") {
		t.Fatal("missing confirmation, exact reference or next step")
	}
	for _, promise := range []string{"within", "assigned", "resolved", "24 hours"} {
		if strings.Contains(strings.ToLower(reply), promise) {
			t.Fatal("acknowledgement makes an unproven promise")
		}
	}
}
