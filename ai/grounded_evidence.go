package ai

import (
	"context"
	"strings"
)

// GenerateGroundedEvidence reuses the real generator and enforces exact quoted
// evidence independently. Neutral passages never acquire PDF provenance.
func GenerateGroundedEvidence(ctx context.Context, g Generator, r Request) (Completion, error) {
	if g == nil || unsupportedAuthorityRequest(r.Question) || strings.TrimSpace(r.Question) == "" || len(r.Question) > 4096 {
		return Completion{}, nil
	}
	passages := requestPassages(r)
	if len(passages) == 0 || len(passages) > 20 {
		return Completion{}, nil
	}
	available := map[string]string{}
	total := 0
	for _, p := range passages {
		if p.Reference == "" || p.Text == "" || available[p.Reference] != "" {
			return Completion{}, nil
		}
		total += len(p.Text)
		if total > 96000 {
			return Completion{}, nil
		}
		available[p.Reference] = p.Text
	}
	r.Sources = append(r.Sources[:0:0], r.Sources...)
	r.Evidence = append(r.Evidence[:0:0], r.Evidence...)
	out, e := g.Generate(ctx, r)
	if e != nil {
		return Completion{}, e
	}
	if !out.Grounded || out.Inference || len(out.Citations) == 0 || len(out.Citations) > 20 || strings.TrimSpace(out.Answer) == "" || len(out.Answer) > maxEvidenceBytes {
		return Completion{}, nil
	}
	supported := false
	seen := map[string]bool{}
	for _, ref := range out.Citations {
		v, ok := available[ref]
		if !ok || seen[ref] {
			return Completion{}, nil
		}
		seen[ref] = true
		if strings.Contains(v, out.Answer) {
			supported = true
		}
	}
	if !supported {
		return Completion{}, nil
	}
	return out, nil
}
