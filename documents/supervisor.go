package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

var ErrBusy = errors.New("document supervisor concurrency limit reached")

// Job is supplied only by an authenticated controller after claiming a durable
// job. Identity fields are trusted controller context, never upload metadata.
type Job struct {
	ID, TenantID, ActorID, InputHash string
	Input                            []byte
	Metadata                         Metadata
}
type JobManifest struct{ JobID, TenantID, ActorID, InputHash, MetadataHash string }
type JobArtifact struct {
	JobManifest
	Output Output
}

// ValidateJobManifest binds a one-job worker's fixed-path input to its manifest.
func ValidateJobManifest(m JobManifest, input []byte, metadata Metadata) error {
	b, err := json.Marshal(metadata)
	if err != nil || !safeJobID.MatchString(m.JobID) || m.TenantID == "" || m.ActorID == "" || m.InputHash != hash(input) || m.MetadataHash != hash(b) {
		return ErrQuarantined
	}
	return nil
}

type Receipt struct {
	JobID, TenantID, ActorID, InputHash, MetadataHash, OutputHash, Image, DefinitionsHash string
	CompletedAt, SignaturesValidUntil                                                     time.Time
}
type SupervisorConfig struct {
	JobRoot, DockerPath, Image, DefinitionsRoot, DefinitionsHash string
	Worker                                                       Config
	MaxConcurrent, MemoryMiB, CPUs, PIDs, TmpfsMiB               int
	Timeout                                                      time.Duration
}
type jobRunner interface {
	run(context.Context, string) error
}
type Supervisor struct {
	config SupervisorConfig
	runner jobRunner
	slots  chan struct{}
}

