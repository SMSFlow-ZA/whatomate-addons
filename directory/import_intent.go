package directory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type ImportRow struct {
	FirstName  *string `json:"first_name,omitempty"`
	LastName   *string `json:"last_name,omitempty"`
	ID         string  `json:"id"`
	Phone      string  `json:"phone"`
	Zone       string  `json:"zone"`
	Department string  `json:"department"`
	Active     string  `json:"active"`
}
type ImportMetadata struct {
	RequestID     string `json:"request_id,omitempty"`
	Mode          string `json:"mode"`
	BaseVersion   uint64 `json:"base_version"`
	SourceDigest  string `json:"source_digest"`
	Selected      []int  `json:"selected_rows"`
	RemovalCount  int    `json:"removal_count"`
	RemovalDigest string `json:"removal_digest"`
}
type ImportRowResult struct {
	Row   int    `json:"row"`
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

func importHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func importDigest(p Preview, m *ImportMetadata) string {
	if m == nil {
		return p.Digest()
	}
	return importHash(struct {
		Rows   string
		Import *ImportMetadata
	}{p.Digest(), m})
}
func (h *directoryHTTP) prepareImport(s Snapshot, raw []ImportRow, mode string, selection *[]int, source string) (Preview, *ImportMetadata, []ImportRowResult, error) {
	bad := errors.New("invalid import")
	if mode == "" {
		mode = h.cfg.ImportDefaultMode
	}
	if mode == "" {
		mode = "replace"
	}
	if mode != "upsert" && mode != "replace" {
		return Preview{}, nil, nil, bad
	}
	max := h.cfg.ImportMaxRows
	if max == 0 {
		max = 100000
	}
	if max < 1 || len(raw) == 0 || len(raw) > max {
		return Preview{}, nil, nil, bad
	}
	opts := PreviewOptions{AllowPhoneLessEmployees: h.cfg.AllowPhoneLessEmployees}
	rows := make([]Employee, len(raw))
	checks := make([]ImportRowResult, len(raw))
	invalid := false
	for i, v := range raw {
		checks[i] = ImportRowResult{Row: i + 1, ID: v.ID}
		active := strings.ToLower(strings.TrimSpace(v.Active))
		first, last := nameValue(v.FirstName), nameValue(v.LastName)
		if mode == "upsert" {
			if old, ok := s.Employees[v.ID]; ok {
				if v.FirstName == nil {
					first = old.FirstName
				}
				if v.LastName == nil {
					last = old.LastName
				}
			}
		}
		rows[i] = Employee{FirstName: first, LastName: last, ID: v.ID, Phone: v.Phone, Zone: v.Zone, Department: v.Department, Active: active == "true" || active == "1" || active == "active"}
		_, e := NewPreviewWithOptions([]Employee{rows[i]}, opts)
		if h.cfg.RejectUnsafeImportText && !safeImportRow(v) {
			checks[i].Error = "Use plain text, without formulas, executable markup or hidden control characters"
			invalid = true
		}
		if e != nil || (active != "true" && active != "false" && active != "1" && active != "0" && active != "active" && active != "inactive") {
			checks[i].Error = "Check employee identifier, phone and active status"
			invalid = true
		}
	}
	// Mark both duplicate source rows. Selection must never silently pick a winner.
	ids := map[string]int{}
	phones := map[string]int{}
	for i, v := range rows {
		for _, pair := range []struct {
			key  string
			seen map[string]int
		}{{v.ID, ids}, {v.Phone, phones}} {
			if pair.key == "" {
				continue
			}
			if old, ok := pair.seen[pair.key]; ok {
				checks[i].Error = "Duplicate identifier or phone in source"
				checks[old].Error = checks[i].Error
				invalid = true
			} else {
				pair.seen[pair.key] = i
			}
		}
	}
	if selection == nil && invalid {
		return Preview{}, nil, checks, nil
	}
	selected := []int{}
	if selection == nil {
		for i := range raw {
			selected = append(selected, i+1)
		}
	} else {
		selected = append(selected, (*selection)...)
	}
	if len(selected) == 0 {
		return Preview{}, nil, nil, bad
	}
	sort.Ints(selected)
	merged := map[string]Employee{}
	if mode == "upsert" {
		for id, v := range s.Employees {
			merged[id] = v
		}
	}
	for i, n := range selected {
		if n < 1 || n > len(rows) || (i > 0 && selected[i-1] == n) || checks[n-1].Error != "" {
			return Preview{}, nil, nil, bad
		}
		merged[rows[n-1].ID] = rows[n-1]
	}
	values := make([]Employee, 0, len(merged))
	for _, v := range merged {
		values = append(values, v)
	}
	p, e := NewPreviewWithOptions(values, opts)
	if e != nil {
		return Preview{}, nil, nil, e
	}
	removed := []string{}
	if mode == "replace" {
		for id := range s.Employees {
			if _, ok := merged[id]; !ok {
				removed = append(removed, id)
			}
		}
	}
	sort.Strings(removed)
	m := &ImportMetadata{Mode: mode, BaseVersion: s.Version, SourceDigest: importHash(struct {
		Source string
		Rows   []ImportRow
	}{source, raw}), Selected: selected, RemovalCount: len(removed), RemovalDigest: importHash(removed)}
	return p, m, nil, nil
}
