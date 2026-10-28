package cases

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

type HTTPConfig struct {
	Enabled bool
	// ResolveIntake derives immutable reporting facts from the current approved
	// directory. Browser snapshot fields are never authoritative.
	ResolveIntake func(context.Context, api.Actor) (Classification, error)
	// ResolveCandidate must resolve current same-organization identity and exact grants.
	// The request supplies only a candidate ID, never candidate grants or conflicts.
	ResolveCandidate func(context.Context, api.Actor, string, string) (Principal, error)
	// AuthorizeEmergency must revalidate the live signed actor and a durable,
	// independently approved read-only exception bound to the exact case ID.
	// The domain still requires exact triage scope and rejects every subject alias.
	AuthorizeEmergency func(context.Context, api.Actor, string, string) error
}
type OperatorEventDTO struct {
	Version      uint64    `json:"version"`
	Kind         string    `json:"kind"`
	ActorID      string    `json:"actor_id"`
	Reason       string    `json:"reason"`
	From         State     `json:"from"`
	To           State     `json:"to"`
	CategoryFrom string    `json:"category_from,omitempty"`
	CategoryTo   string    `json:"category_to,omitempty"`
	OccurredAt   time.Time `json:"occurred_at"`
	RecordedAt   time.Time `json:"recorded_at"`
}
type OperatorDTO struct {
	Events       []OperatorEventDTO `json:"events"`
	Participants []ParticipantDTO   `json:"participants"`
	ID           string             `json:"id"`
	UnitID       string             `json:"unit_id"`
	State        State              `json:"state"`
	Version      uint64             `json:"version"`
	Narrative    string             `json:"narrative"`
	Category     string             `json:"category"`
	Snapshot     Classification     `json:"snapshot"`
	Assignments  []AssignmentDTO    `json:"assignments"`
}
type ParticipantDTO struct {
	ID   string          `json:"id"`
	Role ParticipantRole `json:"role"`
}
type AssignmentDTO struct {
	ID   string         `json:"id"`
	Role AssignmentRole `json:"role"`
}

func operatorView(c Case) OperatorDTO {
	v := OperatorDTO{ID: c.ID, UnitID: c.UnitID, State: c.State, Version: c.Version, Narrative: c.Narrative, Category: c.Category, Snapshot: c.Snapshot, Assignments: []AssignmentDTO{}}
	for _, event := range c.Events {
		v.Events = append(v.Events, OperatorEventDTO{Version: event.Version, Kind: event.Kind, ActorID: event.ActorID, Reason: event.Reason, From: event.From, To: event.To, CategoryFrom: event.CategoryFrom, CategoryTo: event.CategoryTo, OccurredAt: event.OccurredAt, RecordedAt: event.RecordedAt})
	}
	for _, link := range c.Participants {
		v.Participants = append(v.Participants, ParticipantDTO{ID: link.ID, Role: link.Role})
	}
	for _, a := range c.Assignments {
		v.Assignments = append(v.Assignments, AssignmentDTO{ID: a.ID, Role: a.Role})
	}
	return v
}
func safeView(p Principal, c Case) any {
	if operatorRead(p, c) {
		return operatorView(c)
	}
	return EmployeeView(c)
}
func caseError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrDenied), errors.Is(e, ErrNotFound):
		api.Error(w, 404, "not_found")
	case errors.Is(e, ErrConflict), errors.Is(e, ErrIdempotencyConflict):
		api.Error(w, 409, "version_conflict")
	case errors.Is(e, ErrInvalid), errors.Is(e, ErrReasonRequired), errors.Is(e, ErrInvalidTransition), errors.Is(e, ErrNoRoute):
		api.Error(w, 400, "invalid_input")
	case errors.Is(e, ErrCommitUnknown):
		api.Error(w, 503, "commit_unknown_retry_same_key")
	default:
		api.Error(w, 503, "persistence_unavailable")
	}
}

