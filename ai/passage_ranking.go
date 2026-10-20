package ai

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
)

// Context is copied from the immutable page, not supplied by the model. It is
// untrusted topic context only; the returned answer still comes from one span.
func pageContext(source knowledge.Source) string {
	for _, line := range strings.Split(source.Page.Text, "\n") {
		line = strings.TrimSpace(line)
		if len(strings.Fields(line)) < 2 || len([]rune(line)) > 120 || strings.ToUpper(line) == line || strings.ContainsAny(line, "|:_") || pageNumberLine.MatchString(line) {
			continue
		}
		return line
	}
	return ""
}

var pageNumberLine = regexp.MustCompile(`(?i)^page\s+[0-9]+\b`)

func (p *DevelopmentProvider) rankEvidence(ctx context.Context, question string, spans []evidenceSpan) ([]evidenceSpan, error) {
	if len(spans) <= 8 {
		return spans, nil
	}
	scores := make(map[int]float64, len(spans))
	// Combine semantic similarity with rare query terms. Embeddings alone may
	// prefer a broad access paragraph over a specific form or procedure rule.
	terms := queryTerms(question)
	df := map[string]int{}
	for _, span := range spans {
		words := queryTerms(span.Context + " " + span.Text)
		for word := range terms {
			if words[word] {
				df[word]++
			}
		}
	}
	lexical := map[int]float64{}
	maxLex := 0.0
	for _, span := range spans {
		words := queryTerms(span.Context + " " + span.Text)
		for word := range terms {
			if words[word] {
				lexical[span.Index] += math.Log(float64(len(spans)+1)/float64(df[word]+1)) + 1
			}
		}
		if lexical[span.Index] > maxLex {
			maxLex = lexical[span.Index]
		}
	}
	for start := 0; start < len(spans); start += 19 {
		end := start + 19
		if end > len(spans) {
			end = len(spans)
		}
		texts := []string{question}
		for _, s := range spans[start:end] {
			texts = append(texts, s.Context+"\n"+s.Text)
		}
		vectors, err := p.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		for i := start; i < end; i++ {
			scores[spans[i].Index] = cosine(vectors[0], vectors[i-start+1])
			if maxLex > 0 {
				scores[spans[i].Index] += lexical[spans[i].Index] / maxLex
			}
		}
	}
	ranked := append([]evidenceSpan(nil), spans...)
	sort.SliceStable(ranked, func(i, j int) bool { return scores[ranked[i].Index] > scores[ranked[j].Index] })
	// Rank without discarding governed evidence. Shortlisting can lose a small
	// form section behind several similar policy sections; generation still
	// enforces the existing total input and output bounds.
	return ranked, nil
}

var rankWords = regexp.MustCompile(`[a-z0-9]+`)
var rankStopWords = strings.Fields("about after again also been before being between both could does doing during each from have having here into itself more most only other over same some such than that their them then there these they this those through under until very were what when where which while whom will with would your yourself should")

func queryTerms(text string) map[string]bool {
	result := map[string]bool{}
	for _, word := range rankWords.FindAllString(strings.ToLower(text), -1) {
		if len(word) < 3 {
			continue
		}
		stop := false
		for _, v := range rankStopWords {
			if word == v {
				stop = true
				break
			}
		}
		if stop {
			continue
		}
		if len(word) > 4 {
			word = strings.TrimSuffix(word, "s")
		}
		result[word] = true
	}
	return result
}
