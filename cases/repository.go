package cases

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type CommitOutcome string

const (
	Committed  CommitOutcome = "committed"
	RolledBack CommitOutcome = "rolled_back"
	Unknown    CommitOutcome = "unknown"
)

// Repository implementations MUST persist cases, dedupe records and outbox in
// one transaction. Begin and transactions must honor cancellation. No host core
// tables may be modified. See docs/cases.md for isolation/commit requirements.
type Repository interface {
	Begin(context.Context) (Transaction, error)
}
type Transaction interface {
	// Intake serializes duplicate keys within an organization. A key with different
	// fingerprint fails. The returned ID is provisional until Commit confirms it.
	Intake(context.Context, string, string, string, Case, OutboxMessage) (string, error)
	Load(context.Context, string) (Case, error)
	// Save atomically compares the persisted version, including assignment changes.
	Save(context.Context, Case, uint64) error
	Commit(context.Context) (CommitOutcome, error)
	Rollback(context.Context) error
}
type OutboxMessage struct {
	ID, OrganizationID, CaseID, ContactID, Kind string
	CreatedAt                                   time.Time
}
type IntakeRequest struct {
	OrganizationID, UnitID, ContactID, Key string
	Participants                           []Participant
}
type IntakeResult struct {
	CaseID  string
	Outcome CommitOutcome
}
type Service struct{ Repository Repository }

func randomID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}
func (s Service) Intake(ctx context.Context, req IntakeRequest) (IntakeResult, error) {
	if s.Repository == nil || req.OrganizationID == "" || req.UnitID == "" || req.ContactID == "" || req.Key == "" {
		return IntakeResult{Outcome: RolledBack}, ErrInvalid
	}
	for _, participant := range req.Participants {
		if participant.ID == "" || (participant.Role != Complainant && participant.Role != Subject && participant.Role != ParticipantSupervisor) {
			return IntakeResult{Outcome: RolledBack}, ErrInvalid
		}
	}
	id, err := randomID()
	if err != nil {
		return IntakeResult{Outcome: RolledBack}, err
	}
	payload, _ := json.Marshal(req)
	hash := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(hash[:])
	now := time.Now().UTC()
	c := Case{ID: id, OrganizationID: req.OrganizationID, UnitID: req.UnitID, ContactID: req.ContactID, State: Submitted, Version: 1, Participants: []Participant{{ID: req.ContactID, Role: Complainant}}, Events: []Event{{Version: 1, ActorID: req.ContactID, From: Draft, To: Submitted, RecordedAt: now, OccurredAt: now, OrganizationID: req.OrganizationID, UnitID: req.UnitID}}}
	c.Participants = append(c.Participants, req.Participants...)
	tx, err := s.Repository.Begin(ctx)
	if err != nil {
		return IntakeResult{Outcome: RolledBack}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	id, err = tx.Intake(ctx, req.OrganizationID, req.Key, fingerprint, c, OutboxMessage{ID: id + "-ack", OrganizationID: req.OrganizationID, CaseID: id, ContactID: req.ContactID, Kind: "case.intake.acknowledgement", CreatedAt: now})
	if err != nil {
		return IntakeResult{Outcome: RolledBack}, err
	}
	outcome, err := tx.Commit(ctx)
	if outcome != Committed {
		if outcome != RolledBack {
			outcome = Unknown
			err = errors.Join(ErrCommitUnknown, err)
		}
		if err == nil {
			err = fmt.Errorf("case transaction %s", outcome)
		}
		return IntakeResult{Outcome: outcome}, err
	}
	// Committed is authoritative even if a driver supplies an auxiliary error.
	if err != nil {
		return IntakeResult{Outcome: Committed}, err
	}
	return IntakeResult{CaseID: id, Outcome: Committed}, nil
}
func (s Service) ChangeState(ctx context.Context, p Principal, id string, expected uint64, to State, reason string, occurred time.Time) (Case, error) {
	if s.Repository == nil {
		return Case{}, ErrInvalid
	}
	tx, err := s.Repository.Begin(ctx)
	if err != nil {
		return Case{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	c, err := tx.Load(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Case{}, ErrDenied
	}
	if err != nil {
		return Case{}, err
	}
	next, err := Transition(c, p, expected, to, reason, time.Now().UTC(), occurred)
	if err != nil {
		return Case{}, err
	}
	if err = tx.Save(ctx, next, expected); err != nil {
		return Case{}, err
	}
	outcome, err := tx.Commit(ctx)
	if outcome != Committed {
		if outcome != RolledBack {
			return Case{}, errors.Join(ErrCommitUnknown, err)
		}
		if err == nil {
			err = errors.New("case transaction rolled back")
		}
		return Case{}, err
	}
	if err != nil {
		return Case{}, err
	}
	return next, nil
}

// Read enforces the same policy as commands. Missing and denied IDs are both
// reported as denied so the service does not disclose protected case existence.
func (s Service) Read(ctx context.Context, p Principal, id string) (Case, error) {
	if s.Repository == nil {
		return Case{}, ErrInvalid
	}
	tx, e := s.Repository.Begin(ctx)
	if e != nil {
		return Case{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	c, e := tx.Load(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return Case{}, ErrDenied
	}
	if e != nil {
		return Case{}, e
	}
	if !CanRead(p, c) {
		return Case{}, ErrDenied
	}
	return c, nil
}

// Assign routes an unassigned submitted case using explicit triage authority.
// Reassignment is intentionally unsupported until a separate policy is supplied.
func (s Service) Assign(ctx context.Context, p Principal, id string, expected uint64, candidates []Candidate, occurred time.Time) (Case, error) {
	if s.Repository == nil {
		return Case{}, ErrInvalid
	}
	tx, e := s.Repository.Begin(ctx)
	if e != nil {
		return Case{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	c, e := tx.Load(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return Case{}, ErrDenied
	}
	if e != nil {
		return Case{}, e
	}
	if subject(p, c) || len(c.Assignments) != 0 || !scoped(p, c, AssignTriage) {
		return Case{}, ErrDenied
	}
	if c.Version != expected {
		return Case{}, ErrConflict
	}
	if c.State != Submitted {
		return Case{}, ErrInvalidTransition
	}
	if occurred.IsZero() {
		return Case{}, ErrInvalid
	}
	a, e := Route(c, candidates)
	if e != nil {
		return Case{}, e
	}
	next := clone(c)
	next.Assignments = []Assignment{a}
	next.State = Assigned
	next.Version++
	next.Events = append(next.Events, Event{Version: next.Version, ActorID: p.ID, From: c.State, To: Assigned, Reason: "assigned to " + a.ID, RecordedAt: time.Now().UTC(), OccurredAt: occurred.UTC(), OrganizationID: c.OrganizationID, UnitID: c.UnitID})
	if e = tx.Save(ctx, next, expected); e != nil {
		return Case{}, e
	}
	outcome, e := tx.Commit(ctx)
	if outcome != Committed {
		if outcome != RolledBack {
			return Case{}, errors.Join(ErrCommitUnknown, e)
		}
		if e == nil {
			e = errors.New("case transaction rolled back")
		}
		return Case{}, e
	}
	if e != nil {
		return Case{}, e
	}
	return next, nil
}
