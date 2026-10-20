package ai

import (
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"regexp"
	"strings"
)

const maxEvidenceBytes = 900

// Evidence is an immutable bounded source span, not model-authored text.
type evidenceSpan struct {
	Index    int    `json:"evidence_index"`
	Document string `json:"document"`
	Text     string `json:"text"`
	source   int
}

var paragraphBreak = regexp.MustCompile(`\r?\n[ \t]*\r?\n`)
var sentenceEnd = regexp.MustCompile(`[.!?](?:[ \r\n]+|$)`)

func evidenceSpans(sources []knowledge.Source) []evidenceSpan {
	spans := []evidenceSpan{}
	total := 0
	for source, s := range sources {
		for _, block := range paragraphBreak.Split(s.Page.Text, -1) {
			block = strings.TrimSpace(block)
			pieces := []string{block}
			if len(block) > maxEvidenceBytes {
				pieces = nil
				start := 0
				end := 0
				for _, loc := range sentenceEnd.FindAllStringIndex(block, -1) {
					if loc[1]-start > maxEvidenceBytes && end > start {
						pieces = append(pieces, strings.TrimSpace(block[start:end]))
						start = end
					}
					end = loc[1]
				}
				if start < len(block) {
					pieces = append(pieces, strings.TrimSpace(block[start:]))
				}
			}
			for _, text := range pieces {
				if len(text) == 0 || len(text) > maxEvidenceBytes || len(strings.Fields(text)) < 5 || !sentenceEnd.MatchString(text) {
					continue
				}
				if len(spans) >= 64 || total+len(text) > 24000 {
					return spans
				}
				spans = append(spans, evidenceSpan{Index: len(spans), Document: s.DocumentID, Text: text, source: source})
				total += len(text)
			}
		}
	}
	return spans
}

// These explicit authority escalation requests are unsupported by this API.
// This is a supplemental behavior guard, never an access-control boundary.
var authorityOverride = regexp.MustCompile(`(?i)\b(ignore|disregard|override)\b.{0,60}\b(rules|instructions|permissions|authorization)\b|\bact\s+as\s+(an?\s+)?(administrator|admin|root)\b|\b(execute|run)\s+(sql|shell|commands?|tools?)\b|\bfollow\s+(its|their|the\s+(attached|uploaded)\s+file.s)\s+instructions\b`)

func unsupportedAuthorityRequest(question string) bool {
	return authorityOverride.MatchString(question)
}
