package knowledge

import (
	"encoding/hex"
	"net"
	"net/url"
	"strings"
	"time"
)

// PublicReference is dated source provenance, not a finding of current law.
// It is immutable publication content and is covered by ManifestHash.
type PublicReference struct {
	Title          string `json:"title"`
	SourceURL      string `json:"source_url"`
	PublishedOn    string `json:"published_on"`
	Jurisdiction   string `json:"jurisdiction"`
	OriginalSHA256 string `json:"original_sha256"`
	ScopeNote      string `json:"scope_note"`
}

func ClonePublicReference(p *PublicReference) *PublicReference {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}
func EqualPublicReference(a, b *PublicReference) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func ValidPublicReference(p *PublicReference) bool {
	if p == nil {
		return false
	}
	for _, v := range []struct {
		s string
		n int
	}{{p.Title, 200}, {p.Jurisdiction, 100}, {p.ScopeNote, 1000}} {
		if strings.TrimSpace(v.s) == "" || len(v.s) > v.n || strings.ContainsAny(v.s, "\r\n\x00") {
			return false
		}
	}
	if _, e := time.Parse("2006-01-02", p.PublishedOn); e != nil {
		return false
	}
	b, e := hex.DecodeString(p.OriginalSHA256)
	if e != nil || len(b) != 32 || p.OriginalSHA256 != strings.ToLower(p.OriginalSHA256) {
		return false
	}
	u, e := url.Parse(p.SourceURL)
	return e == nil && len(p.SourceURL) <= 2048 && u.Scheme == "https" && u.User == nil && u.Hostname() != "" && u.Hostname() != "localhost" && net.ParseIP(u.Hostname()) == nil && u.Fragment == "" && u.Port() == ""
}
