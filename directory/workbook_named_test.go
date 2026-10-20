package directory

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"os"
	"testing"
	"time"
)

func namedWorkbook(t *testing.T, sheets, relations string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, part := range []struct{ name, body string }{
		{"xl/workbook.xml", `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` + sheets + `</sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + relations + `</Relationships>`},
		{"xl/worksheets/sheet1.xml", `<worksheet><row><c r="A1" t="inlineStr"><is><t>synthetic-001</t></is></c></row></worksheet>`},
	} {
		w, e := z.Create(part.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(part.body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

const namedSheet = `<sheet name="Synthetic roster" sheetId="1" r:id="rId1"/>`
const worksheetRelation = `Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"`

func TestReadXLSXNamedWorksheetAndExplicitCompatibility(t *testing.T) {
	data, e := os.ReadFile("testdata/named-roster.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	for _, selector := range []string{"Synthetic roster", "xl/worksheets/sheet1.xml"} {
		rows, e := ReadXLSX(bytes.NewReader(data), int64(len(data)), selector, DefaultLimits())
		if e != nil || len(rows) != 2 || rows[1][0] != "synthetic-001" {
			t.Fatal(selector, rows, e)
		}
	}
	for _, target := range []string{"/xl/worksheets/sheet1.xml", "worksheets/sheet1.xml"} {
		data = namedWorkbook(t, namedSheet, `<Relationship Id="rId1" `+worksheetRelation+` Target="`+target+`"/>`)
		if _, e = ReadXLSX(bytes.NewReader(data), int64(len(data)), "Synthetic roster", DefaultLimits()); e != nil {
			t.Fatal(target, e)
		}
	}
}
func TestReadXLSXNamedWorksheetRejectsUnsafeRelationships(t *testing.T) {
	safe := `<Relationship Id="rId1" ` + worksheetRelation + ` Target="worksheets/sheet1.xml"/>`
	for name, tc := range map[string]struct{ sheets, rels string }{
		"external":                     {namedSheet, `<Relationship Id="rId1" ` + worksheetRelation + ` Target="https://example.invalid/sheet.xml" TargetMode="External"/>`},
		"url":                          {namedSheet, `<Relationship Id="rId1" ` + worksheetRelation + ` Target="https://example.invalid/sheet.xml"/>`},
		"traversal":                    {namedSheet, `<Relationship Id="rId1" ` + worksheetRelation + ` Target="../xl/worksheets/sheet1.xml"/>`},
		"encoded traversal":            {namedSheet, `<Relationship Id="rId1" ` + worksheetRelation + ` Target="%2e%2e/worksheets/sheet1.xml"/>`},
		"duplicate relationship":       {namedSheet, safe + safe},
		"duplicate name":               {namedSheet + `<sheet name="Synthetic roster" sheetId="2" r:id="rId2"/>`, safe},
		"duplicate sheet id":           {namedSheet + `<sheet name="Other" sheetId="1" r:id="rId2"/>`, safe},
		"duplicate relation reference": {namedSheet + `<sheet name="Other" sheetId="2" r:id="rId1"/>`, safe},
		"wrong relationship type":      {namedSheet, `<Relationship Id="rId1" Type="styles" Target="worksheets/sheet1.xml"/>`},
		"unselected external":          {namedSheet, safe + `<Relationship Id="other" Type="styles" Target="https://example.invalid/style.xml" TargetMode="External"/>`},
		"missing reference":            {`<sheet name="Synthetic roster" sheetId="1"/>`, safe},
	} {
		t.Run(name, func(t *testing.T) {
			data := namedWorkbook(t, tc.sheets, tc.rels)
			if _, e := ReadXLSX(bytes.NewReader(data), int64(len(data)), "Synthetic roster", DefaultLimits()); e == nil {
				t.Fatal("unsafe named worksheet accepted")
			}
		})
	}
}
func TestHTTPNamedWorksheetPreviewPostgres(t *testing.T) {
	dsn := os.Getenv("DIRECTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("DIRECTORY_DATABASE_URL not configured")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = MigrateHTTP(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	tenant := fmt.Sprintf("directory-named-%d", time.Now().UnixNano())
	defer func() {
		for _, table := range []string{"directory_previews", "directory_events", "directory_snapshots"} {
			db.Exec("DELETE FROM "+table+" WHERE tenant=$1", tenant)
		}
	}()
	data, e := os.ReadFile("testdata/named-roster.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	a := api.Actor{OrganizationID: tenant, UserID: "synthetic-reviewer", SessionID: "s", MFA: true, Permissions: []string{"directory:import", "directory:approve"}}
	h := NewHTTPHandler(db, HTTPConfig{Freshness: time.Hour, Clock: time.Now})
	w := uploadDirectory(t, h, a, "named-roster.xlsx", data, `{"expected_version":0,"worksheet":"Synthetic roster","mapping":{"employee_id":0,"phone":1,"active":2,"zone":3,"department":4},"skip_header":true}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Data previewDTO `json:"data"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil || len(out.Data.Employees) != 1 || out.Data.Employees[0].ID != "synthetic-001" {
		t.Fatal(out, e)
	}
	w = directoryRequest(h, a, "POST", "approve", map[string]any{"preview_id": out.Data.PreviewID, "digest": out.Data.Digest, "expected_version": 0})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
