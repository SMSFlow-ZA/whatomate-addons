package directory

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLegacyWorkerCannotRunWithoutExplicitTrustedCommand(t *testing.T) {
	if _, err := (LegacyWorker{}).Read(context.Background(), []byte("xls"), "Staff", DefaultLimits()); err == nil {
		t.Fatal("unconfigured legacy parser enabled")
	}
}
func TestLegacyOutputIsBoundedAndStrict(t *testing.T) {
	l := DefaultLimits()
	for _, raw := range []string{`{"rows":[["x"]],"parser":"wrong"}`, `{"rows":[["x"]],"parser":"xlrd-2.0.2","extra":"forged"}`, `{"rows":[["x"]],"parser":"xlrd-2.0.2"} {}`} {
		if _, err := decodeLegacy([]byte(raw), l); err == nil {
			t.Fatal("invalid worker output accepted")
		}
	}
	l.MaxCellBytes = 1
	if _, err := decodeLegacy([]byte(`{"rows":[["large"]],"parser":"xlrd-2.0.2"}`), l); err == nil {
		t.Fatal("oversized cell accepted")
	}
}
func TestRealLegacyWorker(t *testing.T) {
	command := os.Getenv("XLS_WORKER_COMMAND")
	fixture := os.Getenv("XLS_TEST_FIXTURE")
	if command == "" || fixture == "" {
		t.Skip("isolated Linux XLS worker and synthetic fixture required")
	}
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if script := os.Getenv("XLS_WORKER_SCRIPT"); script != "" {
		args = []string{"-I", "-B", script}
	}
	rows, err := (LegacyWorker{Executable: command, Arguments: args, Timeout: 10 * time.Second}).Read(context.Background(), b, "Staff", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][0] != "synthetic-1" || rows[1][1] != "+27123456789" {
		t.Fatalf("wrong synthetic XLS result: %v", rows)
	}
}
