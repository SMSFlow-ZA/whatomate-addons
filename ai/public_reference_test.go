package ai

import (
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"strings"
	"testing"
)

func TestPublicReferenceReachesPromptContext(t *testing.T) {
	s := knowledge.Source{Reference: "ref", Page: knowledge.Page{Number: 1, Text: "Exact legal passage."}, PublicReference: &knowledge.PublicReference{Title: "Synthetic notice", PublishedOn: "2026-01-01", Jurisdiction: "South Africa", SourceURL: "https://www.gov.za/example", ScopeNote: "Review later amendments."}}
	passages := requestPassages(Request{Sources: []knowledge.Source{s}})
	if len(passages) != 1 || passages[0].Text != s.Page.Text || !strings.Contains(passages[0].Title, "not company policy") || !strings.Contains(passages[0].Title, "Review later amendments.") {
		t.Fatal("provenance absent from prompt or quote changed")
	}
}
