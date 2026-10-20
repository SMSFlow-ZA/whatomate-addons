package documents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syntheticPDF() []byte {
	content := "BT /F1 24 Tf 72 720 Td (Synthetic document worker text.) Tj ET\n"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content)}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}
func TestRealLocalPDFTextAndOCRTools(t *testing.T) {
	tools := map[string]string{}
	for _, name := range []string{"pdfinfo", "pdftotext", "pdftoppm", "tesseract"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip("real local PDF/OCR tools unavailable; no scan claim")
		}
		tools[name] = path
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "synthetic.pdf")
	if err := os.WriteFile(input, syntheticPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e := osExecutor{}
	info, err := e.run(ctx, tools["pdfinfo"], []string{input}, nil, 16384)
	if err != nil || !pagesPattern.Match(info) {
		t.Fatalf("actual PDF page count failed: %v", err)
	}
	text, err := e.run(ctx, tools["pdftotext"], []string{"-f", "1", "-l", "1", "-enc", "UTF-8", "-nopgbrk", input, "-"}, nil, 4096)
	if err != nil || !strings.Contains(string(text), "Synthetic document worker text.") {
		t.Fatalf("actual text extraction failed: %q %v", text, err)
	}
	raster, err := e.run(ctx, tools["pdftoppm"], []string{"-f", "1", "-l", "1", "-singlefile", "-scale-to", "2000", "-png", input}, nil, 8*1024*1024)
	if err != nil || len(raster) == 0 {
		t.Fatalf("actual raster failed: %v", err)
	}
	image := filepath.Join(dir, "page.png")
	if err = os.WriteFile(image, raster, 0600); err != nil {
		t.Fatal(err)
	}
	text, err = e.run(ctx, tools["tesseract"], []string{image, "stdout", "-l", "eng", "--psm", "3"}, nil, 4096)
	if err != nil || !strings.Contains(string(text), "Synthetic document worker text") {
		t.Fatalf("actual OCR failed: %q %v", text, err)
	}
}

// This exercises the genuine ClamAV engine with one synthetic detection rule.
// It is not an accepted production malware signature corpus.
func TestRealScannerDetectsSyntheticMarkerBeforeExtraction(t *testing.T) {
	tools := Tools{}
	for name, target := range map[string]*string{"clamscan": &tools.ClamScan, "pdfinfo": &tools.PDFInfo, "pdftotext": &tools.PDFToText, "pdftoppm": &tools.PDFToPPM, "tesseract": &tools.Tesseract} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip("real scanner/PDF tools unavailable")
		}
		*target = path
	}
	database := t.TempDir()
	signature := "Synthetic.Document.Marker:0:*:53594e5448455449435f444554454354494f4e5f4d41524b4552\n"
	if err := os.WriteFile(filepath.Join(database, "synthetic.ndb"), []byte(signature), 0600); err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.Tools = tools
	c.DatabasePath = database
	c.ScannerRevision = "synthetic-test-signature-only"
	c.CommandTimeout = 10 * time.Second
	c.JobTimeout = 30 * time.Second
	// The private constructor permits the isolated test corpus; public New still
	// requires provisioned CVD/CLD definitions and startup executable checks.
	worker, err := newWorker(c, osExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	pdf := syntheticPDF()
	clean, err := worker.Process(context.Background(), pdf, metadata())
	if err != nil || !clean.Evidence.MalwareClean || !clean.Evidence.ExtractionComplete {
		t.Fatalf("genuine test-corpus scan/extraction failed: %v", err)
	}
	marked := append(append([]byte(nil), pdf...), []byte("SYNTHETIC_DETECTION_MARKER")...)
	out, err := worker.Process(context.Background(), marked, metadata())
	if err == nil || out.Evidence.MalwareClean || len(out.Document.Pages) != 0 {
		t.Fatal("genuine scanner detection failed closed boundary")
	}
}
