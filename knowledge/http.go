package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

type HTTPConfig struct {
	Enabled bool
	// Authorize resolves current host roles AND exact audience grants for any
	// principal, including the original reviewer at publication time.
	Authorize func(context.Context, string, string, string, string) bool
}
type httpHandler struct {
	db  *sql.DB
	cfg HTTPConfig
}

func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler { return &httpHandler{db: db, cfg: cfg} }

// SynchronizeActor is a trusted host/assisted-answer adapter port. Callers must
// obtain actor from api.ActorFrom, never decode actor/grants from client input.
// Every granted row is intersected with the mandatory current host resolver.
func SynchronizeActor(ctx context.Context, db *sql.DB, cfg HTTPConfig, actor api.Actor) error {
	if db == nil || !cfg.Enabled || cfg.Authorize == nil {
		return ErrDenied
	}
	trusted, err := api.ActorFrom(api.WithActor(ctx, actor))
	if err != nil {
		return ErrDenied
	}
	return (&httpHandler{db: db, cfg: cfg}).syncActor(ctx, trusted)
}
func (h *httpHandler) allowed(ctx context.Context, a api.Actor, p, audience string) bool {
	return a.AudienceAllowed(p, audience) && h.cfg.Authorize != nil && h.cfg.Authorize(ctx, a.OrganizationID, a.UserID, p, audience)
}
func (h *httpHandler) all(ctx context.Context, a api.Actor, p string, m UploadMetadata) bool {
	for _, v := range m.Audiences {
		if !h.allowed(ctx, a, p, v) {
			return false
		}
	}
	return len(m.Audiences) > 0
}
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.cfg.Enabled || h.cfg.Authorize == nil {
		api.Error(w, 403, "module_unavailable")
		return
	}
	if h.db == nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/extensions/knowledge")
	path = strings.Trim(path, "/")
	if path == "uploads" && r.Method == "POST" {
		h.upload(w, r)
		return
	}
	if path == "jobs" && r.Method == "GET" {
		h.list(w, r)
		return
	}
	if path == "search" && r.Method == "GET" {
		h.search(w, r)
		return
	}
	if path == "download" && r.Method == "GET" {
		h.download(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] == "jobs" && r.Method == "GET" {
		h.job(w, r, parts[1], "")
		return
	}
	if len(parts) == 3 && parts[0] == "jobs" && r.Method == "POST" && (parts[2] == "approve" || parts[2] == "publish" || parts[2] == "retire") {
		h.job(w, r, parts[1], parts[2])
		return
	}
	api.Error(w, 404, "not_found")
}
func (h *httpHandler) upload(w http.ResponseWriter, r *http.Request) {
	a, e := api.Require(r, "knowledge:upload")
	if e != nil {
		api.Error(w, 403, "denied")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+(64<<10))
	mr, e := r.MultipartReader()
	if e != nil {
		api.Error(w, 400, "invalid_upload")
		return
	}
	var input []byte
	var m UploadMetadata
	seen := map[string]bool{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			api.Error(w, 400, "invalid_upload")
			return
		}
		name := p.FormName()
		if seen[name] || (name != "file" && name != "metadata") {
			api.Error(w, 400, "invalid_upload")
			return
		}
		seen[name] = true
		limit := int64(MaxUploadBytes)
		if name == "metadata" {
			limit = 32 << 10
		}
		b, err := io.ReadAll(io.LimitReader(p, limit+1))
		p.Close()
		if err != nil || int64(len(b)) > limit {
			api.Error(w, 400, "invalid_upload")
			return
		}
		if name == "file" {
			input = b
		} else {
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.DisallowUnknownFields()
			if dec.Decode(&m) != nil || dec.Decode(new(any)) != io.EOF {
				api.Error(w, 400, "invalid_metadata")
				return
			}
		}
	}
	if !validMetadata(m) || len(input) == 0 {
		api.Error(w, 400, "invalid_upload")
		return
	}
	if !h.all(r.Context(), a, "knowledge:upload", m) {
		api.Error(w, 403, "denied")
		return
	}
	id, e := NewProcessingQueue(h.db).enqueue(r.Context(), a.OrganizationID, a.UserID, input, m)
	if e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	api.Write(w, 202, map[string]any{"id": id, "status": "queued", "version": 1})
}

