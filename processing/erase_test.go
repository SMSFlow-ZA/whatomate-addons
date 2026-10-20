package processing

import (
	"context"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func eraseFixture(t *testing.T) (ErasureConfig, ErasureRequest, string) {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0700)
	c := ErasureConfig{JobRoot: root, ReceiptRoot: filepath.Join(root, ".erasure-receipts")}
	r := ErasureRequest{JobID: "synthetic-job", TenantID: "tenant", ActorID: "operator", InputHash: strings.Repeat("a", 64)}
	job := filepath.Join(root, hash([]byte(r.TenantID+"\x00"+r.JobID)))
	os.Mkdir(job, 0700)
	m := documents.JobManifest{JobID: r.JobID, TenantID: r.TenantID, ActorID: r.ActorID, InputHash: r.InputHash, MetadataHash: strings.Repeat("b", 64)}
	mb, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(job, "manifest.json"), mb, 0400)
	receipt := documents.Receipt{JobID: m.JobID, TenantID: m.TenantID, ActorID: m.ActorID, InputHash: m.InputHash, MetadataHash: m.MetadataHash, OutputHash: strings.Repeat("c", 64), CompletedAt: time.Now().UTC()}
	rb, _ := json.Marshal(receipt)
	os.WriteFile(filepath.Join(job, "receipt.json"), rb, 0400)
	return c, r, job
}
func TestErasureBoundIdentityAndDurableReplay(t *testing.T) {
	c, r, job := eraseFixture(t)
	bad := r
	bad.ActorID = "other"
	if _, e := EraseJob(context.Background(), c, bad); e == nil {
		t.Fatal("wrong actor accepted")
	}
	if _, e := os.Stat(job); e != nil {
		t.Fatal("wrong actor removed job")
	}
	receipt, e := EraseJob(context.Background(), c, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(job); !os.IsNotExist(e) {
		t.Fatal("job remains")
	}
	if e = VerifyErasure(context.Background(), c, r, receipt); e != nil {
		t.Fatal(e)
	}
	again, e := EraseJob(context.Background(), c, r)
	if e != nil || again != receipt {
		t.Fatal("not idempotent", e)
	}
	forged := receipt
	forged.InputHash = strings.Repeat("d", 64)
	if VerifyErasure(context.Background(), c, r, forged) == nil {
		t.Fatal("forged receipt accepted")
	}
	os.Mkdir(job, 0700)
	if VerifyErasure(context.Background(), c, r, receipt) == nil {
		t.Fatal("reappeared job accepted")
	}
}
func TestErasureRejectsUnknownFilesAndLinks(t *testing.T) {
	t.Run("unexpected", func(t *testing.T) {
		c, r, job := eraseFixture(t)
		os.WriteFile(filepath.Join(job, "unrelated"), []byte("preserve"), 0600)
		if _, e := EraseJob(context.Background(), c, r); e == nil {
			t.Fatal("unknown artifact accepted")
		}
		if _, e := os.Stat(filepath.Join(job, "unrelated")); e != nil {
			t.Fatal("unrelated file removed")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		c, r, job := eraseFixture(t)
		os.Remove(filepath.Join(job, "receipt.json"))
		other := filepath.Join(c.JobRoot, "unrelated")
		os.WriteFile(other, []byte("preserve"), 0600)
		if e := os.Symlink(other, filepath.Join(job, "receipt.json")); e != nil {
			t.Skip(e)
		}
		if _, e := EraseJob(context.Background(), c, r); e == nil {
			t.Fatal("symlink accepted")
		}
		if b, e := os.ReadFile(other); e != nil || string(b) != "preserve" {
			t.Fatal("outside target touched")
		}
	})
}
