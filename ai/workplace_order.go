package ai

import "regexp"

// This is a retrieval ordering hint, never a conclusion about legal compliance
// or individual entitlement. Explicit legal questions retain ordinary mixed
// ranking; all remaining authorized evidence stays available for later batches.
var explicitLegalContext = regexp.MustCompile(`(?i)\b(law|legal|legislation|statutory|act|court|judgment|constitution|bcea|lra|eea|gazette|minimum wage)\b`)

func workplaceFirstBatch(r Request, ranked []evidenceSpan) []evidenceSpan {
	if explicitLegalContext.MatchString(r.Question) {
		return ranked
	}
	first := []evidenceSpan{}
	rest := []evidenceSpan{}
	for _, span := range ranked {
		company := span.source >= 0 && span.source < len(r.Sources) && r.Sources[span.source].PublicReference == nil
		if company && len(first) < 12 {
			first = append(first, span)
		} else {
			rest = append(rest, span)
		}
	}
	return append(first, rest...)
}
