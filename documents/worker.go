// Package documents processes quarantined PDFs using trusted local CLI tools.
// It never publishes documents or accepts caller-supplied scan evidence.
package documents

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrQuarantined = errors.New("document remains quarantined: verification or extraction failed")
var ErrNotReady = errors.New("document worker is not ready")

type Tools struct{ ClamScan, PDFInfo, PDFToText, PDFToPPM, Tesseract string }
type Config struct {
	QuarantineRoot, DatabasePath, ScannerRevision, ExtractionRevision, OCRRevision string
	Tools                                                                          Tools
	SignaturesValidUntil                                                           time.Time
	MaxInputBytes                                                                  int64
	MaxPages                                                                       int
	MaxPageTextBytes, MaxTotalTextBytes, MaxRasterBytes                            int64
	CommandTimeout, JobTimeout                                                     time.Duration
}
type Metadata struct {
	ID, Version           string
	Audiences             []string
	Effective, WithdrawAt time.Time
}
type PageProvenance struct {
	Number                                                      int
	Method, SourceHash, TextHash, RasterHash, ExtractorRevision string
}
type Output struct {
	Document   knowledge.Document
	Evidence   knowledge.Evidence
	Provenance []PageProvenance
}
type executor interface {
	run(context.Context, string, []string, io.Reader, int64) ([]byte, error)
}
type Worker struct {
	config Config
	exec   executor
}

func newWorker(c Config, e executor) (*Worker, error) {
	if e == nil || !filepath.IsAbs(c.QuarantineRoot) || !filepath.IsAbs(c.DatabasePath) || c.ScannerRevision == "" || c.ExtractionRevision == "" || c.OCRRevision == "" || !time.Now().Before(c.SignaturesValidUntil) || c.MaxInputBytes < 1 || c.MaxInputBytes > 20*1024*1024 || c.MaxPages < 1 || c.MaxPages > 100 || c.MaxPageTextBytes < 1 || c.MaxPageTextBytes > 1024*1024 || c.MaxTotalTextBytes < c.MaxPageTextBytes || c.MaxTotalTextBytes > 16*1024*1024 || c.MaxRasterBytes < 1 || c.MaxRasterBytes > 16*1024*1024 || c.CommandTimeout <= 0 || c.CommandTimeout > 30*time.Second || c.JobTimeout < c.CommandTimeout || c.JobTimeout > 5*time.Minute {
		return nil, ErrNotReady
	}
	info, err := os.Lstat(c.QuarantineRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrNotReady
	}
	for _, tool := range []string{c.Tools.ClamScan, c.Tools.PDFInfo, c.Tools.PDFToText, c.Tools.PDFToPPM, c.Tools.Tesseract} {
		if !filepath.IsAbs(tool) {
			return nil, ErrNotReady
		}
	}
	return &Worker{config: c, exec: e}, nil
}

// New validates genuine installed tools and a separately provisioned signature
// database. Configuration and executables must be mounted read-only and trusted.
func New(c Config) (*Worker, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrNotReady
	}
	w, err := newWorker(c, osExecutor{})
	if err != nil {
		return nil, err
	}
	for _, tool := range []string{c.Tools.ClamScan, c.Tools.PDFInfo, c.Tools.PDFToText, c.Tools.PDFToPPM, c.Tools.Tesseract} {
		info, err := os.Stat(tool)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
			return nil, ErrNotReady
		}
	}
	files, err := os.ReadDir(c.DatabasePath)
	if err != nil {
		return nil, ErrNotReady
	}
	signatures := false
	for _, entry := range files {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".cvd") || strings.HasSuffix(entry.Name(), ".cld")) {
			signatures = true
		}
	}
	if !signatures {
		return nil, ErrNotReady
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.CommandTimeout)
	defer cancel()
	for _, probe := range []struct {
		program string
		args    []string
	}{{c.Tools.ClamScan, []string{"--version"}}, {c.Tools.PDFInfo, []string{"-v"}}, {c.Tools.PDFToText, []string{"-v"}}, {c.Tools.PDFToPPM, []string{"-v"}}, {c.Tools.Tesseract, []string{"--version"}}} {
		_, err := w.exec.run(ctx, probe.program, probe.args, nil, 16384)
		if err != nil {
			return nil, ErrNotReady
		}
	}
	return w, nil
}
func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

var pagesPattern = regexp.MustCompile(`(?m)^Pages:\s+([0-9]+)\s*$`)
var encryptedPattern = regexp.MustCompile(`(?mi)^Encrypted:\s+yes`)

