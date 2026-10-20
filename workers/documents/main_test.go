package main

import (
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerRejectsSymlinkAndOversizedInputs(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	if err := os.WriteFile(input, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegular(input, 2); err == nil {
		t.Fatal("oversized input accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(input, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := readRegular(link, 1024); err == nil {
		t.Fatal("symlink input accepted")
	}
}
func TestUploadedMetadataCannotSupplyEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, []byte(`{"ID":"synthetic","Evidence":{"MalwareClean":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var m documents.Metadata
	if err := decode(path, 1024, &m); err == nil {
		t.Fatal("uploaded evidence accepted")
	}
}
