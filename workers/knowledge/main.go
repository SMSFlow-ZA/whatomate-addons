// The knowledge controller is a trusted separate process. Only this process,
// never the HTTP host, receives the Docker socket and supervisor configuration.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/documents"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/processing"
	_ "github.com/jackc/pgx/v5/stdlib"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type configuration struct {
	ProfileID, WorkerID string
	Supervisor          documents.SupervisorConfig
}

func run() error {
	if os.Getenv("SYNTHETIC_LOCAL_ONLY") != "true" {
		return errors.New("explicit synthetic profile required")
	}
	f, e := os.Open("/run/config/knowledge-controller.json")
	if e != nil {
		return e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() || s.Size() > 65536 || s.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe controller config")
	}
	var c configuration
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid controller config")
	}
	db, e := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	supervisor, e := documents.NewSupervisor(c.Supervisor)
	if e != nil {
		return e
	}
	bridge := processing.Bridge{Queue: knowledge.NewProcessingQueue(db), Processor: supervisor, WorkerID: c.WorkerID, ProfileID: c.ProfileID, Image: c.Supervisor.Image, DefinitionsHash: c.Supervisor.DefinitionsHash, Lease: 8 * time.Minute}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for ctx.Err() == nil {
		e = bridge.Once(ctx)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			os.Stderr.WriteString("processing claim failed; inspect durable job state\n")
		}
		if os.Getenv("PROCESS_ONCE") == "true" {
			if errors.Is(e, sql.ErrNoRows) {
				return nil
			}
			return e
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}
func main() {
	if run() != nil {
		os.Stderr.WriteString("knowledge controller unavailable\n")
		os.Exit(1)
	}
}
