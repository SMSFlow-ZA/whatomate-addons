package documents

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

type sparseExecutor struct {
	native, ocr string
	failOCR     bool
	calls       []string
}

func (e *sparseExecutor) run(_ context.Context, program string, _ []string, _ io.Reader, _ int64) ([]byte, error) {
	name := filepath.Base(program)
	e.calls = append(e.calls, name)
	switch name {
	case "clamscan":
		return nil, nil
	case "pdfinfo":
		return []byte("Pages: 1\nEncrypted: no\n"), nil
	case "pdftotext":
		return []byte(e.native), nil
	case "pdftoppm":
		return []byte("synthetic raster"), nil
	case "tesseract":
		if e.failOCR {
			return nil, errors.New("synthetic OCR failure")
		}
		return []byte(e.ocr), nil
	}
	return nil, errors.New("unexpected tool")
}
func TestSparseWorkerRunsOCRAndBindsOriginalAndCombinedText(t *testing.T) {
	input := []byte("%PDF-1.4 synthetic source")
	for _, tc := range []struct{ name, native, ocr, method string }{
		{"sparse native overlay", "Synthetic page footer.", "Substantive scanned body instructions.", "ocr"},
		{"genuine short native blank OCR", "Short valid native instruction.", " ", "text"},
		{"blank native OCR", " ", "Scanned body.", "ocr"},
		{"genuinely blank page", " ", " ", "ocr"},
		{"substantive native", strings.Repeat("Meaningful native text. ", 20), "unused", "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &sparseExecutor{native: tc.native, ocr: tc.ocr}
			// Exercise Process with a fake tool executor; deployment readiness is tested
			// separately on Linux and is not bypassed in the production constructor.
			w := &Worker{config: testConfig(t), exec: e}
			out, err := w.Process(context.Background(), input, metadata())
			if err != nil {
				t.Fatal(err)
			}
			if string(out.Document.Content) != string(input) || out.Document.ContentHash != hash(input) || len(out.Document.Pages) != 1 {
				t.Fatal("original/page identity changed")
			}
			page, p := out.Document.Pages[0], out.Provenance[0]
			if page.Method != tc.method || p.Method != tc.method || page.Number != 1 || p.SourceHash != hash(input) || p.TextHash != hash([]byte(page.Text)) {
				t.Fatal("provenance mismatch")
			}
			if (tc.method == "ocr") != (p.RasterHash != "") {
				t.Fatal("raster evidence mismatch")
			}
			if strings.TrimSpace(tc.native) != "" && !strings.Contains(page.Text, strings.TrimSpace(tc.native)) {
				t.Fatal("native content lost")
			}
			called := strings.Contains(strings.Join(e.calls, ","), "tesseract")
			if called != sparseNativeText(tc.native) {
				t.Fatal("wrong OCR trigger")
			}
		})
	}
}
func TestSparseWorkerOCRFailureAndMergedPageLimitFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		e     sparseExecutor
		limit int64
	}{
		{"OCR failure", sparseExecutor{native: "Nonblank footer", failOCR: true}, 1024},
		{"merged page limit", sparseExecutor{native: strings.Repeat("a", 100), ocr: strings.Repeat("b", 200)}, 256},
		{"invalid native UTF8", sparseExecutor{native: string([]byte{255}), ocr: "valid"}, 1024},
		{"invalid OCR UTF8", sparseExecutor{native: "footer", ocr: string([]byte{255})}, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(t)
			c.MaxPageTextBytes = tc.limit
			w := &Worker{config: c, exec: &tc.e}
			out, err := w.Process(context.Background(), []byte("%PDF-1.4 synthetic"), metadata())
			if err == nil || out.Evidence.ExtractionComplete || len(out.Document.Pages) != 0 {
				t.Fatal("failed/partial extraction accepted")
			}
		})
	}
}
