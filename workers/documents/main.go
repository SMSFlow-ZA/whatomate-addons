// Command documents is a one-job, offline quarantine worker. All paths are fixed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"io"
	"os"
	"path/filepath"
)

func readRegular(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("invalid worker input")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > limit {
		return nil, errors.New("invalid worker input")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("worker input limit")
	}
	return b, nil
}
func decode(path string, limit int64, out any) error {
	b, err := readRegular(path, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid worker JSON")
	}
	return nil
}
func run() error {
	var config documents.Config
	if err := decode("/run/config/documents.json", 65536, &config); err != nil {
		return err
	}
	if config.QuarantineRoot != "/tmp/quarantine" {
		return errors.New("invalid quarantine configuration")
	}
	if err := os.MkdirAll(config.QuarantineRoot, 0700); err != nil {
		return err
	}
	if err := os.Chmod(config.QuarantineRoot, 0700); err != nil {
		return err
	}
	worker, err := documents.New(config)
	if err != nil {
		return err
	}
	var metadata documents.Metadata
	if err = decode("/input/metadata.json", 16384, &metadata); err != nil {
		return err
	}
	input, err := readRegular("/input/document.pdf", config.MaxInputBytes)
	if err != nil {
		return err
	}
	var manifest documents.JobManifest
	if err = decode("/input/job.json", 4096, &manifest); err != nil {
		return err
	}
	if err = documents.ValidateJobManifest(manifest, input, metadata); err != nil {
		return err
	}
	out, err := worker.Process(context.Background(), input, metadata)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("/output", ".documents-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if err = json.NewEncoder(f).Encode(documents.JobArtifact{JobManifest: manifest, Output: out}); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Link(temp, filepath.Join("/output", "result.json"))
}
func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("document quarantined or worker not ready\n")
		os.Exit(1)
	}
}
