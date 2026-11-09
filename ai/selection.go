package ai

import (
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"regexp"
	"strings"
)

const maxEvidenceBytes = 900

const maxSelectorInputBytes = 30000

// boundedSelectorInput retains a prefix of already ranked evidence. The wire
// budget includes question, provenance, references, escaping and UTF-8 bytes.
// Drop whole lowest-ranked spans; never truncate evidence text or provenance.
func boundedSelectorInput(question string, ranked []evidenceSpan) ([]evidenceSpan, []byte, error) {
	spans := append([]evidenceSpan(nil), ranked...)
	for i := range spans {
		spans[i].Index = i
	}
	for len(spans) > 0 {
		input, err := json.Marshal(struct {
			Question string         `json:"question"`
			Evidence []evidenceSpan `json:"evidence"`
		}{question, spans})
		if err != nil {
			return nil, nil, err
		}
		if len(input) <= maxSelectorInputBytes {
			return spans, input, nil
		}
		spans = spans[:len(spans)-1]
	}
	return nil, nil, nil
}

// Evidence is an immutable bounded source span, not model-authored text.
type evidenceSpan struct {
	Index    int    `json:"evidence_index"`
	Document string `json:"document"`
	Context  string `json:"source_context"`
	Text     string `json:"text"`
	source   int
}

var paragraphBreak = regexp.MustCompile(`\r?\n[ \t]*\r?\n`)
var sentenceEnd = regexp.MustCompile(`[.!?](?:[ \r\n]+|$)`)
var fieldLine = regexp.MustCompile(`(?m)^[A-Za-z][A-Za-z /_-]{1,80}:[ \t]*[^\r\n]+$`)

func evidenceSpans(sources []knowledge.Source) []evidenceSpan {
	passages := make([]EvidencePassage, 0, len(sources))
	for _, s := range sources {
		passages = append(passages, EvidencePassage{s.Reference, pageContext(s), s.Page.Text})
	}
	return passageSpans(passages)
}
func passageSpans(sources []EvidencePassage) []evidenceSpan {
	spans := []evidenceSpan{}
	total := 0
	for source, s := range sources {
		for _, block := range evidenceParagraphs(s.Text) {
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
				if len(text) == 0 || len(text) > maxEvidenceBytes || len(strings.Fields(text)) < 5 || (!sentenceEnd.MatchString(text) && len(fieldLine.FindAllStringIndex(text, -1)) < 2) {
					continue
				}
				if len(spans) >= 64 || total+len(text) > 24000 {
					return spans
				}
				spans = append(spans, evidenceSpan{Index: len(spans), Document: s.Reference, Context: s.Title, Text: text, source: source})
				total += len(text)
			}
		}
	}
	return spans
}

// These explicit authority escalation requests are unsupported by this API.
// This is a supplemental behavior guard, never an access-control boundary.
var authorityOverride = regexp.MustCompile(`(?i)\b(ignore|disregard|override)\b.{0,60}\b(rules|instructions|permissions|authorization)\b|\bact\s+as\s+(an?\s+)?(administrator|admin|root)\b|\b(execute|run)\s+(sql|shell|commands?|tools?)\b|\bfollow\s+(its|their|the\s+(attached|uploaded)\s+file.s)\s+instructions\b`)

// This API only uses server-selected current publications. Free-text directives
// choosing a particular source version cannot replace that selection contract.
// Factual questions about revisions are still handled by ordinary retrieval.
var sourceVersionDirective = regexp.MustCompile(`(?i)\b(use|using|from)\s+(the\s+)?(revision|version)\s+[a-z0-9]`)

func unsupportedAuthorityRequest(question string) bool {
	return authorityOverride.MatchString(question) || sourceVersionDirective.MatchString(question)
}
