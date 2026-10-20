package ai

import "testing"

func TestSourceQuoteOnlyToleratesWhitespace(t *testing.T) {
	source := "Record the concern.\r\nOffer the form."
	if q := sourceQuote(source, "Record the concern. Offer the form."); q != source {
		t.Fatal(q)
	}
	for _, q := range []string{"Record the allegation. Offer the form.", "record the concern. Offer the form.", "Record the concern Offer the form."} {
		if sourceQuote(source, q) != "" {
			t.Fatal("changed words accepted")
		}
	}
}