// syncActor replaces all internal grants for this actor from current, trusted
// host permissions. It cannot be called with client-provided actor/grant data.
func (h *httpHandler) syncActor(ctx context.Context, a api.Actor) error {
	tx, e := h.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, a.OrganizationID+":"+a.UserID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM addon_knowledge_grants WHERE tenant=$1 AND principal=$2`, a.OrganizationID, a.UserID); e != nil {
		return e
	}
	for _, action := range []string{"read", "review", "publish", "retire"} {
		has := false
		for _, g := range a.Grants {
			if g.Permission != "knowledge:"+action || !h.allowed(ctx, a, g.Permission, g.Audience) {
				continue
			}
			scope := "*"
			if action == "read" {
				scope = g.Audience
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO addon_knowledge_grants(tenant,principal,action,scope)VALUES($1,$2,$3,$4)ON CONFLICT DO NOTHING`, a.OrganizationID, a.UserID, action, scope); e != nil {
				return e
			}
			has = true
		}
		_ = has
	}
	return tx.Commit()
}

type JobView struct {
	ID           string         `json:"id"`
	ActorID      string         `json:"actor_id"`
	Status       string         `json:"status"`
	Version      int64          `json:"version"`
	Metadata     UploadMetadata `json:"metadata"`
	ManifestHash string         `json:"manifest_hash,omitempty"`
	Pages        []Page         `json:"pages,omitempty"`
	Failure      string         `json:"failure,omitempty"`
}

