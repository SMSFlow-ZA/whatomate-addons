package documents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type contractExecutor struct {
	calls     []string
	failTool  string
	pages     int
	blank     bool
	oversized bool
	tamper    bool
}

func (e *contractExecutor) run(_ context.Context, program string, args []string, _ io.Reader, limit int64) ([]byte, error) {
	name := filepath.Base(program)
	e.calls = append(e.calls, name)
	if name == e.failTool {
		return nil, errors.New("synthetic tool failure")
	}
	switch name {
	case "clamscan":
		path := args[len(args)-1]
		file, err := os.Stat(path)
		if err != nil || file.Mode().Perm() != 0600 {
			return nil, errors.New("insecure input spool")
		}
		directory, err := os.Stat(filepath.Dir(path))
		if err != nil || directory.Mode().Perm() != 0700 {
			return nil, errors.New("insecure quarantine directory")
		}
		return nil, nil
	case "pdfinfo":
		return []byte(fmt.Sprintf("Pages: %d\nEncrypted: no\n", e.pages)), nil
	case "pdftotext":
		if e.tamper {
			if err := os.WriteFile(args[len(args)-2], []byte("%PDF-tampered"), 0600); err != nil {
				return nil, err
			}
		}
		if e.oversized {
			return []byte(strings.Repeat("x", int(limit+1))), nil
		}
		if e.blank {
			return []byte("\n"), nil
		}
		return []byte("Synthetic approved guidance."), nil
	case "pdftoppm":
		return []byte("synthetic raster contract fixture"), nil
	case "tesseract":
		return []byte("Synthetic OCR page."), nil
	}
	return nil, errors.New("unexpected executable")
}
func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return Config{QuarantineRoot: root, Tools: Tools{ClamScan: "/usr/bin/clamscan", PDFInfo: "/usr/bin/pdfinfo", PDFToText: "/usr/bin/pdftotext", PDFToPPM: "/usr/bin/pdftoppm", Tesseract: "/usr/bin/tesseract"}, DatabasePath: "/var/lib/clamav", ScannerRevision: "clamav-reviewed-signatures-1", ExtractionRevision: "poppler-1", OCRRevision: "tesseract-1", SignaturesValidUntil: time.Now().Add(time.Hour), MaxInputBytes: 4096, MaxPages: 2, MaxPageTextBytes: 1024, MaxTotalTextBytes: 2048, MaxRasterBytes: 4096, CommandTimeout: time.Second, JobTimeout: 10 * time.Second}
}
func metadata() Metadata {
	return Metadata{ID: "synthetic", Version: "1", Audiences: []string{"staff"}, Effective: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func TestScannerFailureNeverProducesEvidenceOrRunsExtraction(t *testing.T) {
	e := &contractExecutor{pages: 1, failTool: "clamscan"}
	w, err := newWorker(testConfig(t), e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Process(context.Background(), []byte("%PDF-1.4\nsynthetic"), metadata())
	if err == nil || out.Evidence.MalwareClean || len(e.calls) != 1 || e.calls[0] != "clamscan" {
		t.Fatalf("quarantine bypass: %+v %v %v", out, err, e.calls)
	}
}
func TestExtractionRecordsEveryPageHashAndOCRProvenance(t *testing.T) {
	e := &contractExecutor{pages: 2, blank: true}
	w, err := newWorker(testConfig(t), e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Process(context.Background(), []byte("%PDF-1.4\nsynthetic"), metadata())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Evidence.MalwareClean || !out.Evidence.ExtractionComplete || len(out.Document.Pages) != 2 || len(out.Provenance) != 2 || out.Document.ContentHash != out.Evidence.ContentHash {
		t.Fatalf("missing verified extraction: %+v", out)
	}
	for i, p := range out.Provenance {
		if p.Number != i+1 || p.Method != "ocr" || p.TextHash == "" || p.RasterHash == "" || p.SourceHash != out.Document.ContentHash {
			t.Fatalf("missing provenance: %+v", p)
		}
	}
}
func TestBoundsAndPartialExtractionFailClosed(t *testing.T) {
	for _, mode := range []string{"input", "pages", "text", "ocr"} {
		t.Run(mode, func(t *testing.T) {
			e := &contractExecutor{pages: 1}
			c := testConfig(t)
			input := "%PDF-1.4\nsynthetic"
			switch mode {
			case "input":
				input += strings.Repeat("x", 4096)
			case "pages":
				e.pages = 3
			case "text":
				e.oversized = true
			case "ocr":
				e.blank = true
				e.failTool = "tesseract"
			}
			w, err := newWorker(c, e)
			if err != nil {
				t.Fatal(err)
			}
			out, err := w.Process(context.Background(), []byte(input), metadata())
			if err == nil || out.Evidence.ExtractionComplete {
				t.Fatalf("bounds bypassed: %+v %v", out, err)
			}
		})
	}
}
func TestUnavailableToolsAndExpiredSignaturesDenyReadiness(t *testing.T) {
	c := testConfig(t)
	if _, err := New(c); err == nil {
		t.Fatal("missing tools considered ready")
	}
	c.SignaturesValidUntil = time.Now().Add(-time.Second)
	if _, err := newWorker(c, &contractExecutor{}); err == nil {
		t.Fatal("expired signature configuration accepted")
	}
}

func TestExecutorKeepsDiagnosticsOutOfSourceText(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := (osExecutor{}).run(ctx, path, []string{"-test.run=TestExecutorHelper", "--", "worker-helper-output"}, nil, 1024)
	if err != nil || string(out) != "source text" {
		t.Fatalf("diagnostic contaminated extracted source: %q %v", out, err)
	}
}
func TestExecutorHelper(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "worker-helper-flood" {
			fmt.Fprint(os.Stdout, strings.Repeat("x", 10000))
			os.Exit(0)
		}
		if arg == "worker-helper-sleep" {
			time.Sleep(2 * time.Second)
			os.Exit(0)
		}
		if arg == "worker-helper-output" {
			fmt.Fprint(os.Stdout, "source text")
			fmt.Fprint(os.Stderr, "diagnostic only")
			os.Exit(0)
		}
	}
}

func TestChangedOriginalCannotProduceVerifiedEvidence(t *testing.T) {
	e := &contractExecutor{pages: 1, tamper: true}
	w, err := newWorker(testConfig(t), e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Process(context.Background(), []byte("%PDF-1.4\nsynthetic"), metadata())
	if err == nil || out.Evidence.MalwareClean {
		t.Fatal("changed original accepted after scanning")
	}
}

func TestExecutorKillsTimeoutAndRejectsOutputOverflow(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"worker-helper-flood", "worker-helper-sleep"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		started := time.Now()
		_, err := (osExecutor{}).run(ctx, path, []string{"-test.run=TestExecutorHelper", "--", mode}, nil, 128)
		cancel()
		if err == nil || time.Since(started) > time.Second {
			t.Fatalf("CLI time/output bound failed: %s %v", mode, err)
		}
	}
}
