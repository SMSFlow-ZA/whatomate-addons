package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// Hash frozen definition bytes with a fixed-size buffer. Definition databases
// can be hundreds of MiB; retaining a whole file is unnecessary for SHA-256.
func definitionFileDigest(path string, limit int64) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return "", ErrNotReady
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrNotReady
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() != before.Size() {
		return "", ErrNotReady
	}
	digest := sha256.New()
	n, err := io.CopyBuffer(digest, io.LimitReader(file, limit+1), make([]byte, 32<<10))
	if err != nil || n > limit || n != before.Size() {
		return "", ErrNotReady
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return "", ErrNotReady
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
