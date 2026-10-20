package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"net/http"
	"strings"
	"time"
)

type HTTPConfig struct {
	Enabled bool
	// RefreshActor must revalidate the original live session, MFA generation and
	// current roles/scopes. Returning a different identity/session is rejected.
	RefreshActor   func(context.Context, api.Actor) (api.Actor, error)
	Adapters       []PhysicalAdapter
	SubjectAliases func(context.Context, string, string) ([]string, error)
}

func MigrateHTTP(ctx context.Context, db *sql.DB) error { return (&SQLRepository{DB: db}).Migrate(ctx) }
func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, e := api.ActorFrom(r.Context())
		if e != nil || !cfg.Enabled || cfg.RefreshActor == nil {
			api.Error(w, 403, "denied")
			return
		}
		current, e := cfg.RefreshActor(r.Context(), a)
		if e != nil || current.OrganizationID != a.OrganizationID || current.UserID != a.UserID || current.SessionID != a.SessionID {
			api.Error(w, 403, "denied")
			return
		}
		if _, e = api.ActorFrom(api.WithActor(r.Context(), current)); e != nil {
			api.Error(w, 403, "denied")
			return
		}
		a = current
		if db == nil {
			api.Error(w, 503, "persistence_unavailable")
			return
		}
		service := Service{Repository: &SQLRepository{DB: db}}
		physical := PhysicalService{DB: db, Adapters: cfg.Adapters, SubjectAliases: cfg.SubjectAliases}
		p := PrincipalForActor(a)
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/extensions/governance"), "/")
		if r.Method == "GET" {
			scope := Scope{a.OrganizationID, r.URL.Query().Get("unit_id")}
			switch path {
			case "inventory":
				out, e := physical.VisibleInventory(r.Context(), a, scope)
				httpResult(w, out, e)
				return
			case "journal":
				out, e := service.Journal(r.Context(), p, scope, r.URL.Query().Get("request_id"))
				httpResult(w, out, e)
				return
			case "export":
				out, e := physical.ExportSubject(r.Context(), a, scope, r.URL.Query().Get("subject_id"))
				if e == nil {
					again, err := cfg.RefreshActor(r.Context(), a)
					_, identityError := api.ActorFrom(api.WithActor(r.Context(), again))
					if err != nil || identityError != nil || again.UserID != a.UserID || again.SessionID != a.SessionID || again.OrganizationID != a.OrganizationID || !again.Allows(string(ExportRequest), scope.UnitID) {
						e = ErrDenied
					} else {
						out, e = physical.ExportSubject(r.Context(), again, scope, r.URL.Query().Get("subject_id"))
					}
				}
				httpResult(w, out, e)
				return
			}
		}
		if r.Method != "POST" {
			api.Error(w, 404, "not_found")
			return
		}
		var in struct {
			UnitID         string          `json:"unit_id"`
			SubjectID      string          `json:"subject_id"`
			RequestID      string          `json:"request_id"`
			Kind           RequestKind     `json:"kind"`
			Reference      string          `json:"reference"`
			Expected       uint64          `json:"expected_version"`
			LineageID      string          `json:"lineage_id"`
			HoldID         string          `json:"hold_id"`
			Active         bool            `json:"active"`
			Source         Source          `json:"source"`
			MinimumSeconds int64           `json:"minimum_retention_seconds"`
			Approved       bool            `json:"approved"`
			RestoreKey     string          `json:"restore_key"`
			RecordID       string          `json:"record_id"`
			Patch          json.RawMessage `json:"patch"`
		}
		if api.Decode(r, &in) != nil {
			api.Error(w, 400, "invalid_input")
			return
		}
		scope := Scope{a.OrganizationID, in.UnitID}
		if !scope.valid() || scope.UnitID == "*" {
			api.Error(w, 400, "invalid_input")
			return
		}
		switch path {
		case "discover":
			out, e := physical.Discover(r.Context(), a, scope, in.SubjectID)
			httpResult(w, out, e)
		case "requests":
			out, e := service.Request(r.Context(), p, SubjectRequest{ID: in.RequestID, Scope: scope, SubjectID: in.SubjectID, Kind: in.Kind, Reference: in.Reference})
			httpResult(w, out, e)
		case "approve":
			out, e := service.ApproveRequest(r.Context(), p, scope, in.RequestID, in.Expected)
			httpResult(w, out, e)
		case "plan":
			out, e := service.PlanDeletion(r.Context(), p, scope, in.RequestID)
			httpResult(w, out, e)
		case "erase":
			out, e := physical.EraseRequest(r.Context(), a, scope, in.RequestID)
			httpResult(w, out, e)
		case "correct":
			e := physical.Correct(r.Context(), a, scope, in.RequestID, in.RecordID, in.Patch)
			httpResult(w, map[string]bool{"saved": e == nil}, e)
		case "hold":
			e := service.SetHold(r.Context(), p, Hold{ID: in.HoldID, Scope: scope, LineageID: in.LineageID, Reference: in.Reference, Active: in.Active})
			httpResult(w, map[string]bool{"saved": e == nil}, e)
		case "rule":
			if in.MinimumSeconds < 0 || in.MinimumSeconds > 315360000 {
				api.Error(w, 400, "invalid_input")
				return
			}
			e := service.SetRule(r.Context(), p, scope, Rule{Source: in.Source, MinimumRetention: time.Duration(in.MinimumSeconds) * time.Second, Approved: in.Approved})
			httpResult(w, map[string]bool{"saved": e == nil}, e)
		case "restore":
			// This endpoint is an OFFLINE restore gate. The independent authoritative
			// journal must already be restored before business data becomes accessible.
			if !validReference(in.RestoreKey) || len(in.RestoreKey) > 128 {
				httpResult(w, nil, ErrInvalid)
				return
			}
			out, e := physical.ReconcileSubjectRestore(r.Context(), a, scope, in.SubjectID, in.RequestID, in.RestoreKey)
			httpResult(w, out, e)
		default:
			api.Error(w, 404, "not_found")
		}
	})
}
func httpResult(w http.ResponseWriter, v any, e error) {
	if e == nil {
		api.Write(w, 200, v)
		return
	}
	switch {
	case errors.Is(e, ErrDenied), errors.Is(e, ErrNotFound):
		api.Error(w, 404, "not_found")
	case errors.Is(e, ErrHold):
		api.Error(w, 409, "blocked_hold")
	case errors.Is(e, ErrRetention):
		api.Error(w, 409, "blocked_retention_or_policy")
	case errors.Is(e, ErrApproval):
		api.Error(w, 409, "approval_required")
	case errors.Is(e, ErrConflict):
		api.Error(w, 409, "conflict")
	case errors.Is(e, ErrInvalid):
		api.Error(w, 400, "invalid_input")
	case errors.Is(e, ErrCommitUnknown):
		api.Error(w, 503, "commit_unknown_reconcile")
	default:
		api.Error(w, 503, "unavailable")
	}
}
