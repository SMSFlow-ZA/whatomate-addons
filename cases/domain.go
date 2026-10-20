// Package cases implements a host-independent protected case domain. Claims and
// participant identity links must be authenticated by the host before use.
package cases

import (
	"errors"
	"strings"
	"time"
)

type State string

const (
	Draft        State = "Draft"
	Submitted    State = "Submitted"
	Assigned     State = "Assigned"
	InProgress   State = "InProgress"
	AwaitingInfo State = "AwaitingInfo"
	Escalated    State = "Escalated"
	Resolved     State = "Resolved"
	Closed       State = "Closed"
)

type ParticipantRole string

const (
	Complainant           ParticipantRole = "complainant"
	Subject               ParticipantRole = "subject"
	ParticipantSupervisor ParticipantRole = "supervisor"
)

type AssignmentRole string

const (
	Handler    AssignmentRole = "handler"
	Supervisor AssignmentRole = "supervisor"
)

type Permission string

const (
	WorkAssigned Permission = "cases:work.assigned"
	ReadOwn      Permission = "cases:read.own"
	SubmitOwn    Permission = "cases:submit.own"
	ReadTriage   Permission = "cases:read.triage"
	AssignTriage Permission = "cases:assign.triage"
)

type Participant struct {
	ID   string
	Role ParticipantRole
}
type Assignment struct {
	ID   string
	Role AssignmentRole
}
type Grant struct {
	OrganizationID, UnitID string
	Permission             Permission
}
type Principal struct {
	ID      string
	Grants  []Grant
	Aliases []string `json:"aliases,omitempty"`
}

func (p Principal) matches(id string) bool {
	if p.ID == id && id != "" {
		return true
	}
	for _, alias := range p.Aliases {
		if alias != "" && alias == id {
			return true
		}
	}
	return false
}

type Event struct {
	ParticipantLinks         []Participant
	Kind                     string
	CategoryFrom, CategoryTo string
	Assignments              []Assignment
	Version                  uint64
	ActorID                  string
	From, To                 State
	Reason                   string
	RecordedAt, OccurredAt   time.Time
	OrganizationID, UnitID   string
}
type Classification struct {
	Zone       string `json:"zone"`
	Department string `json:"department"`
	CaseType   string `json:"case_type"`
}

type Case struct {
	Snapshot            Classification
	Narrative, Category string

	ID, OrganizationID, UnitID, ContactID string
	State                                 State
	Version                               uint64
	Participants                          []Participant
	Assignments                           []Assignment
	ReopenReason                          string
	Events                                []Event
}

var (
	ErrDenied              = errors.New("case access denied")
	ErrConflict            = errors.New("case version conflict")
	ErrInvalidTransition   = errors.New("invalid case transition")
	ErrReasonRequired      = errors.New("reopen reason required")
	ErrNoRoute             = errors.New("no eligible conflict-free routing candidate")
	ErrInvalid             = errors.New("invalid case input")
	ErrNotFound            = errors.New("case not found")
	ErrIdempotencyConflict = errors.New("idempotency key used for different intake")
	ErrCommitUnknown       = errors.New("transaction commit outcome unknown; retry same intake key")
)

func scoped(p Principal, c Case, permission Permission) bool {
	if p.ID == "" || c.OrganizationID == "" || c.UnitID == "" {
		return false
	}
	for _, g := range p.Grants {
		if g.OrganizationID == c.OrganizationID && g.UnitID == c.UnitID && g.Permission == permission {
			return true
		}
	}
	return false
}
func subject(p Principal, c Case) bool {
	for _, v := range c.Participants {
		if p.matches(v.ID) && v.Role == Subject {
			return true
		}
	}
	return false
}
func assigned(p Principal, c Case) bool {
	for _, a := range c.Assignments {
		if p.matches(a.ID) && (a.Role == Handler || a.Role == Supervisor) {
			return true
		}
	}
	return false
}
func complainant(p Principal, c Case) bool {
	for _, v := range c.Participants {
		if p.matches(v.ID) && v.Role == Complainant {
			return true
		}
	}
	return false
}
func CanRead(p Principal, c Case) bool {
	if subject(p, c) {
		return false
	}
	return (assigned(p, c) && scoped(p, c, WorkAssigned)) || (len(c.Assignments) == 0 && scoped(p, c, ReadTriage)) || (complainant(p, c) && scoped(p, c, ReadOwn))
}
func Transition(c Case, p Principal, expected uint64, to State, reason string, recorded, occurred time.Time) (Case, error) {
	if subject(p, c) {
		return Case{}, ErrDenied
	}
	ownSubmit := c.State == Draft && to == Submitted && complainant(p, c) && scoped(p, c, SubmitOwn)
	if !ownSubmit && !(assigned(p, c) && scoped(p, c, WorkAssigned)) {
		return Case{}, ErrDenied
	}
	if c.Version != expected {
		return Case{}, ErrConflict
	}
	if recorded.IsZero() || occurred.IsZero() {
		return Case{}, ErrInvalid
	}
	allowed := false
	switch c.State {
	case Draft:
		allowed = to == Submitted
	case Submitted:
		allowed = to == Assigned && len(c.Assignments) > 0
	case Assigned:
		allowed = to == InProgress || to == Escalated
	case InProgress:
		allowed = to == AwaitingInfo || to == Escalated || to == Resolved
	case AwaitingInfo:
		allowed = to == InProgress || to == Escalated || to == Resolved
	case Escalated:
		allowed = to == InProgress || to == AwaitingInfo || to == Resolved
	case Resolved:
		allowed = to == Closed || to == InProgress
	case Closed:
		allowed = to == InProgress
	}
	if !allowed {
		return Case{}, ErrInvalidTransition
	}
	if (c.State == Resolved || c.State == Closed) && to == InProgress && strings.TrimSpace(reason) == "" {
		return Case{}, ErrReasonRequired
	}
	next := clone(c)
	next.Version++
	next.State = to
	if (c.State == Resolved || c.State == Closed) && to == InProgress {
		next.ReopenReason = reason
	}
	next.Events = append(next.Events, Event{Kind: "lifecycle", Version: next.Version, ActorID: p.ID, From: c.State, To: to, Reason: reason, RecordedAt: recorded.UTC(), OccurredAt: occurred.UTC(), OrganizationID: c.OrganizationID, UnitID: c.UnitID})
	return next, nil
}

type Candidate struct {
	Principal        Principal
	Role             AssignmentRole
	Conflict, Backup bool
}

func Route(c Case, candidates []Candidate) (Assignment, error) {
	for _, backup := range []bool{false, true} {
		for _, v := range candidates {
			if v.Backup == backup && !v.Conflict && !subject(v.Principal, c) && scoped(v.Principal, c, WorkAssigned) && (v.Role == Handler || v.Role == Supervisor) {
				return Assignment{ID: v.Principal.ID, Role: v.Role}, nil
			}
		}
	}
	return Assignment{}, ErrNoRoute
}
func clone(c Case) Case {
	c.Participants = append([]Participant(nil), c.Participants...)
	c.Assignments = append([]Assignment(nil), c.Assignments...)
	c.Events = append([]Event(nil), c.Events...)
	for i := range c.Events {
		c.Events[i].ParticipantLinks = append([]Participant(nil), c.Events[i].ParticipantLinks...)
		c.Events[i].Assignments = append([]Assignment(nil), c.Events[i].Assignments...)
	}
	return c
}
