package documents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testJobRunner struct {
	config  Config
	mutate  string
	entered chan struct{}
	release chan struct{}
}

func (r *testJobRunner) run(ctx context.Context, dir string) error {
	if r.entered != nil {
		close(r.entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.release:
		}
	}
	if r.mutate == "failure" {
		return errors.New("worker failed")
	}
	var j JobManifest
	b, _ := os.ReadFile(filepath.Join(dir, "input", "job.json"))
	json.Unmarshal(b, &j)
	var m Metadata
	b, _ = os.ReadFile(filepath.Join(dir, "input", "metadata.json"))
	json.Unmarshal(b, &m)
	input, _ := os.ReadFile(filepath.Join(dir, "input", "document.pdf"))
	w, err := newWorker(r.config, &contractExecutor{pages: 1})
	if err != nil {
		return err
	}
	out, err := w.Process(ctx, input, m)
	if err != nil {
		return err
	}
	artifact := JobArtifact{JobManifest: j, Output: out}
	if r.mutate == "hash" {
		artifact.InputHash = strings.Repeat("0", 64)
	}
	if r.mutate == "metadata" {
		artifact.Output.Document.Version = "forged"
	}
	if r.mutate == "page" {
		artifact.Output.Provenance[0].TextHash = "forged"
	}
	if r.mutate == "input" {
		os.Chmod(filepath.Join(dir, "input", "document.pdf"), 0600)
		os.WriteFile(filepath.Join(dir, "input", "document.pdf"), []byte("%PDF-tampered"), 0600)
	}
	if r.mutate == "symlink" {
		target := filepath.Join(dir, "external.json")
		b, _ = json.Marshal(artifact)
		os.WriteFile(target, b, 0600)
		return os.Symlink(target, filepath.Join(dir, "output", "result.json"))
	}
	b, _ = json.Marshal(artifact)
	if r.mutate == "malformed" {
		b = append(b, []byte(` {}`)...)
	}
	return os.WriteFile(filepath.Join(dir, "output", "result.json"), b, 0600)
}
func testSupervisor(t *testing.T, r *testJobRunner) *Supervisor {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0700)
	c := SupervisorConfig{JobRoot: root, Image: "reviewed@sha256:" + strings.Repeat("a", 64), DockerPath: "/usr/bin/docker", DefinitionsRoot: root, DefinitionsHash: strings.Repeat("b", 64), Worker: r.config, MaxConcurrent: 1, Timeout: time.Second, MemoryMiB: 1024, CPUs: 1, PIDs: 64, TmpfsMiB: 128}
	s, err := newSupervisor(c, r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func supervisorJob(id string) Job {
	input := []byte("%PDF-1.4\nsynthetic")
	return Job{ID: id, TenantID: "tenant", ActorID: "operator", InputHash: hash(input), Input: input, Metadata: metadata()}
}
func TestSupervisorBindsTrustedArtifactAndPreventsReplay(t *testing.T) {
	s := testSupervisor(t, &testJobRunner{config: testConfig(t)})
	j := supervisorJob("job-1")
	out, receipt, err := s.Process(context.Background(), j)
	if err != nil || receipt.JobID != j.ID || receipt.InputHash != j.InputHash || receipt.OutputHash == "" || out.Document.ContentHash != j.InputHash {
		t.Fatalf("artifact linkage: %+v %v", receipt, err)
	}
	if _, _, err = s.Process(context.Background(), j); err == nil {
		t.Fatal("job replay accepted")
	}
	j = supervisorJob("job-2")
	j.InputHash = strings.Repeat("0", 64)
	if _, _, err = s.Process(context.Background(), j); err == nil {
		t.Fatal("stale input hash accepted")
	}
	j = supervisorJob("job-3")
	j.ActorID = ""
	if _, _, err = s.Process(context.Background(), j); err == nil {
		t.Fatal("unauthenticated job accepted")
	}
}
func TestSupervisorRejectsMalformedUnlinkedOrFailedArtifacts(t *testing.T) {
	for _, mutation := range []string{"hash", "metadata", "page", "malformed", "failure", "input", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			s := testSupervisor(t, &testJobRunner{config: testConfig(t), mutate: mutation})
			out, receipt, err := s.Process(context.Background(), supervisorJob("job"))
			if err == nil || out.Evidence.MalwareClean || receipt.OutputHash != "" {
				t.Fatal("untrusted result escaped")
			}
		})
	}
}
func TestSupervisorBoundsConcurrencyAndDeadline(t *testing.T) {
	r := &testJobRunner{config: testConfig(t), entered: make(chan struct{}), release: make(chan struct{})}
	s := testSupervisor(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := s.Process(ctx, supervisorJob("first")); done <- err }()
	<-r.entered
	if _, _, err := s.Process(context.Background(), supervisorJob("second")); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent bound bypassed", err)
	}
	if err := <-done; err == nil {
		t.Fatal("deadline accepted")
	}
}

func TestSupervisorAcceptsImmutableLocalImageIDOnly(t *testing.T) {
	s := testSupervisor(t, &testJobRunner{config: testConfig(t)})
	c := s.config
	c.Image = "sha256:" + strings.Repeat("c", 64)
	if _, err := newSupervisor(c, s.runner); err != nil {
		t.Fatal("immutable Docker image ID rejected", err)
	}
	for _, image := range []string{"reviewed:latest", "sha256:short", "reviewed@sha256:short"} {
		c.Image = image
		if _, err := newSupervisor(c, s.runner); err == nil {
			t.Fatal("mutable/unpinned image accepted")
		}
	}
}
