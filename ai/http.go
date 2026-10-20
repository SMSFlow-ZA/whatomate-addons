package ai

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

type HTTPConfig struct {
	Classifier Classifier
	Labels     []string
	Enabled    bool
	ServiceFor func(context.Context, api.Actor) (Service, error)
	// Authorize must reread the active session, current role, and exact audience grant.
	Authorize func(context.Context, api.Actor, string, string) bool
	// Cases rereads the actual case and current work scope. Unknown and denied are equivalent.
	Cases func(context.Context, api.Actor, string, string) bool
}
type Record struct {
	Classification *ClassificationSuggestion `json:"classification,omitempty"`
	ID             string                    `json:"id"`
	CaseID         string                    `json:"case_id"`
	Audience       string                    `json:"audience"`
	Kind           string                    `json:"kind"`
	Question       string                    `json:"question"`
	Result         Result                    `json:"result"`
	Version        int64                     `json:"version"`
	Status         string                    `json:"status"`
	Correction     string                    `json:"correction,omitempty"`
	ActorID        string                    `json:"actor_id"`
	UpdatedAt      time.Time                 `json:"updated_at"`
}
type SQLStore struct{ DB *sql.DB }

func (s SQLStore) Migrate(ctx context.Context) error {
	if s.DB == nil {
		return api.ErrDenied
	}
	_, e := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_ai_records(organization_id text NOT NULL,id text NOT NULL,case_id text NOT NULL,audience text NOT NULL,version bigint NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(organization_id,id)); CREATE TABLE IF NOT EXISTS addon_ai_audit(organization_id text NOT NULL,record_id text NOT NULL,version bigint NOT NULL,actor_id text NOT NULL,kind text NOT NULL,recorded_at timestamptz NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(organization_id,record_id,version))`)
	return e
}
func (s SQLStore) save(ctx context.Context, a api.Actor, v Record, expected int64) error {
	if s.DB == nil {
		return api.ErrDenied
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if expected == 0 {
		_, e = tx.ExecContext(ctx, `INSERT INTO addon_ai_records VALUES($1,$2,$3,$4,$5,$6)`, a.OrganizationID, v.ID, v.CaseID, v.Audience, v.Version, b)
	} else {
		var result sql.Result
		result, e = tx.ExecContext(ctx, `UPDATE addon_ai_records SET version=$3,payload=$4 WHERE organization_id=$1 AND id=$2 AND version=$5`, a.OrganizationID, v.ID, v.Version, b, expected)
		if e == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				return errVersion
			}
		}
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_ai_audit VALUES($1,$2,$3,$4,$5,$6,$7)`, a.OrganizationID, v.ID, v.Version, a.UserID, v.Status, v.UpdatedAt, b)
	if e != nil {
		return e
	}
	return tx.Commit()
}

var errVersion = &versionError{}

type versionError struct{}

