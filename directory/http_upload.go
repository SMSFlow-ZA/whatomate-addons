package directory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func (h *directoryHTTP) savePreview(w http.ResponseWriter, r *http.Request, a api.Actor, p Preview, version uint64, source string, metadata ...*ImportMetadata) {
	var random [16]byte
	if _, e := rand.Read(random[:]); e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	dto := previewDTO{PreviewID: hex.EncodeToString(random[:]), Digest: p.Digest(), ExpectedVersion: version, CreatedBy: a.UserID, CreatedAt: h.cfg.Clock(), SourceSHA256: source, Employees: make([]employeeDTO, 0, len(p.employees))}
	if len(metadata) > 0 {
		dto.Import = metadata[0]
		if dto.Import != nil && dto.Import.RequestID != "" {
			dto.PreviewID = dto.Import.RequestID
		}
		dto.Digest = importDigest(p, dto.Import)
	}
	for _, em := range p.Employees() {
		dto.Employees = append(dto.Employees, toDTO(em))
	}
	payload, _ := json.Marshal(dto.Employees)
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	defer tx.Rollback()
	ids := make([]string, len(dto.Employees))
	for i, em := range dto.Employees {
		ids[i] = em.ID
	}
	if h.cfg.AuthorizeAccess != nil && h.cfg.AuthorizeAccess(r.Context(), tx, a, "directory:import", ids) != nil {
		api.Error(w, 403, "denied")
		return
	}
	var current uint64
	e = tx.QueryRowContext(r.Context(), `SELECT COALESCE((SELECT version FROM directory_snapshots WHERE tenant=$1),0)`, a.OrganizationID).Scan(&current)
	if e != nil || current != version {
		api.Error(w, 409, "version_conflict")
		return
	}
	meta, _ := json.Marshal(dto.Import)
	_, e = tx.ExecContext(r.Context(), `INSERT INTO directory_previews(tenant,id,digest,expected_version,payload,created_by,created_at,source_sha256,import_metadata) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9::jsonb) ON CONFLICT(tenant,id) DO NOTHING`, a.OrganizationID, dto.PreviewID, dto.Digest, int64(version), string(payload), a.UserID, dto.CreatedAt, source, string(meta))
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	saved, readErr := scanPreview(tx.QueryRowContext(r.Context(), previewSelect, a.OrganizationID, dto.PreviewID))
	if readErr != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	if saved.Digest != dto.Digest || saved.CreatedBy != a.UserID {
		api.Error(w, 409, "idempotency_conflict")
		return
	}
	if e = tx.Commit(); e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	api.Write(w, 201, saved)
}

type workbookMapping struct {
	FirstName  *int `json:"first_name,omitempty"`
	LastName   *int `json:"last_name,omitempty"`
	EmployeeID int  `json:"employee_id"`
	Phone      int  `json:"phone"`
	Active     int  `json:"active"`
	Zone       int  `json:"zone"`
	Department int  `json:"department"`
}

func (h *directoryHTTP) upload(w http.ResponseWriter, r *http.Request, a api.Actor, store *PostgresStore) {
	// Stream multipart without writing attacker-controlled uploads into temporary files.
	r.Body = http.MaxBytesReader(w, r.Body, h.importLimits().MaxFileBytes+(128<<10))
	reader, e := r.MultipartReader()
	if e != nil {
		api.Error(w, 400, "invalid_upload")
		return
	}
	metadataLimit := int64(8192)
	if h.cfg.ImportDefaultMode != "" {
		metadataLimit = 64 << 10
	}
	var data, metadata []byte
	filename := ""
	haveFile, haveMetadata := false, false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			api.Error(w, 400, "invalid_upload")
			return
		}
		switch part.FormName() {
		case "metadata":
			if haveMetadata {
				api.Error(w, 400, "invalid_upload")
				return
			}
			haveMetadata = true
			metadata, e = io.ReadAll(io.LimitReader(part, metadataLimit+1))
			if e != nil || int64(len(metadata)) > metadataLimit {
				api.Error(w, 400, "invalid_upload")
				return
			}
		case "file":
			if haveFile {
				api.Error(w, 400, "invalid_upload")
				return
			}
			haveFile = true
			filename = part.FileName()
			data, e = io.ReadAll(io.LimitReader(part, h.importLimits().MaxFileBytes+1))
			if e != nil || len(data) == 0 || int64(len(data)) > h.importLimits().MaxFileBytes {
				api.Error(w, 400, "invalid_upload")
				return
			}
		default:
			api.Error(w, 400, "invalid_upload")
			return
		}
		part.Close()
	}
	var in struct {
		RequestID       string           `json:"request_id"`
		Mode            string           `json:"mode"`
		Selected        *[]int           `json:"selected_rows"`
		ExpectedVersion *uint64          `json:"expected_version"`
		Worksheet       string           `json:"worksheet"`
		Mapping         *workbookMapping `json:"mapping"`
		SkipHeader      *bool            `json:"skip_header"`
	}
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(metadata))
	if !haveFile || !haveMetadata || api.Decode(request, &in) != nil || in.ExpectedVersion == nil || in.Mapping == nil || in.SkipHeader == nil || in.Worksheet == "" {
		api.Error(w, 400, "invalid_input")
		return
	}
	s, e := store.Load(r.Context())
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	if s.Version != *in.ExpectedVersion {
		api.Error(w, 409, "version_conflict")
		return
	}
	var rows [][]string
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx":
		rows, e = ReadXLSX(bytes.NewReader(data), int64(len(data)), in.Worksheet, h.importLimits())
	case ".xls":
		if h.cfg.Worker == nil {
			api.Error(w, 400, "legacy_xls_unconfigured")
			return
		}
		rows, e = h.cfg.Worker.Read(r.Context(), data, in.Worksheet, h.importLimits())
	default:
		api.Error(w, 400, "unsupported_workbook")
		return
	}
	if e != nil {
		api.Error(w, 400, "invalid_workbook")
		return
	}
	m := in.Mapping
	raw, e := h.workbookImportRows(rows, Mapping{m.EmployeeID, m.Phone, m.Active, m.Zone, m.Department}, *in.SkipHeader, m.FirstName, m.LastName)
	if e != nil {
		api.Error(w, 400, "invalid_mapping_or_roster")
		return
	}
	digest := sha256.Sum256(data)
	source := hex.EncodeToString(digest[:])
	p, meta, validation, e := h.prepareImport(s, raw, in.Mode, in.Selected, source)
	if e != nil {
		api.Error(w, 400, "invalid_roster")
		return
	}
	if len(validation) > 0 {
		if h.cfg.ImportDefaultMode == "" && in.Mode == "" {
			api.Error(w, 400, "invalid_mapping_or_roster")
			return
		}
		api.Write(w, 200, map[string]any{"validation_only": true, "rows": validation, "expected_version": s.Version})
		return
	}
	if h.cfg.ImportDefaultMode == "" && in.Mode == "" && in.Selected == nil {
		meta = nil
	}
	if meta != nil {
		if !validImportRequestID(in.RequestID) {
			api.Error(w, 400, "invalid_request_id")
			return
		}
		meta.RequestID = in.RequestID
	}
	h.savePreview(w, r, a, p, s.Version, source, meta)
}
