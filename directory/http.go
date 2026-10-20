package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// HTTPConfig is trusted host configuration. An invalid clock or freshness denies service.
type HTTPConfig struct {
	Freshness time.Duration
	Clock     func() time.Time
	Worker    *LegacyWorker
}
type employeeDTO struct {
	ID         string `json:"id"`
	Phone      string `json:"phone"`
	Zone       string `json:"zone"`
	Department string `json:"department"`
	Active     bool   `json:"active"`
	Revoked    bool   `json:"revoked,omitempty"`
}
type previewDTO struct {
	SourceSHA256    string        `json:"source_sha256,omitempty"`
	PreviewID       string        `json:"preview_id"`
	Digest          string        `json:"digest"`
	ExpectedVersion uint64        `json:"expected_version"`
	Employees       []employeeDTO `json:"employees"`
	CreatedAt       time.Time     `json:"created_at"`
	CreatedBy       string        `json:"created_by"`
	ApprovedAt      *time.Time    `json:"approved_at,omitempty"`
	ApprovedBy      string        `json:"approved_by,omitempty"`
}
type directoryHTTP struct {
	db  *sql.DB
	cfg HTTPConfig
}

func MigrateHTTP(ctx context.Context, db *sql.DB) error {
	s, e := NewPostgresStore(db, "migration", "migration")
	if e != nil {
		return e
	}
	if e = s.Init(ctx); e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS directory_previews (
 tenant text NOT NULL, id text NOT NULL, digest text NOT NULL, expected_version bigint NOT NULL CHECK(expected_version>=0),
 payload jsonb NOT NULL, created_by text NOT NULL, created_at timestamptz NOT NULL,
 approved_by text, approved_at timestamptz, source_sha256 text NOT NULL DEFAULT '', PRIMARY KEY(tenant,id))`)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(ctx, `ALTER TABLE directory_previews ADD COLUMN IF NOT EXISTS source_sha256 text NOT NULL DEFAULT ''`)
	return e
}
func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler { return &directoryHTTP{db, cfg} }
func (h *directoryHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/extensions/directory/")
	permission := ""
	switch {
	case r.Method == "GET" && (path == "status" || path == "roster"):
		permission = "directory:read"
	case r.Method == "GET" && strings.HasPrefix(path, "preview/"):
		permission = "directory:approve"
	case r.Method == "POST" && (path == "preview" || path == "preview-file"):
		permission = "directory:import"
	case r.Method == "POST" && path == "approve":
		permission = "directory:approve"
	case r.Method == "POST" && path == "revoke":
		permission = "directory:revoke"
	case r.Method == "POST" && path == "validate":
		permission = "directory:read"
	default:
		api.Error(w, 404, "not_found")
		return
	}
	a, e := api.Require(r, permission)
	if e != nil {
		api.Error(w, 403, "denied")
		return
	}
	if h.db == nil || h.cfg.Freshness <= 0 || h.cfg.Clock == nil || h.cfg.Clock().IsZero() {
		api.Error(w, 503, "unavailable")
		return
	}
	store, e := NewPostgresStore(h.db, a.OrganizationID, a.UserID)
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	if r.Method == "GET" && strings.HasPrefix(path, "preview/") {
		h.review(w, r, a, strings.TrimPrefix(path, "preview/"))
		return
	}
	if path == "preview-file" {
		h.upload(w, r, a, store)
		return
	}
	if path == "approve" {
		h.approve(w, r, a, store)
		return
	}
	s, e := store.Load(r.Context())
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	switch path {
	case "status", "roster":
		now := h.cfg.Clock()
		fresh := !s.ImportedAt.IsZero() && !now.Before(s.ImportedAt) && now.Sub(s.ImportedAt) < h.cfg.Freshness
		rows := make([]employeeDTO, 0, len(s.Employees))
		active := 0
		revoked := 0
		for id, em := range s.Employees {
			d := toDTO(em)
			d.Revoked = s.Revoked[id]
			rows = append(rows, d)
			if em.Active && !d.Revoked {
				active++
			}
			if d.Revoked {
				revoked++
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		out := map[string]any{"version": s.Version, "imported_at": s.ImportedAt, "fresh": fresh, "freshness_seconds": int64(h.cfg.Freshness / time.Second), "total": len(rows), "active": active, "revoked": revoked}
		if path == "roster" {
			out["employees"] = rows
		}
		api.Write(w, 200, out)
	case "validate":
		var in struct {
			Phone string `json:"phone"`
		}
		if api.Decode(r, &in) != nil {
			api.Error(w, 400, "invalid_input")
			return
		}
		em, e := Match(s, in.Phone, h.cfg.Clock(), h.cfg.Freshness)
		if e != nil {
			api.Error(w, 403, "ineligible")
			return
		}
		api.Write(w, 200, toDTO(em))
	case "preview":
		var in struct {
			ExpectedVersion *uint64       `json:"expected_version"`
			Employees       []employeeDTO `json:"employees"`
		}
		if api.Decode(r, &in) != nil || in.ExpectedVersion == nil {
			api.Error(w, 400, "invalid_input")
			return
		}
		if *in.ExpectedVersion != s.Version {
			api.Error(w, 409, "version_conflict")
			return
		}
		rows := make([]Employee, len(in.Employees))
		for i, d := range in.Employees {
			if d.Revoked {
				api.Error(w, 400, "invalid_input")
				return
			}
			rows[i] = Employee{ID: d.ID, Phone: d.Phone, Zone: d.Zone, Department: d.Department, Active: d.Active}
		}
		p, e := NewPreview(rows)
		if e != nil {
			api.Error(w, 400, "invalid_roster")
			return
		}
		h.savePreview(w, r, a, p, s.Version, "")

	case "revoke":
		var in struct {
			EmployeeID      string  `json:"employee_id"`
			ExpectedVersion *uint64 `json:"expected_version"`
		}
		if api.Decode(r, &in) != nil || in.ExpectedVersion == nil || strings.TrimSpace(in.EmployeeID) == "" || len(in.EmployeeID) > 256 {
			api.Error(w, 400, "invalid_input")
			return
		}
		if *in.ExpectedVersion != s.Version {
			api.Error(w, 409, "version_conflict")
			return
		}
		if _, ok := s.Employees[in.EmployeeID]; !ok {
			api.Error(w, 404, "not_found")
			return
		}
		n, e := Revoke(s, in.EmployeeID)
		if e == nil {
			e = store.CompareAndSwap(r.Context(), s.Version, n)
		}
		if e != nil {
			writeDirectoryError(w, e)
			return
		}
		api.Write(w, 200, map[string]any{"version": n.Version, "employee_id": in.EmployeeID, "revoked": true})
	}
}
func toDTO(e Employee) employeeDTO {
	return employeeDTO{ID: e.ID, Phone: e.Phone, Zone: e.Zone, Department: e.Department, Active: e.Active}
}
func writeDirectoryError(w http.ResponseWriter, e error) {
	if errors.Is(e, ErrConflict) {
		api.Error(w, 409, "version_conflict")
	} else {
		api.Error(w, 503, "unavailable")
	}
}
func scanPreview(row *sql.Row) (previewDTO, error) {
	var d previewDTO
	var payload []byte
	var version int64
	var approvedBy sql.NullString
	var approvedAt sql.NullTime
	e := row.Scan(&d.PreviewID, &d.Digest, &version, &payload, &d.CreatedBy, &d.CreatedAt, &approvedBy, &approvedAt, &d.SourceSHA256)
	if e != nil {
		return d, e
	}
	if version < 0 {
		return d, errors.New("invalid version")
	}
	d.ExpectedVersion = uint64(version)
	if e = json.Unmarshal(payload, &d.Employees); e != nil {
		return d, e
	}
	d.ApprovedBy = approvedBy.String
	if approvedAt.Valid {
		d.ApprovedAt = &approvedAt.Time
	}
	return d, nil
}

const previewSelect = `SELECT id,digest,expected_version,payload,created_by,created_at,approved_by,approved_at,source_sha256 FROM directory_previews WHERE tenant=$1 AND id=$2`

func (h *directoryHTTP) review(w http.ResponseWriter, r *http.Request, a api.Actor, id string) {
	d, e := scanPreview(h.db.QueryRowContext(r.Context(), previewSelect, a.OrganizationID, id))
	if errors.Is(e, sql.ErrNoRows) {
		api.Error(w, 404, "not_found")
		return
	}
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	api.Write(w, 200, d)
}
func (h *directoryHTTP) approve(w http.ResponseWriter, r *http.Request, a api.Actor, store *PostgresStore) {
	var in struct {
		PreviewID       string  `json:"preview_id"`
		Digest          string  `json:"digest"`
		ExpectedVersion *uint64 `json:"expected_version"`
	}
	if api.Decode(r, &in) != nil || in.ExpectedVersion == nil || in.PreviewID == "" || in.Digest == "" || *in.ExpectedVersion >= math.MaxInt64 {
		api.Error(w, 400, "invalid_input")
		return
	}
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	defer tx.Rollback()
	d, e := scanPreview(tx.QueryRowContext(r.Context(), previewSelect+` FOR UPDATE`, a.OrganizationID, in.PreviewID))
	if errors.Is(e, sql.ErrNoRows) {
		api.Error(w, 404, "not_found")
		return
	}
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	if d.ApprovedAt != nil || d.Digest != in.Digest || d.ExpectedVersion != *in.ExpectedVersion {
		api.Error(w, 409, "approval_conflict")
		return
	}
	rows := make([]Employee, len(d.Employees))
	for i, em := range d.Employees {
		rows[i] = Employee{ID: em.ID, Phone: em.Phone, Zone: em.Zone, Department: em.Department, Active: em.Active}
	}
	p, e := NewPreview(rows)
	if e != nil || p.Digest() != d.Digest {
		api.Error(w, 503, "unavailable")
		return
	}
	// Load through the transaction. CAS below prevents any concurrent import/revoke from being overwritten.
	var payload []byte
	var version int64
	s := Snapshot{}
	e = tx.QueryRowContext(r.Context(), `SELECT version,payload FROM directory_snapshots WHERE tenant=$1`, a.OrganizationID).Scan(&version, &payload)
	if e == nil {
		if json.Unmarshal(payload, &s) != nil || version <= 0 || s.Version != uint64(version) {
			api.Error(w, 503, "unavailable")
			return
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		api.Error(w, 503, "unavailable")
		return
	}
	if s.Version != *in.ExpectedVersion {
		api.Error(w, 409, "version_conflict")
		return
	}
	now := h.cfg.Clock()
	n, e := Replace(s, p, d.Digest, now)
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	if e = store.compareAndSwapTx(r.Context(), tx, s.Version, n); e != nil {
		writeDirectoryError(w, e)
		return
	}
	if _, e = tx.ExecContext(r.Context(), `UPDATE directory_previews SET approved_by=$3,approved_at=$4 WHERE tenant=$1 AND id=$2`, a.OrganizationID, in.PreviewID, a.UserID, now); e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	if e = tx.Commit(); e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	api.Write(w, 200, map[string]any{"version": n.Version, "preview_id": d.PreviewID, "digest": d.Digest, "approved_by": a.UserID, "approved_at": now})
}