func (*versionError) Error() string { return "version conflict" }
func (s SQLStore) load(ctx context.Context, a api.Actor, id string) (Record, error) {
	var v Record
	var b []byte
	if s.DB == nil {
		return v, api.ErrDenied
	}
	e := s.DB.QueryRowContext(ctx, `SELECT payload FROM addon_ai_records WHERE organization_id=$1 AND id=$2`, a.OrganizationID, id).Scan(&b)
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	return v, e
}
func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler {
	store := SQLStore{db}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, e := api.Require(r, "ai:assist")
		if e != nil || !cfg.Enabled || cfg.Authorize == nil || cfg.Cases == nil || cfg.ServiceFor == nil {
			api.Error(w, 403, "denied")
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/extensions/ai"), "/")
		parts := strings.Split(path, "/")
		allowed := func(v Record) bool {
			return a.AudienceAllowed("knowledge:read", v.Audience) && cfg.Authorize(r.Context(), a, "ai:assist", v.Audience) && cfg.Cases(r.Context(), a, v.CaseID, "cases:work.assigned")
		}
		if (path == "answers" || path == "suggestions") && r.Method == http.MethodPost {
			var in struct {
				Question string `json:"question"`
				Audience string `json:"audience"`
				CaseID   string `json:"case_id"`
			}
			if api.Decode(r, &in) != nil || strings.TrimSpace(in.Question) == "" || len(in.Question) > 4096 || in.CaseID == "" || len(in.CaseID) > 200 || len(in.Audience) > 200 {
				api.Error(w, 400, "invalid_input")
				return
			}
			v := Record{CaseID: in.CaseID, Audience: in.Audience, Question: in.Question, Kind: "answer", Version: 1, ActorID: a.UserID, UpdatedAt: time.Now().UTC()}
			if !allowed(v) {
				api.Error(w, 404, "not_found")
				return
			}
			service, e := cfg.ServiceFor(r.Context(), a)
			if e != nil {
				api.Error(w, 503, "unavailable")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
			defer cancel()
			if path == "suggestions" {
				if cfg.Classifier == nil {
					api.Error(w, 503, "unavailable")
					return
				}
				suggestion, err := cfg.Classifier.Suggest(ctx, in.Question, cfg.Labels)
				if err != nil {
					v.Result = fallback()
				} else {
					v.Classification = &suggestion
					v.Kind = "classification"
				}
			} else {
				v.Result = service.Answer(ctx, a.UserID, in.Audience, in.Question)
			}
			if !allowed(v) {
				api.Error(w, 403, "denied")
				return
			}
			v.Status = "suggested"
			if v.Result.Handoff {
				v.Status = "handoff_requested"
			}
			var id [16]byte
			if _, e = rand.Read(id[:]); e != nil {
				api.Error(w, 503, "unavailable")
				return
			}
			v.ID = hex.EncodeToString(id[:])
			if store.save(r.Context(), a, v, 0) != nil {
				api.Error(w, 503, "persistence_unavailable")
				return
			}
			api.Write(w, 201, v)
			return
		}
		if len(parts) < 2 || parts[0] != "records" {
			api.Error(w, 404, "not_found")
			return
		}
		v, e := store.load(r.Context(), a, parts[1])
		if e != nil || !allowed(v) {
			api.Error(w, 404, "not_found")
			return
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			api.Write(w, 200, v)
			return
		}
		if len(parts) != 3 || r.Method != http.MethodPost || (parts[2] != "correct" && parts[2] != "handoff") {
			api.Error(w, 404, "not_found")
			return
		}
		var in struct {
			ExpectedVersion int64  `json:"expected_version"`
			Reason          string `json:"reason"`
		}
		if api.Decode(r, &in) != nil || in.ExpectedVersion < 1 || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
			api.Error(w, 400, "invalid_input")
			return
		}
		if !a.Has("ai:review") || !cfg.Authorize(r.Context(), a, "ai:review", v.Audience) || !cfg.Cases(r.Context(), a, v.CaseID, "cases:work.assigned") {
			api.Error(w, 404, "not_found")
			return
		}
		if v.Version != in.ExpectedVersion {
			api.Error(w, 409, "version_conflict")
			return
		}
		v.Version++
		v.ActorID = a.UserID
		v.UpdatedAt = time.Now().UTC()
		v.Correction = in.Reason
		v.Status = "human_corrected"
		if parts[2] == "handoff" {
			v.Status = "handoff_requested"
		}
		if !allowed(v) {
			api.Error(w, 403, "denied")
			return
		}
		if !cfg.Authorize(r.Context(), a, "ai:review", v.Audience) {
			api.Error(w, 403, "denied")
			return
		}
		if e = store.save(r.Context(), a, v, in.ExpectedVersion); e != nil {
			if e == errVersion {
				api.Error(w, 409, "version_conflict")
			} else {
				api.Error(w, 503, "persistence_unavailable")
			}
			return
		}
		api.Write(w, 200, v)
	})
}