var safeJobID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
var pinnedImage = regexp.MustCompile(`^(?:[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:|sha256:)[a-f0-9]{64}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func newSupervisor(c SupervisorConfig, r jobRunner) (*Supervisor, error) {
	if r == nil || !filepath.IsAbs(c.JobRoot) || !filepath.IsAbs(c.DockerPath) || !filepath.IsAbs(c.DefinitionsRoot) || !pinnedImage.MatchString(c.Image) || !digestPattern.MatchString(c.DefinitionsHash) || c.MaxConcurrent < 1 || c.MaxConcurrent > 16 || c.MemoryMiB < 128 || c.MemoryMiB > 4096 || c.CPUs < 1 || c.CPUs > 4 || c.PIDs < 16 || c.PIDs > 128 || c.TmpfsMiB < 32 || c.TmpfsMiB > 512 || c.Timeout <= 0 || c.Timeout > 6*time.Minute {
		return nil, ErrNotReady
	}
	info, err := os.Lstat(c.JobRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrNotReady
	}
	wc := c.Worker
	wc.QuarantineRoot = c.JobRoot
	if _, err = newWorker(wc, osExecutor{}); err != nil {
		return nil, err
	}
	return &Supervisor{config: c, runner: r, slots: make(chan struct{}, c.MaxConcurrent)}, nil
}

// DefinitionsDigest hashes an immutable, reviewed definition directory. It
// rejects links and nested entries and requires provisioned CVD/CLD definitions.
// This records operator acceptance; it does not verify upstream signatures.
func DefinitionsDigest(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrNotReady
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", ErrNotReady
	}
	var corpus bytes.Buffer
	accepted := false
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024*1024 {
			return "", ErrNotReady
		}
		b, err := readArtifact(filepath.Join(root, entry.Name()), 512*1024*1024)
		if err != nil {
			return "", ErrNotReady
		}
		fmt.Fprintf(&corpus, "%d:%s:%s\n", len(entry.Name()), entry.Name(), hash(b))
		if strings.HasSuffix(entry.Name(), ".cvd") || strings.HasSuffix(entry.Name(), ".cld") {
			accepted = true
		}
	}
	if !accepted {
		return "", ErrNotReady
	}
	return hash(corpus.Bytes()), nil
}

func NewSupervisor(c SupervisorConfig) (*Supervisor, error) {
	if runtime.GOOS != "linux" || c.Worker.QuarantineRoot != "/tmp/quarantine" || c.Worker.DatabasePath != "/var/lib/clamav" {
		return nil, ErrNotReady
	}
	info, err := os.Stat(c.DockerPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return nil, ErrNotReady
	}
	digest, err := DefinitionsDigest(c.DefinitionsRoot)
	if err != nil || digest != c.DefinitionsHash {
		return nil, ErrNotReady
	}
	return newSupervisor(c, dockerJobRunner{config: c})
}

func readArtifact(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return nil, ErrQuarantined
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrQuarantined
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() {
		return nil, ErrQuarantined
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrQuarantined
	}
	return b, nil
}
func strictArtifact(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrQuarantined
	}
	return nil
}
func writeJobFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Supervisor) Process(ctx context.Context, j Job) (Output, Receipt, error) {
	fail := func() (Output, Receipt, error) { return Output{}, Receipt{}, ErrQuarantined }
	if s == nil || !safeJobID.MatchString(j.ID) || j.TenantID == "" || j.ActorID == "" || len(j.TenantID) > 256 || len(j.ActorID) > 256 || !digestPattern.MatchString(j.InputHash) || j.InputHash != hash(j.Input) || len(j.Input) < 5 || int64(len(j.Input)) > s.config.Worker.MaxInputBytes || !bytes.HasPrefix(j.Input, []byte("%PDF-")) || !time.Now().Before(s.config.Worker.SignaturesValidUntil) {
		return fail()
	}
	j.Input = append([]byte(nil), j.Input...)
	j.Metadata.Audiences = append([]string(nil), j.Metadata.Audiences...)
	metadataBytes, err := json.Marshal(j.Metadata)
	if err != nil || len(metadataBytes) > 16384 || j.Metadata.ID == "" || j.Metadata.Version == "" || len(j.Metadata.Audiences) == 0 || j.Metadata.Effective.IsZero() {
		return fail()
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return Output{}, Receipt{}, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	if ctx.Err() != nil {
		return fail()
	}
	// Atomic directory creation is the permanent replay marker, including failures.
	dir := filepath.Join(s.config.JobRoot, hash([]byte(j.TenantID+"\x00"+j.ID)))
	if err = os.Mkdir(dir, 0700); err != nil {
		return fail()
	}
	manifest := JobManifest{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: hash(metadataBytes)}
	mb, _ := json.Marshal(manifest)
	cb, _ := json.Marshal(s.config.Worker)
	// Retain only the receipt/manifest and replay marker after returning. Failed
	// originals remain in the queue's separately governed quarantine, not logs.
	defer func() {
		for _, name := range []string{"input", "output", "config"} {
			os.RemoveAll(filepath.Join(dir, name))
		}
	}()
	for _, name := range []string{"input", "output", "config"} {
		if err = os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			return fail()
		}
	}
	for name, b := range map[string][]byte{"input/document.pdf": j.Input, "input/metadata.json": metadataBytes, "input/job.json": mb, "config/documents.json": cb, "manifest.json": mb} {
		if err = writeJobFile(filepath.Join(dir, filepath.FromSlash(name)), b); err != nil {
			return fail()
		}
	}
	if err = s.runner.run(ctx, dir); err != nil || ctx.Err() != nil || !time.Now().Before(s.config.Worker.SignaturesValidUntil) {
		return fail()
	}
	// Verify inputs again after the isolated worker exits.
	for name, want := range map[string]string{"document.pdf": j.InputHash, "metadata.json": manifest.MetadataHash, "job.json": hash(mb)} {
		b, e := readArtifact(filepath.Join(dir, "input", name), s.config.Worker.MaxInputBytes+16384)
		if e != nil || hash(b) != want {
			return fail()
		}
	}
	b, err := readArtifact(filepath.Join(dir, "output", "result.json"), 64*1024*1024)
	if err != nil {
		return fail()
	}
	var a JobArtifact
	if strictArtifact(b, &a) != nil || a.JobManifest != manifest || !s.validOutput(a.Output, j) {
		return fail()
	}
	receipt := Receipt{JobID: j.ID, TenantID: j.TenantID, ActorID: j.ActorID, InputHash: j.InputHash, MetadataHash: manifest.MetadataHash, OutputHash: hash(b), Image: s.config.Image, DefinitionsHash: s.config.DefinitionsHash, CompletedAt: time.Now().UTC(), SignaturesValidUntil: s.config.Worker.SignaturesValidUntil}
	rb, _ := json.Marshal(receipt)
	if writeJobFile(filepath.Join(dir, "receipt.json"), rb) != nil {
		return fail()
	}
	return a.Output, receipt, nil
}
func (s *Supervisor) validOutput(o Output, j Job) bool {
	d, e := o.Document, o.Evidence
	c := s.config.Worker
	if !bytes.Equal(d.Content, j.Input) || d.ContentHash != j.InputHash || e.ContentHash != j.InputHash || !e.MalwareClean || !e.ExtractionComplete || e.ScannerRevision != c.ScannerRevision || d.ID != j.Metadata.ID || d.Version != j.Metadata.Version || !d.Effective.Equal(j.Metadata.Effective) || !d.WithdrawAt.Equal(j.Metadata.WithdrawAt) || len(d.Pages) < 1 || len(d.Pages) > c.MaxPages || len(d.Pages) != len(o.Provenance) {
		return false
	}
	audiences, _ := json.Marshal(d.Audiences)
	expected, _ := json.Marshal(j.Metadata.Audiences)
	if !bytes.Equal(audiences, expected) {
		return false
	}
	var total int64
	for i, p := range d.Pages {
		v := o.Provenance[i]
		revision := c.ExtractionRevision
		if p.Method == "ocr" {
			revision = c.OCRRevision
			if !digestPattern.MatchString(v.RasterHash) {
				return false
			}
		} else if p.Method != "text" || v.RasterHash != "" {
			return false
		}
		total += int64(len(p.Text))
		if !utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) != p.Text || int64(len(p.Text)) > c.MaxPageTextBytes || total > c.MaxTotalTextBytes || p.Number != i+1 || v.Number != p.Number || v.Method != p.Method || p.ExtractorRevision != revision || v.ExtractorRevision != revision || v.SourceHash != j.InputHash || v.TextHash != hash([]byte(p.Text)) {
			return false
		}
	}
	return true
}

type dockerJobRunner struct{ config SupervisorConfig }

func (r dockerJobRunner) run(ctx context.Context, dir string) error {
	c := r.config
	digest, err := DefinitionsDigest(c.DefinitionsRoot)
	if err != nil || digest != c.DefinitionsHash {
		return ErrNotReady
	}
	name := "documents-" + filepath.Base(dir)
	// An out-of-process controller owns this socket; never construct this runner
	// in the HTTP process. No user-supplied command, path, mount or option exists.
	for _, sub := range []string{"input", "output", "config"} {
		path := filepath.Join(dir, sub)
		if err := os.Chown(path, 10001, 10001); err != nil {
			return err
		}
		if sub != "output" {
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err = os.Chown(filepath.Join(path, entry.Name()), 10001, 10001); err != nil {
					return err
				}
			}
		}
	}
	mount := func(source, target string, ro bool) string {
		v := "type=bind,src=" + source + ",dst=" + target
		if ro {
			v += ",readonly"
		}
		return v
	}
	args := []string{"run", "--rm", "--pull=never", "--name", name, "--user", "10001:10001", "--read-only", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(c.PIDs), "--cpus", strconv.Itoa(c.CPUs), "--memory", strconv.Itoa(c.MemoryMiB) + "m", "--memory-swap", strconv.Itoa(c.MemoryMiB) + "m", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=" + strconv.Itoa(c.TmpfsMiB) + "m,uid=10001,gid=10001", "--mount", mount(filepath.Join(dir, "input"), "/input", true), "--mount", mount(filepath.Join(dir, "config"), "/run/config", true), "--mount", mount(c.DefinitionsRoot, "/var/lib/clamav", true), "--mount", mount(filepath.Join(dir, "output"), "/output", false), c.Image}
	// Killing the CLI alone does not kill its container. Always force-remove the
	// named container using an independent bounded cleanup context.
	cleanup := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := (osExecutor{}).run(cleanup, c.DockerPath, []string{"rm", "--force", name}, nil, 16384)
		return err
	}
	_, err = (osExecutor{}).run(ctx, c.DockerPath, args, nil, 16384)
	// With --rm, a normally finished container is already gone; Docker rm then
	// reports not-found. Inspect with a name filter after forced cleanup instead.
	cleanup()
	verify, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remaining, verifyErr := (osExecutor{}).run(verify, c.DockerPath, []string{"ps", "--all", "--quiet", "--filter", "name=^/" + name + "$"}, nil, 16384)
	if verifyErr != nil || len(bytes.TrimSpace(remaining)) != 0 {
		return ErrQuarantined
	}
	after, digestErr := DefinitionsDigest(c.DefinitionsRoot)
	if digestErr != nil || after != c.DefinitionsHash {
		return ErrQuarantined
	}
	return err
}
