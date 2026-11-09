package ai

import "strings"

// PDF extraction can put a numbered rule's introduction, numbering, and list
// clauses in separate paragraphs. Preserve their exact contiguous source bytes
// as one bounded evidence unit rather than dropping the introduction and ranking
// contextless clauses. Already complete paragraphs remain separate.
func evidenceParagraphs(text string) []string {
	boundaries := paragraphBreak.FindAllStringIndex(text, -1)
	type block struct{ start, end int }
	blocks := []block{}
	start := 0
	for _, b := range boundaries {
		blocks = append(blocks, block{start, b[0]})
		start = b[1]
	}
	blocks = append(blocks, block{start, len(text)})
	result := []string{}
	for i := 0; i < len(blocks); i++ {
		first := blocks[i].start
		end := blocks[i].end
		current := strings.TrimSpace(text[first:end])
		if !sentenceEnd.MatchString(current) && len(fieldLine.FindAllStringIndex(current, -1)) < 2 {
			for i+1 < len(blocks) && len(text[first:blocks[i+1].end]) <= maxEvidenceBytes {
				// Structured worksheet fields are already independently usable.
				if len(fieldLine.FindAllStringIndex(text[blocks[i+1].start:blocks[i+1].end], -1)) >= 2 {
					break
				}
				i++
				end = blocks[i].end
				current = strings.TrimSpace(text[first:end])
				if sentenceEnd.MatchString(current) || len(fieldLine.FindAllStringIndex(current, -1)) >= 2 {
					break
				}
			}
		}
		result = append(result, current)
	}
	return result
}
