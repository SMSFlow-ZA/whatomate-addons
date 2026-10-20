// Command controller handles exactly one trusted queue claim on stdin. Deploy
// separately from the web process: only this controller can access Docker.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/SMSFlow-ZA/whatomate-addons/documents"
)

func strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid controller JSON")
	}
	return nil
}
func readJob(r io.Reader, limit int64) (documents.Job, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	var j documents.Job
	if err != nil || int64(len(b)) > limit {
		return j, errors.New("invalid job size")
	}
	if err = strict(b, &j); err != nil {
		return j, err
	}
	if j.ID == "" || j.TenantID == "" || j.ActorID == "" || j.InputHash == "" || len(j.Input) == 0 {
		return j, errors.New("missing trusted job identity")
	}
	return j, nil
}
func run() error {
	// This configuration is a private, operator-owned regular read-only file.
	path := "/run/config/supervisor.json"
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 || info.Mode().Perm()&0022 != 0 {
		return documents.ErrNotReady
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return documents.ErrNotReady
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return documents.ErrNotReady
	}
	var c documents.SupervisorConfig
	if strict(b, &c) != nil {
		return documents.ErrNotReady
	}
	supervisor, err := documents.NewSupervisor(c)
	if err != nil {
		return err
	}
	j, err := readJob(os.Stdin, 32*1024*1024)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	out, receipt, err := supervisor.Process(ctx, j)
	if err != nil {
		return err
	}
	// Only the trusted queue controller consumes this pipe. Never connect it to
	// a browser response or accept this shape from an HTTP request.
	return json.NewEncoder(os.Stdout).Encode(struct {
		Output  documents.Output
		Receipt documents.Receipt
	}{out, receipt})
}
func main() {
	if run() != nil {
		os.Stderr.WriteString("document controller failed; job remains quarantined\n")
		os.Exit(1)
	}
}
