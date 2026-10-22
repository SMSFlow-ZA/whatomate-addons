package documents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSupervisorCPUQuotaMilliAndLegacyBounds(t *testing.T) {
	for _, tc := range []struct {
		cpus, millis int
		want         string
	}{{1, 0, "1"}, {4, 0, "4"}, {0, 100, "0.100"}, {0, 500, "0.500"}, {0, 1250, "1.250"}, {0, 4000, "4.000"}} {
		got, e := (SupervisorConfig{CPUs: tc.cpus, CPUMillis: tc.millis}).CPUQuota()
		if e != nil || got != tc.want {
			t.Fatalf("quota(%d,%d)=%q %v", tc.cpus, tc.millis, got, e)
		}
	}
	for _, tc := range []struct{ cpus, millis int }{{0, 0}, {-1, 0}, {5, 0}, {0, -1}, {0, 99}, {0, 4001}, {1, 500}, {-1, 500}} {
		if _, e := (SupervisorConfig{CPUs: tc.cpus, CPUMillis: tc.millis}).CPUQuota(); e == nil {
			t.Fatalf("invalid quota accepted %+v", tc)
		}
	}
}

func TestDockerRunnerEmitsValidated500mArgument(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("real supervisor ownership fixture requires Linux root")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	defs := filepath.Join(root, "definitions")
	os.Mkdir(defs, 0700)
	os.WriteFile(filepath.Join(defs, "daily.cvd"), []byte("synthetic-argument-fixture-only"), 0400)
	sum, e := DefinitionsDigest(defs)
	if e != nil {
		t.Fatal(e)
	}
	job := filepath.Join(root, "job")
	os.Mkdir(job, 0700)
	for _, sub := range []string{"input", "config", "output"} {
		os.Mkdir(filepath.Join(job, sub), 0700)
	}
	capture := filepath.Join(root, "args")
	docker := filepath.Join(root, "docker")
	if e := os.WriteFile(docker, []byte("#!/bin/sh\nif [ \"$1\" = run ]; then printf '%s\\n' \"$@\" > \""+capture+"\"; fi\n"), 0700); e != nil {
		t.Fatal(e)
	}
	c := SupervisorConfig{DockerPath: docker, Image: "worker@sha256:" + strings.Repeat("a", 64), DefinitionsRoot: defs, DefinitionsHash: sum, CPUMillis: 500, MemoryMiB: 1536, PIDs: 64, TmpfsMiB: 128, Timeout: time.Second}
	if e := (dockerJobRunner{config: c}).run(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	args, e := os.ReadFile(capture)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(args), "--cpus\n0.500\n") || !strings.Contains(string(args), "--network\nnone\n") {
		t.Fatal("missing bounded Docker arguments", string(args))
	}
	os.Remove(capture)
	c.CPUMillis = -500
	if e := (dockerJobRunner{config: c}).run(context.Background(), job); e == nil {
		t.Fatal("invalid quota ran Docker")
	}
	if _, e = os.Stat(capture); !os.IsNotExist(e) {
		t.Fatal("invalid quota reached Docker executable")
	}
}
