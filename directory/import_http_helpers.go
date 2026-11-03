package directory

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func (h *directoryHTTP) importLimits() Limits {
	v := DefaultLimits()
	if h.cfg.ImportMaxFileBytes > 0 {
		v.MaxFileBytes = h.cfg.ImportMaxFileBytes
	}
	if h.cfg.ImportMaxRows > 0 {
		v.MaxRows = h.cfg.ImportMaxRows + 1
	}
	return v
}
func (h *directoryHTTP) decodeImport(r *http.Request, v any) error {
	max := h.cfg.ImportMaxFileBytes
	if max == 0 {
		max = 1 << 20
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, max+1))
	if e != nil {
		return e
	}
	if int64(len(b)) > max {
		return errors.New("input too large")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return errors.New("trailing input")
	}
	return nil
}
func (h *directoryHTTP) workbookImportRows(rows [][]string, m Mapping, skip bool, names ...*int) ([]ImportRow, error) {
	required := []int{m.EmployeeID, m.Active}
	optional := []int{m.Zone, m.Department}
	if h.cfg.AllowPhoneLessEmployees {
		optional = append(optional, m.Phone)
	} else {
		required = append(required, m.Phone)
	}
	seen := map[int]bool{}
	for _, i := range required {
		if i < 0 || i >= 256 || seen[i] {
			return nil, errors.New("invalid mapping")
		}
		seen[i] = true
	}
	for _, i := range optional {
		if i < -1 || i >= 256 || i >= 0 && seen[i] {
			return nil, errors.New("invalid mapping")
		}
		if i >= 0 {
			seen[i] = true
		}
	}
	for _, index := range names {
		if index != nil && (*index < -1 || *index >= 256 || (*index >= 0 && seen[*index])) {
			return nil, errors.New("invalid name mapping")
		}
		if index != nil && *index >= 0 {
			seen[*index] = true
		}
	}
	if skip && len(rows) > 0 {
		rows = rows[1:]
	}
	out := []ImportRow{}
	for _, row := range rows {
		get := func(i int) string {
			if i < 0 || i >= len(row) {
				return ""
			}
			return row[i]
		}
		blank := true
		for _, v := range row {
			if strings.TrimSpace(v) != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		var first, last *string
		if len(names) > 0 && names[0] != nil && *names[0] >= 0 {
			v := get(*names[0])
			first = &v
		}
		if len(names) > 1 && names[1] != nil && *names[1] >= 0 {
			v := get(*names[1])
			last = &v
		}
		out = append(out, ImportRow{FirstName: first, LastName: last, ID: get(m.EmployeeID), Phone: get(m.Phone), Active: get(m.Active), Zone: get(m.Zone), Department: get(m.Department)})
	}
	return out, nil
}

func validImportRequestID(v string) bool {
	if v == "" {
		return true
	}
	if len(v) != 32 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