func (h *httpHandler) visible(ctx context.Context, a api.Actor, m UploadMetadata, owner string) bool {
	if owner == a.UserID && h.all(ctx, a, "knowledge:upload", m) {
		return true
	}
	for _, p := range []string{"knowledge:review", "knowledge:publish", "knowledge:retire"} {
		if h.all(ctx, a, p, m) {
			return true
		}
	}
	return false
}
func (h *httpHandler) list(w http.ResponseWriter, r *http.Request) {
	a, e := api.ActorFrom(r.Context())
	if e != nil {
		api.Error(w, 403, "denied")
		return
	}
	rows, e := h.db.QueryContext(r.Context(), `SELECT id,actor,status,version,metadata,COALESCE(failure,'') FROM addon_knowledge_jobs WHERE tenant=$1 ORDER BY created_at DESC LIMIT 100`, a.OrganizationID)
	if e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	defer rows.Close()
	out := []JobView{}
	for rows.Next() {
		var v JobView
		var b []byte
		if rows.Scan(&v.ID, &v.ActorID, &v.Status, &v.Version, &b, &v.Failure) != nil || json.Unmarshal(b, &v.Metadata) != nil {
			api.Error(w, 503, "persistence_unavailable")
			return
		}
		if h.visible(r.Context(), a, v.Metadata, v.ActorID) {
			out = append(out, v)
		}
	}
	if rows.Err() != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	api.Write(w, 200, out)
}
func (h *httpHandler) job(w http.ResponseWriter, r *http.Request, id, action string) {
	a, e := api.ActorFrom(r.Context())
	if e != nil {
		api.Error(w, 403, "denied")
		return
	}
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	defer tx.Rollback()
	var v JobView
	var metadata, doc, ev, ap []byte
	e = tx.QueryRowContext(r.Context(), `SELECT id,actor,status,version,metadata,document,evidence,approval,COALESCE(failure,'') FROM addon_knowledge_jobs WHERE tenant=$1 AND id=$2 FOR UPDATE`, a.OrganizationID, id).Scan(&v.ID, &v.ActorID, &v.Status, &v.Version, &metadata, &doc, &ev, &ap, &v.Failure)
	if errors.Is(e, sql.ErrNoRows) {
		api.Error(w, 404, "not_found")
		return
	}
	if e != nil || json.Unmarshal(metadata, &v.Metadata) != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	if !h.visible(r.Context(), a, v.Metadata, v.ActorID) {
		api.Error(w, 404, "not_found")
		return
	}
	var d Document
	var evidence Evidence
	var approval Approval
	if len(doc) > 0 {
		if json.Unmarshal(doc, &d) != nil || json.Unmarshal(ev, &evidence) != nil {
			api.Error(w, 503, "persistence_unavailable")
			return
		}
		v.ManifestHash = ManifestHash(d)
		v.Pages = d.Pages
	}
	if len(ap) > 0 {
		if json.Unmarshal(ap, &approval) != nil {
			api.Error(w, 503, "persistence_unavailable")
			return
		}
	}
	if action == "" {
		api.Write(w, 200, v)
		return
	}
	permission := map[string]string{"approve": "knowledge:review", "publish": "knowledge:publish", "retire": "knowledge:retire"}[action]
	if !h.all(r.Context(), a, permission, v.Metadata) {
		api.Error(w, 403, "denied")
		return
	}
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if api.Decode(r, &body) != nil {
		api.Error(w, 400, "invalid_input")
		return
	}
	if body.ExpectedVersion != v.Version {
		api.Error(w, 409, "version_conflict")
		return
	}
	if e = h.syncActor(r.Context(), a); e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	repo, _ := NewPostgresRepository(h.db, a.OrganizationID)
	repo.tx = tx
	next := ""
	switch action {
	case "approve":
		if v.Status != "processed" {
			api.Error(w, 409, "invalid_state")
			return
		}
		if _, e = Publish(d, evidence, Approval{Reviewer: a.UserID, ManifestHash: ManifestHash(d)}); e == nil {
			approval, e = repo.Approve(r.Context(), a.UserID, d)
		}
		next = "approved"
	case "publish":
		if v.Status != "approved" || approval.Reviewer == a.UserID {
			api.Error(w, 409, "invalid_state")
			return
		}
		for _, aud := range v.Metadata.Audiences {
			if !h.cfg.Authorize(r.Context(), a.OrganizationID, approval.Reviewer, "knowledge:review", aud) {
				api.Error(w, 403, "reviewer_denied")
				return
			}
		}
		// Current host reviewer checks above refresh the internal review grant; the
		// domain independently requires the persisted exact manifest approval.
		_, e = tx.ExecContext(r.Context(), `INSERT INTO addon_knowledge_grants(tenant,principal,action,scope)VALUES($1,$2,'review','*')ON CONFLICT DO NOTHING`, a.OrganizationID, approval.Reviewer)
		if e == nil {
			e = repo.Publish(r.Context(), a.UserID, d, evidence, approval)
		}
		next = "published"
	case "retire":
		if v.Status != "published" {
			api.Error(w, 409, "invalid_state")
			return
		}
		e = repo.Retire(r.Context(), a.UserID, d.ID, d.Version)
		next = "retired"
	}
	if e != nil {
		if errors.Is(e, ErrDenied) {
			api.Error(w, 403, "denied")
		} else if errors.Is(e, ErrPublicationConflict) {
			api.Error(w, 409, "publication_conflict")
		} else {
			api.Error(w, 503, "operation_outcome_unknown")
		}
		return
	}
	approvalJSON, _ := json.Marshal(approval)
	_, e = tx.ExecContext(r.Context(), `UPDATE addon_knowledge_jobs SET status=$3,approval=$4,version=version+1,updated_at=clock_timestamp() WHERE tenant=$1 AND id=$2`, a.OrganizationID, id, next, approvalJSON)
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		api.Error(w, 503, "operation_outcome_unknown")
		return
	}
	v.Status = next
	v.Version++
	api.Write(w, 200, v)
}
func (h *httpHandler) search(w http.ResponseWriter, r *http.Request) {
	a, e := api.Require(r, "knowledge:read")
	aud := r.URL.Query().Get("audience")
	if e != nil || !h.allowed(r.Context(), a, "knowledge:read", aud) {
		api.Error(w, 403, "denied")
		return
	}
	if e = h.syncActor(r.Context(), a); e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	repo, _ := NewPostgresRepository(h.db, a.OrganizationID)
	out, e := repo.Retrieve(r.Context(), Query{Principal: a.UserID, Audience: aud, Text: r.URL.Query().Get("q"), At: time.Now()}, AuthorizeFunc(func(ctx context.Context, p, s string) bool { return h.allowed(ctx, a, "knowledge:read", s) }))
	if e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	api.Write(w, 200, out)
}
func (h *httpHandler) download(w http.ResponseWriter, r *http.Request) {
	a, e := api.Require(r, "knowledge:read")
	aud := r.URL.Query().Get("audience")
	if e != nil || !h.allowed(r.Context(), a, "knowledge:read", aud) {
		api.Error(w, 404, "not_found")
		return
	}
	var b, ev, ap []byte
	var manifest string
	e = h.db.QueryRowContext(r.Context(), `WITH current AS(SELECT DISTINCT ON(document_id) * FROM addon_knowledge_publications WHERE tenant=$1 AND effective_at<=clock_timestamp() ORDER BY document_id,effective_at DESC) SELECT document,evidence,approval,manifest_hash FROM current WHERE document_id=$2 AND version=$3 AND NOT retired AND(withdraw_at IS NULL OR withdraw_at>clock_timestamp()) AND(document->'Audiences') ? $4`, a.OrganizationID, r.URL.Query().Get("document_id"), r.URL.Query().Get("version"), aud).Scan(&b, &ev, &ap, &manifest)
	if errors.Is(e, sql.ErrNoRows) {
		api.Error(w, 404, "not_found")
		return
	}
	if e != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	var d Document
	var evidence Evidence
	var approval Approval
	if json.Unmarshal(b, &d) != nil || json.Unmarshal(ev, &evidence) != nil || json.Unmarshal(ap, &approval) != nil {
		api.Error(w, 503, "persistence_unavailable")
		return
	}
	p, e := Publish(d, evidence, approval)
	if e != nil || p.manifestHash != manifest || !h.allowed(r.Context(), a, "knowledge:read", aud) {
		api.Error(w, 404, "not_found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="source-document"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(d.Content)
}
