package cases

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// PrincipalForActor preserves only current, exact unit grants from the trusted bridge.
func PrincipalForActor(a api.Actor) Principal {
	p := Principal{ID: a.UserID, Aliases: append([]string(nil), a.Aliases...)}
	for _, g := range a.Grants {
		if a.Allows(g.Permission, g.UnitID) {
			p.Grants = append(p.Grants, Grant{OrganizationID: a.OrganizationID, UnitID: g.UnitID, Permission: Permission(g.Permission)})
		}
	}
	return p
}

type EmployeeDTO struct {
	ID      string `json:"id"`
	State   State  `json:"state"`
	Version uint64 `json:"version"`
}

func EmployeeView(c Case) EmployeeDTO {
	return EmployeeDTO{ID: c.ID, State: c.State, Version: c.Version}
}

// Operator edits require assignment work scope or explicit triage scope, and subjects always lose.
func canManage(p Principal, c Case) bool {
	return !operatorConflict(p, c) && (scoped(p, c, AssignTriage) || CanWork(p, c))
}
func (s Service) mutate(ctx context.Context, p Principal, id string, expected uint64, occurred time.Time, reason, kind string, edit func(*Case) error) (Case, error) {
	if s.Repository == nil || occurred.IsZero() {
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
	if subject(p, c) {
		return Case{}, ErrDenied
	}
	next := clone(c)
	if e = edit(&next); e != nil {
		return Case{}, e
	}
	if c.Version != expected {
		return Case{}, ErrConflict
	}
	next.Version++
	next.Events = append(next.Events, Event{ParticipantLinks: append([]Participant(nil), next.Participants...), Kind: kind, CategoryFrom: c.Category, CategoryTo: next.Category, Assignments: append([]Assignment(nil), next.Assignments...), Version: next.Version, ActorID: p.ID, From: c.State, To: next.State, Reason: reason, RecordedAt: time.Now().UTC(), OccurredAt: occurred.UTC(), OrganizationID: c.OrganizationID, UnitID: c.UnitID})
	if e = tx.Save(ctx, next, expected); e != nil {
		return Case{}, e
	}
	outcome, e := tx.Commit(ctx)
	if outcome != Committed {
		if outcome != RolledBack {
			return Case{}, errors.Join(ErrCommitUnknown, e)
		}
		if e == nil {
			e = ErrInvalid
		}
		return Case{}, e
	}
	if e != nil {
		return Case{}, e
	}
	return next, nil
}
func (s Service) CorrectCategory(ctx context.Context, p Principal, id string, v uint64, category, reason string, at time.Time) (Case, error) {
	if strings.TrimSpace(category) == "" || strings.TrimSpace(reason) == "" {
		return Case{}, ErrInvalid
	}
	return s.mutate(ctx, p, id, v, at, "category correction: "+reason, "category_corrected", func(c *Case) error {
		if !canManage(p, *c) {
			return ErrDenied
		}
		c.Category = category
		return nil
	})
}
func (s Service) SaveDraft(ctx context.Context, p Principal, id string, v uint64, narrative string, at time.Time) (Case, error) {
	if strings.TrimSpace(narrative) == "" || len(narrative) > 65536 {
		return Case{}, ErrInvalid
	}
	return s.mutate(ctx, p, id, v, at, "draft edited", "draft_edited", func(c *Case) error {
		if !canManage(p, *c) && !(complainant(p, *c) && scoped(p, *c, SubmitOwn)) {
			return ErrDenied
		}
		if c.State != Draft {
			return ErrInvalidTransition
		}
		c.Narrative = narrative
		return nil
	})
}
func (s Service) Reassign(ctx context.Context, p Principal, id string, v uint64, candidates []Candidate, reason string, at time.Time) (Case, error) {
	if strings.TrimSpace(reason) == "" {
		return Case{}, ErrInvalid
	}
	return s.mutate(ctx, p, id, v, at, "assignment: "+reason, "assignment", func(c *Case) error {
		if !canManage(p, *c) {
			return ErrDenied
		}
		if c.State == Draft || c.State == Resolved || c.State == Closed {
			return ErrInvalidTransition
		}
		a, e := Route(*c, candidates)
		if e != nil {
			return e
		}
		c.Assignments = []Assignment{a}
		if c.State == Submitted {
			c.State = Assigned
		}
		return nil
	})
}

// ListCurrent returns only authorized rows. Text search never searches internal narratives.
func ListCurrent(ctx context.Context, db *sql.DB, a api.Actor, query string) ([]Case, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil {
		return nil, ErrDenied
	}
	rows, e := db.QueryContext(ctx, `SELECT payload FROM addon_cases_case WHERE organization_id=$1 ORDER BY id`, a.OrganizationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Case{}
	p := PrincipalForActor(a)
	query = strings.ToLower(query)
	for rows.Next() {
		var payload []byte
		var c Case
		if e = rows.Scan(&payload); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(payload, &c); e != nil {
			return nil, e
		}
		if !CanRead(p, c) {
			continue
		}
		search := c.ID + " " + string(c.State)
		if operatorRead(p, c) {
			search += " " + c.Category
		}
		if query != "" && !strings.Contains(strings.ToLower(search), query) {
			continue
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// ReportFact contains event-time lifecycle and immutable creation dimensions only.
// Authorization is checked at delivery against current case scope and subject links.
type ReportFact struct {
	Kind           string         `json:"kind"`
	CaseID         string         `json:"case_id"`
	Version        uint64         `json:"version"`
	OrganizationID string         `json:"organization_id"`
	UnitID         string         `json:"unit_id"`
	Snapshot       Classification `json:"snapshot"`
	From           State          `json:"from"`
	To             State          `json:"to"`
	RecordedAt     time.Time      `json:"recorded_at"`
	OccurredAt     time.Time      `json:"occurred_at"`
}

func ReportFacts(ctx context.Context, db *sql.DB, a api.Actor) ([]ReportFact, error) {
	list, e := ListCurrent(ctx, db, a, "")
	if e != nil {
		return nil, e
	}
	facts := []ReportFact{}
	for _, c := range list {
		p := PrincipalForActor(a)
		if !operatorRead(p, c) {
			continue
		}
		for _, event := range c.Events {
			facts = append(facts, ReportFact{Kind: event.Kind, CaseID: c.ID, Version: event.Version, OrganizationID: event.OrganizationID, UnitID: event.UnitID, Snapshot: c.Snapshot, From: event.From, To: event.To, RecordedAt: event.RecordedAt, OccurredAt: event.OccurredAt})
		}
	}
	return facts, nil
}

// LinkParticipant records a verified identity conflict fact. A newly linked subject
// loses any current assignment immediately; participant links never grant access.
func (s Service) LinkParticipant(ctx context.Context, p Principal, id string, v uint64, verified Principal, role ParticipantRole, reason string, at time.Time) (Case, error) {
	if verified.ID == "" || (role != Subject && role != ParticipantSupervisor) || strings.TrimSpace(reason) == "" {
		return Case{}, ErrInvalid
	}
	return s.mutate(ctx, p, id, v, at, "participant linked: "+reason, "participant_linked", func(c *Case) error {
		if !canManage(p, *c) {
			return ErrDenied
		}
		for _, link := range c.Participants {
			if link.ID == verified.ID && link.Role == role {
				return ErrConflict
			}
		}
		c.Participants = append(c.Participants, Participant{ID: verified.ID, Role: role})
		if role == Subject {
			kept := []Assignment{}
			for _, assignment := range c.Assignments {
				if assignment.ID != verified.ID {
					kept = append(kept, assignment)
				}
			}
			c.Assignments = kept
		}
		return nil
	})
}

// AggregateFacts is a server-only reporting boundary. Its caller must apply
// aggregate suppression before delivering any result; it is never an HTTP route.
// Case keys are one-way opaque digests, and no people or narrative fields leave
// this function. Exact current reports:aggregate unit grants are independent of
// operator work access and do not grant direct case or raw event access.
func AggregateFacts(ctx context.Context, db *sql.DB, a api.Actor) ([]ReportFact, error) {
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil || !a.Has("reports:aggregate") {
		return nil, ErrDenied
	}
	if db == nil {
		return nil, ErrInvalid
	}
	rows, e := db.QueryContext(ctx, `SELECT payload FROM addon_cases_case WHERE organization_id=$1 ORDER BY id`, a.OrganizationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	facts := []ReportFact{}
	p := PrincipalForActor(a)
	for rows.Next() {
		var payload []byte
		var c Case
		if e = rows.Scan(&payload); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(payload, &c); e != nil {
			return nil, e
		}
		if !a.Allows("reports:aggregate", c.UnitID) || subject(p, c) {
			continue
		}
		opaque := sha256.Sum256([]byte(c.OrganizationID + "\x00" + c.ID))
		key := hex.EncodeToString(opaque[:])
		for _, event := range c.Events {
			facts = append(facts, ReportFact{CaseID: key, Kind: event.Kind, Version: event.Version, OrganizationID: event.OrganizationID, UnitID: event.UnitID, Snapshot: c.Snapshot, From: event.From, To: event.To, RecordedAt: event.RecordedAt, OccurredAt: event.OccurredAt})
		}
	}
	return facts, rows.Err()
}