func (w *Worker) command(ctx context.Context, tool string, args []string, limit int64) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, w.config.CommandTimeout)
	defer cancel()
	out, err := w.exec.run(child, tool, args, nil, limit)
	if err != nil || int64(len(out)) > limit || child.Err() != nil {
		return nil, ErrQuarantined
	}
	return out, nil
}
func (w *Worker) Process(ctx context.Context, input []byte, m Metadata) (Output, error) {
	fail := func() (Output, error) { return Output{}, ErrQuarantined }
	if w == nil || input == nil || m.ID == "" || m.Version == "" || len(m.Audiences) == 0 || m.Effective.IsZero() || !time.Now().Before(w.config.SignaturesValidUntil) {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, w.config.JobTimeout)
	defer cancel()
	if len(input) < 5 || int64(len(input)) > w.config.MaxInputBytes || !strings.HasPrefix(string(input[:5]), "%PDF-") || ctx.Err() != nil {
		return fail()
	}
	data := append([]byte(nil), input...)
	dir, err := os.MkdirTemp(w.config.QuarantineRoot, "pdf-")
	if err != nil {
		return fail()
	}
	defer os.RemoveAll(dir)
	inputPath := filepath.Join(dir, "original.pdf")
	if err = os.WriteFile(inputPath, data, 0600); err != nil {
		return fail()
	}
	scanArgs := []string{"--no-summary", "--infected", "--stdout", "--database=" + w.config.DatabasePath, "--follow-file-symlinks=0", "--follow-dir-symlinks=0", "--alert-exceeds-max=yes", "--max-filesize=" + strconv.FormatInt(w.config.MaxInputBytes, 10), "--max-scansize=" + strconv.FormatInt(w.config.MaxInputBytes*4, 10), inputPath}
	if _, err = w.command(ctx, w.config.Tools.ClamScan, scanArgs, 16384); err != nil {
		return fail()
	}
	info, err := w.command(ctx, w.config.Tools.PDFInfo, []string{inputPath}, 16384)
	if err != nil || encryptedPattern.Match(info) {
		return fail()
	}
	matches := pagesPattern.FindAllSubmatch(info, -1)
	if len(matches) != 1 {
		return fail()
	}
	pages, err := strconv.Atoi(string(matches[0][1]))
	if err != nil || pages < 1 || pages > w.config.MaxPages {
		return fail()
	}
	doc := knowledge.Document{ID: m.ID, Version: m.Version, Content: data, ContentHash: hash(data), Audiences: append([]string(nil), m.Audiences...), Effective: m.Effective, WithdrawAt: m.WithdrawAt}
	provenance := make([]PageProvenance, 0, pages)
	var total int64
	for page := 1; page <= pages; page++ {
		n := strconv.Itoa(page)
		text, err := w.command(ctx, w.config.Tools.PDFToText, []string{"-f", n, "-l", n, "-enc", "UTF-8", "-nopgbrk", inputPath, "-"}, w.config.MaxPageTextBytes)
		if err != nil {
			return fail()
		}
		method, revision, rasterHash := "text", w.config.ExtractionRevision, ""
		if strings.TrimSpace(string(text)) == "" {
			raster, err := w.command(ctx, w.config.Tools.PDFToPPM, []string{"-f", n, "-l", n, "-singlefile", "-scale-to", "2000", "-r", "150", "-png", inputPath}, w.config.MaxRasterBytes)
			if err != nil || len(raster) == 0 {
				return fail()
			}
			rasterHash = hash(raster)
			rasterPath := filepath.Join(dir, "page.png")
			if err = os.WriteFile(rasterPath, raster, 0600); err != nil {
				return fail()
			}
			text, err = w.command(ctx, w.config.Tools.Tesseract, []string{rasterPath, "stdout", "-l", "eng", "--psm", "3"}, w.config.MaxPageTextBytes)
			os.Remove(rasterPath)
			if err != nil {
				return fail()
			}
			method, revision = "ocr", w.config.OCRRevision
		}
		if !utf8.Valid(text) {
			return fail()
		}
		total += int64(len(text))
		if total > w.config.MaxTotalTextBytes {
			return fail()
		}
		normalized := strings.TrimSpace(string(text))
		doc.Pages = append(doc.Pages, knowledge.Page{Number: page, Text: normalized, Method: method, ExtractorRevision: revision})
		provenance = append(provenance, PageProvenance{Number: page, Method: method, SourceHash: doc.ContentHash, TextHash: hash([]byte(normalized)), RasterHash: rasterHash, ExtractorRevision: revision})
	}
	if ctx.Err() != nil || !time.Now().Before(w.config.SignaturesValidUntil) {
		return fail()
	}
	fileInfo, err := os.Lstat(inputPath)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() != int64(len(data)) {
		return fail()
	}
	original, err := os.Open(inputPath)
	if err != nil {
		return fail()
	}
	openedInfo, statErr := original.Stat()
	if statErr != nil || !os.SameFile(fileInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		original.Close()
		return fail()
	}
	verifiedBytes, err := io.ReadAll(io.LimitReader(original, w.config.MaxInputBytes+1))
	original.Close()
	if err != nil || hash(verifiedBytes) != doc.ContentHash {
		return fail()
	}
	return Output{Document: doc, Evidence: knowledge.Evidence{ContentHash: doc.ContentHash, ScannerRevision: w.config.ScannerRevision, MalwareClean: true, ExtractionComplete: true}, Provenance: provenance}, nil
}
