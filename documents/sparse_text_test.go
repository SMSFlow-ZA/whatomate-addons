package documents

import (
	"strings"
	"testing"
)

func TestSparseNativeTextNeedsOCR(t *testing.T) {
	for _, s := range []string{"", "  \n", "Publication footer page 12", strings.Repeat("- ", 500), strings.Repeat("a", 199)} {
		if !sparseNativeText(s) {
			t.Fatal("sparse layer bypassed OCR")
		}
	}
	for _, s := range []string{strings.Repeat("a", 200), strings.Repeat("界", 200)} {
		if sparseNativeText(s) {
			t.Fatal("substantive layer unnecessarily considered sparse")
		}
	}
}
func TestSparseOCRPreservesNativeContent(t *testing.T) {
	tests := []struct {
		native, ocr, want string
		used              bool
	}{
		{"Important native instruction.", "  ", "Important native instruction.", false},
		{"", "Body read from image.", "Body read from image.", true},
		{"", " ", "", true},
		{"Native heading", "Native heading\nBody read from image.", "Native heading\nBody read from image.", true},
		{"Native\nheading", "Native heading\nBody read from image.", "Native heading\nBody read from image.", true},
		{"Native instruction.", "Body read from image.", "Native instruction.\n\nBody read from image.", true},
	}
	for _, tc := range tests {
		got, used := preserveSparseNativeText(tc.native, tc.ocr)
		if got != tc.want || used != tc.used {
			t.Fatalf("native text preservation mismatch: %q %v", got, used)
		}
	}
}
