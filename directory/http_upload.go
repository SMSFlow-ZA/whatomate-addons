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

func (h *directoryHTTP) savePreview(w http.ResponseWriter, r *http.Request, a api.Actor, p Preview, version uint64, source string) {
	var random [16]byte
	if _, e := rand.Read(random[:]); e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	dto := previewDTO{PreviewID: hex.EncodeToString(random[:]), Digest: p.Digest(), ExpectedVersion: version, CreatedBy: a.UserID, CreatedAt: h.cfg.Clock(), SourceSHA256: source, Employees: make([]employeeDTO, 0, len(p.employees))}
	for _, em := range p.Employees() {
		dto.Employees = append(dto.Employees, toDTO(em))
	}
	payload, _ := json.Marshal(dto.Employees)
	_, e := h.db.ExecContext(r.Context(), `INSERT INTO directory_previews(tenant,id,digest,expected_version,payload,created_by,created_at,source_sha256) VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8)`, a.OrganizationID, dto.PreviewID, dto.Digest, int64(version), string(payload), a.UserID, dto.CreatedAt, source)
	if e != nil {
		api.Error(w, 503, "unavailable")
		return
	}
	api.Write(w, 201, dto)
}

type workbookMapping struct {
	EmployeeID int `json:"employee_id"`
	Phone      int `json:"phone"`
	Active     int `json:"active"`
	Zone       int `json:"zone"`
	Department int `json:"department"`
}

func (h *directoryHTTP) upload(w http.ResponseWriter, r *http.Request, a api.Actor, store *PostgresStore) {
	// Stream multipart without writing attacker-controlled uploads into temporary files.
	r.Body = http.MaxBytesReader(w, r.Body, DefaultLimits().MaxFileBytes+(64<<10))
	reader, e := r.MultipartReader()
	if e != nil {
		api.Error(w, 400, "invalid_upload")
		return
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
			metadata, e = io.ReadAll(io.LimitReader(part, 8193))
			if e != nil || len(metadata) > 8192 {
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
			data, e = io.ReadAll(io.LimitReader(part, DefaultLimits().MaxFileBytes+1))
			if e != nil || len(data) == 0 || int64(len(data)) > DefaultLimits().MaxFileBytes {
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
		rows, e = ReadXLSX(bytes.NewReader(data), int64(len(data)), in.Worksheet, DefaultLimits())
	case ".xls":
		if h.cfg.Worker == nil {
			api.Error(w, 400, "legacy_xls_unconfigured")
			return
		}
		rows, e = h.cfg.Worker.Read(r.Context(), data, in.Worksheet, DefaultLimits())
	default:
		api.Error(w, 400, "unsupported_workbook")
		return
	}
	if e != nil {
		api.Error(w, 400, "invalid_workbook")
		return
	}
	m := in.Mapping
	p, e := MapRows(rows, Mapping{m.EmployeeID, m.Phone, m.Active, m.Zone, m.Department}, *in.SkipHeader)
	if e != nil {
		api.Error(w, 400, "invalid_mapping_or_roster")
		return
	}
	digest := sha256.Sum256(data)
	h.savePreview(w, r, a, p, s.Version, hex.EncodeToString(digest[:]))
}
