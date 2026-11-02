package knowledge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDocumentTypeIsExplicitAndLegacyMetadataHashUnchanged(t *testing.T) {
	m := UploadMetadata{DocumentID: "id", Version: "1", Filename: "form.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	legacy := `{"document_id":"id","version":"1","filename":"form.pdf","media_type":"application/pdf","audiences":["staff"],"effective_at":"2026-01-01T00:00:00Z","withdraw_at":"0001-01-01T00:00:00Z"}`
	if string(raw) != legacy || m.Type() != "policy" || !validMetadata(m) {
		t.Fatal("legacy changed", string(raw))
	}
	m.DocumentType = "form"
	if validMetadata(m) {
		t.Fatal("untitled form accepted")
	}
	m.Title = "Request form"
	if !validMetadata(m) {
		t.Fatal("valid form denied")
	}
	m.DocumentType = "browser-special"
	if validMetadata(m) {
		t.Fatal("unknown type accepted")
	}
}
