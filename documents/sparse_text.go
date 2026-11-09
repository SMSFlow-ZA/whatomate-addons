package documents

import (
	"strings"
	"unicode"
)

// Below 200 letters/digits, a native layer may be only a page number,
// masthead or footer over a scanned body. This is a generic OCR trigger, not
// an assertion that longer native text is complete. Count Unicode content,
// not whitespace/punctuation or UTF-8 byte length. Review remains required.
func sparseNativeText(text string) bool {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
			if n >= 200 {
				return false
			}
		}
	}
	return true
}

// OCR may omit a genuine short native instruction. Preserve its exact text
// when it is not already present (ignoring layout whitespace) in the OCR.
// The output method is OCR when nonblank raster text contributes; hashes bind
// the final combined text. Blank OCR cannot erase existing native content.
func preserveSparseNativeText(native, ocr string) (string, bool) {
	native, ocr = strings.TrimSpace(native), strings.TrimSpace(ocr)
	if ocr == "" && native != "" {
		return native, false
	}
	if native != "" && !strings.Contains(strings.Join(strings.Fields(ocr), " "), strings.Join(strings.Fields(native), " ")) {
		return native + "\n\n" + ocr, true
	}
	return ocr, true
}
