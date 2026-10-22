package documents

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDefinitionsDigestStreamingPreservesCorpusIdentityAndBounds(t *testing.T) {
	root := t.TempDir()
	data := make([]byte, 4<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	path := filepath.Join(root, "main.cvd")
	if e := os.WriteFile(path, data, 0400); e != nil {
		t.Fatal(e)
	}
	expected := hash([]byte(fmt.Sprintf("%d:%s:%s\n", len("main.cvd"), "main.cvd", hash(data))))
	got, e := DefinitionsDigest(root)
	if e != nil || got != expected {
		t.Fatalf("corpus digest changed: %s %v", got, e)
	}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if _, e := DefinitionsDigest(root); e != nil {
				b.Fatal(e)
			}
		}
	})
	if allocated := result.AllocedBytesPerOp(); allocated > 512<<10 {
		t.Fatalf("definition hash allocates file content: %d bytes/op", allocated)
	} else {
		t.Logf("4MiB definition hash allocation: %d bytes/op", allocated)
	}
	if e := os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Truncate(path, (512<<20)+1); e != nil {
		t.Fatal(e)
	}
	if _, e := DefinitionsDigest(root); e == nil {
		t.Fatal("oversized definition accepted")
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("missing", path); e != nil {
		t.Fatal(e)
	}
	if _, e := DefinitionsDigest(root); e == nil {
		t.Fatal("linked definition accepted")
	}
}
