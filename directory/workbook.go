package directory

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

type Limits struct {
	MaxFileBytes, MaxExpandedBytes                int64
	MaxEntries, MaxRows, MaxColumns, MaxCellBytes int
}

func DefaultLimits() Limits { return Limits{8 << 20, 32 << 20, 256, 100001, 64, 4096} }
func (l Limits) valid() bool {
	return l.MaxFileBytes > 0 && l.MaxFileBytes <= 64<<20 && l.MaxExpandedBytes > 0 && l.MaxExpandedBytes <= 128<<20 && l.MaxEntries > 0 && l.MaxEntries <= 4096 && l.MaxRows > 0 && l.MaxRows <= 100001 && l.MaxColumns > 0 && l.MaxColumns <= 256 && l.MaxCellBytes > 0 && l.MaxCellBytes <= 65536
}

type xmlText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (s xmlText) value() string {
	v := s.Text
	for _, r := range s.Runs {
		v += r.Text
	}
	return v
}

type cell struct {
	Reference string  `xml:"r,attr"`
	Type      string  `xml:"t,attr"`
	Formula   *string `xml:"f"`
	Value     string  `xml:"v"`
	Inline    xmlText `xml:"is"`
}

// ReadXLSX reads only the explicitly selected local worksheet ZIP entry.
// It never follows relationships, opens URLs, or evaluates formulas/macros.
func ReadXLSX(r io.ReaderAt, size int64, sheet string, l Limits) ([][]string, error) {
	if !l.valid() || size <= 0 || size > l.MaxFileBytes || !strings.HasPrefix(sheet, "xl/worksheets/") || path.Clean(sheet) != sheet || !strings.HasSuffix(sheet, ".xml") {
		return nil, errors.New("invalid workbook bounds or worksheet")
	}
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	if len(z.File) > l.MaxEntries {
		return nil, errors.New("too many ZIP entries")
	}
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		if _, ok := files[f.Name]; ok {
			return nil, errors.New("duplicate ZIP entry")
		}
		files[f.Name] = f
		total += f.UncompressedSize64
		if total > uint64(l.MaxExpandedBytes) || f.UncompressedSize64 > uint64(l.MaxExpandedBytes) {
			return nil, errors.New("expanded workbook exceeds bound")
		}
		lower := strings.ToLower(f.Name)
		if strings.HasSuffix(lower, ".bin") || strings.Contains(lower, "macrosheet") {
			return nil, errors.New("macro/binary workbook parts forbidden")
		}
	}
	read := func(name string) ([]byte, error) {
		f := files[name]
		if f == nil {
			return nil, errors.New("missing workbook entry")
		}
		rc, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer rc.Close()
		b, e := io.ReadAll(io.LimitReader(rc, l.MaxExpandedBytes+1))
		if int64(len(b)) > l.MaxExpandedBytes {
			return nil, errors.New("entry exceeds bound")
		}
		return b, e
	}
	var shared []string
	if files["xl/sharedStrings.xml"] != nil {
		b, e := read("xl/sharedStrings.xml")
		if e != nil {
			return nil, e
		}
		var ss struct {
			Items []xmlText `xml:"si"`
		}
		if e = xml.Unmarshal(b, &ss); e != nil {
			return nil, e
		}
		for _, s := range ss.Items {
			v := s.value()
			if len(v) > l.MaxCellBytes {
				return nil, errors.New("shared string too large")
			}
			shared = append(shared, v)
		}
	}
	b, err := read(sheet)
	if err != nil {
		return nil, err
	}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	var rows [][]string
	for {
		tok, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		if len(rows) >= l.MaxRows {
			return nil, errors.New("too many worksheet rows")
		}
		var row struct {
			Cells []cell `xml:"c"`
		}
		if e = d.DecodeElement(&row, &start); e != nil {
			return nil, e
		}
		if len(row.Cells) > l.MaxColumns {
			return nil, errors.New("too many cells")
		}
		values := []string{}
		seen := map[int]bool{}
		for _, c := range row.Cells {
			if c.Formula != nil {
				return nil, errors.New("formulas forbidden")
			}
			col, e := column(c.Reference)
			if e != nil || col >= l.MaxColumns || seen[col] {
				return nil, errors.New("invalid or repeated cell reference")
			}
			seen[col] = true
			v := c.Value
			switch c.Type {
			case "inlineStr":
				v = c.Inline.value()
			case "s":
				index, e := strconv.Atoi(v)
				if e != nil || index < 0 || index >= len(shared) {
					return nil, errors.New("invalid shared string index")
				}
				v = shared[index]
			case "", "n", "b", "str":
			default:
				return nil, errors.New("unsupported cell type")
			}
			if len(v) > l.MaxCellBytes {
				return nil, errors.New("cell too large")
			}
			for len(values) <= col {
				values = append(values, "")
			}
			values[col] = v
		}
		rows = append(rows, values)
	}
	return rows, nil
}
func column(ref string) (int, error) {
	n := 0
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		n = n*26 + int(ref[i]-'A'+1)
		i++
		if n > 256 {
			return 0, errors.New("column exceeds bound")
		}
	}
	if i == 0 || i == len(ref) {
		return 0, errors.New("missing cell coordinate")
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil || row < 1 || row > 1048576 {
		return 0, errors.New("invalid cell row")
	}
	return n - 1, nil
}

// Mapping uses zero-based column indexes. Optional Zone and Department may be -1.
type Mapping struct{ EmployeeID, Phone, Active, Zone, Department int }

func MapRows(rows [][]string, m Mapping, skipHeader bool) (Preview, error) {
	required := []int{m.EmployeeID, m.Phone, m.Active}
	seen := map[int]bool{}
	for _, i := range required {
		if i < 0 || i >= 256 || seen[i] {
			return Preview{}, errors.New("invalid required mapping")
		}
		seen[i] = true
	}
	for _, i := range []int{m.Zone, m.Department} {
		if i < -1 || i >= 256 || i >= 0 && seen[i] {
			return Preview{}, errors.New("invalid optional mapping")
		}
		if i >= 0 {
			seen[i] = true
		}
	}
	if skipHeader && len(rows) > 0 {
		rows = rows[1:]
	}
	out := []Employee{}
	for n, row := range rows {
		value := func(i int) string {
			if i < 0 || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		blank := true
		for _, s := range row {
			if strings.TrimSpace(s) != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		active := false
		switch strings.ToLower(value(m.Active)) {
		case "1", "true", "active":
			active = true
		case "0", "false", "inactive":
		default:
			return Preview{}, fmt.Errorf("invalid active status at row %d", n+1)
		}
		out = append(out, Employee{ID: value(m.EmployeeID), Phone: value(m.Phone), Active: active, Zone: value(m.Zone), Department: value(m.Department)})
	}
	return NewPreview(out)
}

var ErrLegacyXLSUnsupported = errors.New("legacy binary XLS requires an explicitly configured bounded local worker adapter")

// ReadXLS fails explicitly; XLS bytes must never be interpreted as XLSX or CSV.
// Legacy XLS support is a production readiness gap, not a successful import.
func ReadXLS(io.ReaderAt, int64, Limits) ([][]string, error) { return nil, ErrLegacyXLSUnsupported }
