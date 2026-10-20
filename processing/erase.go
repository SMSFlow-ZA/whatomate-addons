package processing

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type ErasureConfig struct{ JobRoot, ReceiptRoot string }
type ErasureRequest struct{ JobID, TenantID, ActorID, InputHash string }
type ErasureReceipt struct {
	JobID, TenantID, ActorID, InputHash, ManifestHash, WorkerReceiptHash string
	DeletedAt                                                            time.Time
	ReceiptHash                                                          string
}
type erasureIntent struct {
	Request                         ErasureRequest
	ManifestHash, WorkerReceiptHash string
}

var eraseID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
var eraseHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func eraseDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("unsafe erasure path")
	}
	for current := path; ; current = filepath.Dir(current) {
		s, e := os.Lstat(current)
		if e != nil || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe erasure directory")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
func erasePaths(c ErasureConfig, r ErasureRequest) (string, string, error) {
	if !eraseID.MatchString(r.JobID) || r.TenantID == "" || r.ActorID == "" || len(r.TenantID) > 256 || len(r.ActorID) > 256 || !eraseHash.MatchString(r.InputHash) || c.JobRoot == c.ReceiptRoot {
		return "", "", errors.New("invalid erasure identity")
	}
	if e := eraseDirectory(c.JobRoot); e != nil {
		return "", "", e
	}
	s, _ := os.Lstat(c.JobRoot)
	if s.Mode().Perm()&0077 != 0 {
		return "", "", errors.New("job root is not private")
	}
	if e := os.Mkdir(c.ReceiptRoot, 0700); e != nil && !os.IsExist(e) {
		return "", "", e
	}
	if e := eraseDirectory(c.ReceiptRoot); e != nil {
		return "", "", e
	}
	s, _ = os.Lstat(c.ReceiptRoot)
	if s.Mode().Perm()&0077 != 0 {
		return "", "", errors.New("receipt root is not private")
	}
	key := hash([]byte(r.TenantID + "\x00" + r.JobID))
	job := filepath.Join(c.JobRoot, key)
	relative, e := filepath.Rel(c.JobRoot, job)
	if e != nil || relative != key || strings.Contains(relative, string(os.PathSeparator)) {
		return "", "", errors.New("job outside root")
	}
	return job, filepath.Join(c.ReceiptRoot, key), nil
}
func eraseRead(path string) ([]byte, error) {
	s, e := os.Lstat(path)
	if e != nil || !s.Mode().IsRegular() || s.Mode()&os.ModeSymlink != 0 || s.Size() > 65536 {
		return nil, errors.New("invalid erasure artifact")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(s, opened) {
		return nil, errors.New("changed erasure artifact")
	}
	b, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || int64(len(b)) != s.Size() {
		return nil, errors.New("incomplete erasure artifact")
	}
	return b, nil
}
func eraseSync(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func eraseWrite(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return eraseSync(filepath.Dir(path))
}
func erasureDigest(r ErasureReceipt) string {
	r.ReceiptHash = ""
	b, _ := json.Marshal(r)
	return hash(b)
}

// EraseJob removes only the two immutable flat artifacts retained by a successful
// supervisor job. Unexpected files/directories/symlinks fail closed. A durable
// outside intent enables exact crash resume; no recursive deletion is performed.
func EraseJob(ctx context.Context, c ErasureConfig, r ErasureRequest) (ErasureReceipt, error) {
	job, base, e := erasePaths(c, r)
	if e != nil {
		return ErasureReceipt{}, e
	}
	if _, statErr := os.Lstat(base + ".receipt.json"); statErr == nil {
		b, e := eraseRead(base + ".receipt.json")
		if e != nil {
			return ErasureReceipt{}, e
		}
		var receipt ErasureReceipt
		if json.Unmarshal(b, &receipt) != nil {
			return receipt, errors.New("invalid erasure receipt")
		}
		return receipt, VerifyErasure(ctx, c, r, receipt)
	} else if !os.IsNotExist(statErr) {
		return ErasureReceipt{}, statErr
	}
	var intent erasureIntent
	if _, statErr := os.Lstat(base + ".intent.json"); statErr == nil {
		b, e := eraseRead(base + ".intent.json")
		if e != nil {
			return ErasureReceipt{}, e
		}
		if json.Unmarshal(b, &intent) != nil || intent.Request != r {
			return ErasureReceipt{}, errors.New("erasure intent mismatch")
		}
	} else if !os.IsNotExist(statErr) {
		return ErasureReceipt{}, statErr
	} else {
		if e = eraseDirectory(job); e != nil {
			return ErasureReceipt{}, e
		}
		entries, e := os.ReadDir(job)
		if e != nil || len(entries) != 2 {
			return ErasureReceipt{}, errors.New("unexpected job artifacts")
		}
		for _, entry := range entries {
			if entry.Name() != "manifest.json" && entry.Name() != "receipt.json" {
				return ErasureReceipt{}, errors.New("unexpected job artifact")
			}
		}
		mb, e := eraseRead(filepath.Join(job, "manifest.json"))
		if e != nil {
			return ErasureReceipt{}, e
		}
		var m documents.JobManifest
		if json.Unmarshal(mb, &m) != nil || m.JobID != r.JobID || m.TenantID != r.TenantID || m.ActorID != r.ActorID || m.InputHash != r.InputHash || !eraseHash.MatchString(m.MetadataHash) {
			return ErasureReceipt{}, errors.New("erasure manifest mismatch")
		}
		rb, e := eraseRead(filepath.Join(job, "receipt.json"))
		if e != nil {
			return ErasureReceipt{}, e
		}
		var receipt documents.Receipt
		if json.Unmarshal(rb, &receipt) != nil || receipt.JobID != m.JobID || receipt.TenantID != m.TenantID || receipt.ActorID != m.ActorID || receipt.InputHash != m.InputHash || receipt.MetadataHash != m.MetadataHash || !eraseHash.MatchString(receipt.OutputHash) {
			return ErasureReceipt{}, errors.New("worker receipt mismatch")
		}
		intent = erasureIntent{Request: r, ManifestHash: hash(mb), WorkerReceiptHash: hash(rb)}
		if e = eraseWrite(base+".intent.json", intent); e != nil {
			return ErasureReceipt{}, e
		}
	}
	if ctx.Err() != nil {
		return ErasureReceipt{}, ctx.Err()
	}
	if _, e = os.Lstat(job); e == nil {
		if e = eraseDirectory(job); e != nil {
			return ErasureReceipt{}, e
		}
		entries, e := os.ReadDir(job)
		if e != nil {
			return ErasureReceipt{}, e
		}
		for _, entry := range entries {
			want := ""
			switch entry.Name() {
			case "manifest.json":
				want = intent.ManifestHash
			case "receipt.json":
				want = intent.WorkerReceiptHash
			default:
				return ErasureReceipt{}, errors.New("unexpected resumed artifact")
			}
			b, e := eraseRead(filepath.Join(job, entry.Name()))
			if e != nil || hash(b) != want {
				return ErasureReceipt{}, errors.New("changed erasure artifact")
			}
		}
		for _, name := range []string{"receipt.json", "manifest.json"} {
			if e = os.Remove(filepath.Join(job, name)); e != nil && !os.IsNotExist(e) {
				return ErasureReceipt{}, e
			}
		}
		if e = os.Remove(job); e != nil {
			return ErasureReceipt{}, e
		}
	} else if !os.IsNotExist(e) {
		return ErasureReceipt{}, e
	}
	if e = eraseSync(c.JobRoot); e != nil {
		return ErasureReceipt{}, e
	}
	if _, e = os.Lstat(job); !os.IsNotExist(e) {
		return ErasureReceipt{}, errors.New("job still exists")
	}
	receipt := ErasureReceipt{JobID: r.JobID, TenantID: r.TenantID, ActorID: r.ActorID, InputHash: r.InputHash, ManifestHash: intent.ManifestHash, WorkerReceiptHash: intent.WorkerReceiptHash, DeletedAt: time.Now().UTC()}
	receipt.ReceiptHash = erasureDigest(receipt)
	if e = eraseWrite(base+".receipt.json", receipt); e != nil {
		return ErasureReceipt{}, e
	}
	return receipt, nil
}

// VerifyErasure is called by a trusted owner port, never on a browser assertion.
func VerifyErasure(ctx context.Context, c ErasureConfig, r ErasureRequest, receipt ErasureReceipt) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	job, base, e := erasePaths(c, r)
	if e != nil {
		return e
	}
	if receipt.JobID != r.JobID || receipt.TenantID != r.TenantID || receipt.ActorID != r.ActorID || receipt.InputHash != r.InputHash || !eraseHash.MatchString(receipt.ManifestHash) || !eraseHash.MatchString(receipt.WorkerReceiptHash) || receipt.DeletedAt.IsZero() || receipt.ReceiptHash != erasureDigest(receipt) {
		return errors.New("erasure receipt mismatch")
	}
	b, e := eraseRead(base + ".receipt.json")
	if e != nil {
		return e
	}
	var stored ErasureReceipt
	if json.Unmarshal(b, &stored) != nil || stored != receipt {
		return errors.New("erasure receipt not durable")
	}
	if _, e = os.Lstat(job); !os.IsNotExist(e) {
		return errors.New("erased job reappeared")
	}
	return nil
}
