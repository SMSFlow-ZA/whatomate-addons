// Package ai exposes retrieval-grounded assistance without model authority.
package ai

import (
	"context"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"strings"
	"time"
)

const FixedResponse = "I cannot provide an answer from approved current sources. Please contact a human reviewer."

type Request struct {
	Question string
	Sources  []knowledge.Source
}
type Completion struct {
	Answer    string   `json:"answer"`
	Citations []string `json:"citations"`
	Grounded  bool     `json:"grounded"`
	Inference bool     `json:"inference"`
}
type Generator interface {
	Generate(context.Context, Request) (Completion, error)
}
type Result struct {
	Text      string
	Citations []knowledge.Source
	Handoff   bool
}

func fallback() Result { return Result{Text: FixedResponse, Handoff: true} }
func answer(ctx context.Context, g Generator, r Request) Result {
	if g == nil || strings.TrimSpace(r.Question) == "" || len(r.Sources) == 0 {
		return fallback()
	}
	available := map[string]knowledge.Source{}
	for _, s := range r.Sources {
		if s.Reference == "" || s.ManifestHash == "" || s.Page.Number < 1 || s.Page.Text == "" {
			return fallback()
		}
		available[s.Reference] = s
	}
	// Preserve an independent snapshot even when an adapter mutates its request.
	r.Sources = append([]knowledge.Source(nil), r.Sources...)
	out, err := g.Generate(ctx, r)
	if err != nil || !out.Grounded || out.Inference || strings.TrimSpace(out.Answer) == "" || len(out.Citations) == 0 {
		return fallback()
	}
	result := Result{Text: out.Answer}
	supported := false
	seen := map[string]bool{}
	for _, ref := range out.Citations {
		s, ok := available[ref]
		if !ok {
			return fallback()
		}
		if strings.Contains(s.Page.Text, out.Answer) {
			supported = true
		}
		if !seen[ref] {
			result.Citations = append(result.Citations, s)
			seen[ref] = true
		}
	}
	if !supported {
		return fallback()
	}
	return result
}

// Service always retrieves itself and revalidates provenance after generation.
// Host authorizers, publication review and scanner adapters are trusted inputs.
type Retriever interface {
	Retrieve(context.Context, knowledge.Query, knowledge.Authorizer) ([]knowledge.Source, error)
}
type Service struct {
	Catalog    Retriever
	Authorizer knowledge.Authorizer
	Provider   Generator
	Now        func() time.Time
}

func (s Service) Answer(ctx context.Context, principal, audience, question string) Result {
	if s.Catalog == nil || s.Now == nil {
		return fallback()
	}
	q := knowledge.Query{Principal: principal, Audience: audience, Text: question, At: s.Now()}
	sources, err := s.Catalog.Retrieve(ctx, q, s.Authorizer)
	if err != nil {
		return fallback()
	}
	result := answer(ctx, s.Provider, Request{Question: question, Sources: sources})
	if result.Handoff {
		return result
	}
	q.At = s.Now()
	current, err := s.Catalog.Retrieve(ctx, q, s.Authorizer)
	if err != nil {
		return fallback()
	}
	refs := map[string]bool{}
	for _, v := range current {
		refs[v.Reference] = true
	}
	for _, v := range result.Citations {
		if !refs[v.Reference] {
			return fallback()
		}
	}
	return result
}

// ClassificationSuggestion carries no permission, tool or execution authority.
// Human approval must be authorized and durably audited by the host workflow.
type ClassificationSuggestion struct{ Label, Rationale string }

var ErrHumanApprovalRequired = errors.New("authorized human approval required")

func (s ClassificationSuggestion) Apply() error { return ErrHumanApprovalRequired }
