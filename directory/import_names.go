package directory

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func optionalName(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func nameValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Native import policy only. Reject input rather than modifying identifiers or
// formula text. Canonical E.164 phone validation is separate, so '+' is valid there.
func safeImportText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	trimmed := strings.TrimSpace(s)
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.ContainsAny(s, "<>") || strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "vbscript:") || strings.HasPrefix(lower, "data:") {
		return false
	}
	return true
}
func safeImportRow(r ImportRow) bool {
	for _, s := range []string{r.ID, r.Zone, r.Department, nameValue(r.FirstName), nameValue(r.LastName)} {
		if !safeImportText(s) {
			return false
		}
	}
	return utf8.RuneCountInString(nameValue(r.FirstName)) <= 200 && utf8.RuneCountInString(nameValue(r.LastName)) <= 200
}
