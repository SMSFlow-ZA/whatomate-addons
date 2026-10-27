package conversation

import (
	"strings"
	"unicode/utf8"
)

const humanAssistanceReply = "Thanks for explaining. I don't have enough approved guidance to answer this safely. If you would like a person to review your concern, reply LOG CASE. I'll ask for a brief description and your confirmation before submitting a case."

// Framing explains the next interaction, not policy. The host's validated quote
// and complete citation block remain byte-for-byte intact. Do not truncate a
// source or citation to make room for presentation text.
func supportedReply(answer Answer) (string, bool) {
	if answer.Handoff || len(answer.Citations) == 0 || strings.TrimSpace(answer.Text) == "" || !utf8.ValidString(answer.Text) {
		return "", false
	}
	reply := "Thanks for your question. Here is the guidance I can share from the approved source:\n\n" + answer.Text +
		"\n\nIf you would like help with your situation, reply LOG CASE. I'll ask you to confirm before submitting a case for human review."
	if len(reply) > 4096 {
		return "", false
	}
	return reply, true
}

// Both the intake result and the case-owned outbox use this one renderer.
// Submission confirms persistence, not human assignment or a response deadline.
func caseAcknowledgementReply(reference string) string {
	return "Thank you. Your case has been submitted for human review.\nReference: " + reference +
		"\n\nReply STATUS for a privacy-protected update. Keep this reference so you can choose the right case if you have more than one."
}