// NewHTTPHandler does no DDL. The composition root must explicitly migrate first.
func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler {
	s := Service{Repository: &SQLRepository{DB: db}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Enabled {
			api.Error(w, 403, "unavailable")
			return
		}
		a, e := api.ActorFrom(r.Context())
		if e != nil {
			api.Error(w, 403, "denied")
			return
		}
		p := PrincipalForActor(a)
		path := strings.TrimPrefix(r.URL.Path, "/api/extensions/cases")
		path = strings.Trim(path, "/")
		parts := strings.Split(path, "/")
		if path == "" {
			if r.Method == http.MethodGet {
				list, e := ListCurrent(r.Context(), db, a, r.URL.Query().Get("q"))
				if e != nil {
					caseError(w, e)
					return
				}
				views := []any{}
				for _, c := range list {
					views = append(views, safeView(p, c))
				}
				api.Write(w, 200, views)
				return
			}
			if r.Method != http.MethodPost {
				api.Error(w, 405, "method_not_allowed")
				return
			}
			var in struct {
				UnitID    string         `json:"unit_id"`
				Key       string         `json:"key"`
				Narrative string         `json:"narrative"`
				Snapshot  Classification `json:"snapshot"`
				Draft     bool           `json:"draft"`
			}
			if api.Decode(r, &in) != nil || strings.TrimSpace(in.Narrative) == "" || len(in.Narrative) > 65536 {
				api.Error(w, 400, "invalid_input")
				return
			}
			if !a.Allows(string(SubmitOwn), in.UnitID) {
				api.Error(w, 403, "denied")
				return
			}
			if cfg.ResolveIntake == nil {
				api.Error(w, 503, "intake_policy_unavailable")
				return
			}
			snapshot, err := cfg.ResolveIntake(r.Context(), a)
			if err != nil || snapshot.Zone != in.UnitID || snapshot.Department == "" || snapshot.CaseType == "" {
				api.Error(w, 403, "denied")
				return
			}
			result, e := s.Intake(r.Context(), IntakeRequest{OrganizationID: a.OrganizationID, UnitID: snapshot.Zone, ContactID: a.UserID, Key: in.Key, Narrative: in.Narrative, Snapshot: snapshot, Draft: in.Draft})
			if e != nil {
				caseError(w, e)
				return
			}
			api.Write(w, 201, map[string]any{"id": result.CaseID, "outcome": result.Outcome})
			return
		}
		if path == "facts" && r.Method == http.MethodGet {
			if _, err := api.Require(r, "reports:operational"); err != nil {
				api.Error(w, 403, "denied")
				return
			}
			v, e := ReportFacts(r.Context(), db, a)
			if e != nil {
				caseError(w, e)
				return
			}
			api.Write(w, 200, v)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			c, e := s.Read(r.Context(), p, parts[0])
			if e != nil {
				caseError(w, e)
				return
			}
			api.Write(w, 200, safeView(p, c))
			return
		}
		if len(parts) != 2 {
			api.Error(w, 404, "not_found")
			return
		}
		if parts[1] == "emergency-read" {
			serveEmergencyRead(w, r, s, cfg, a, p, parts[0])
			return
		}
		// All mutation DTOs use one strict schema and optimistic expected_version.
		var in struct {
			ExpectedVersion uint64          `json:"expected_version"`
			State           State           `json:"state"`
			Reason          string          `json:"reason"`
			OccurredAt      time.Time       `json:"occurred_at"`
			Narrative       string          `json:"narrative"`
			Category        string          `json:"category"`
			BackupIDs       []string        `json:"backup_ids"`
			CandidateID     string          `json:"candidate_id"`
			ParticipantRole ParticipantRole `json:"participant_role"`
			Role            AssignmentRole  `json:"role"`
		}
		expectedMethod := http.MethodPost
		if parts[1] == "draft" {
			expectedMethod = http.MethodPut
		}
		if r.Method != expectedMethod {
			api.Error(w, 405, "method_not_allowed")
			return
		}
		if api.Decode(r, &in) != nil || in.ExpectedVersion == 0 {
			api.Error(w, 400, "invalid_input")
			return
		}
		if in.OccurredAt.IsZero() {
			in.OccurredAt = time.Now().UTC()
		}
		var c Case
		switch parts[1] {
		case "state":
			c, e = s.ChangeState(r.Context(), p, parts[0], in.ExpectedVersion, in.State, in.Reason, in.OccurredAt)
		case "draft":
			c, e = s.SaveDraft(r.Context(), p, parts[0], in.ExpectedVersion, in.Narrative, in.OccurredAt)
		case "category":
			c, e = s.CorrectCategory(r.Context(), p, parts[0], in.ExpectedVersion, in.Category, in.Reason, in.OccurredAt)
		case "assignment", "participants":
			// Load and authorize before resolving any candidate or revealing case unit.
			c, e = s.Read(r.Context(), p, parts[0])
			if e != nil {
				caseError(w, e)
				return
			}
			if !canManage(p, c) || cfg.ResolveCandidate == nil {
				api.Error(w, 403, "denied")
				return
			}
			var candidate Principal
			candidate, e = cfg.ResolveCandidate(r.Context(), a, in.CandidateID, c.UnitID)
			if e != nil {
				api.Error(w, 400, "invalid_candidate")
				return
			}
			candidates := []Candidate{{Principal: candidate, Role: in.Role}}
			if len(in.BackupIDs) > 16 {
				api.Error(w, 400, "invalid_input")
				return
			}
			for _, target := range in.BackupIDs {
				backup, err := cfg.ResolveCandidate(r.Context(), a, target, c.UnitID)
				if err != nil {
					api.Error(w, 400, "invalid_candidate")
					return
				}
				candidates = append(candidates, Candidate{Principal: backup, Role: in.Role, Backup: true})
			}
			if parts[1] == "participants" {
				c, e = s.LinkParticipant(r.Context(), p, parts[0], in.ExpectedVersion, candidate, in.ParticipantRole, in.Reason, in.OccurredAt)
			} else {
				c, e = s.Reassign(r.Context(), p, parts[0], in.ExpectedVersion, candidates, in.Reason, in.OccurredAt)
			}
		default:
			api.Error(w, 404, "not_found")
			return
		}
		if e != nil {
			caseError(w, e)
			return
		}
		if !CanRead(p, c) {
			api.Write(w, 200, map[string]bool{"saved": true})
			return
		}
		api.Write(w, 200, safeView(p, c))
	})
}
