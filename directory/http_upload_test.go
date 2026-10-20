package directory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func uploadDirectory(t *testing.T, h http.Handler, a api.Actor, name string, data []byte, meta string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	m := multipart.NewWriter(&buf)
	m.WriteField("metadata", meta)
	part, _ := m.CreateFormFile("file", name)
	part.Write(data)
	m.Close()
	r := httptest.NewRequest("POST", "/api/extensions/directory/preview-file", &buf)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPWorkbookImport(t *testing.T) {
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
	tenant := fmt.Sprintf("directory-upload-%d", time.Now().UnixNano())
	defer func() {
		for _, table := range []string{"directory_previews", "directory_events", "directory_snapshots"} {
			db.Exec("DELETE FROM "+table+" WHERE tenant=$1", tenant)
		}
	}()
	a := api.Actor{OrganizationID: tenant, UserID: "importer", SessionID: "s", MFA: true, Permissions: []string{"directory:import", "directory:approve"}}
	h := NewHTTPHandler(db, HTTPConfig{Freshness: time.Hour, Clock: time.Now})
	metadata := `{"expected_version":0,"worksheet":"xl/worksheets/sheet1.xml","mapping":{"employee_id":0,"phone":1,"active":2,"zone":-1,"department":-1},"skip_header":false}`
	valid := workbook(t, `<worksheet><row><c r="A1" t="inlineStr"><is><t>e1</t></is></c><c r="B1" t="inlineStr"><is><t>+27820000001</t></is></c><c r="C1"><v>1</v></c></row></worksheet>`)
	w := uploadDirectory(t, h, a, "roster.xlsx", valid, metadata)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Data previewDTO `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Data.SourceSHA256 == "" || len(out.Data.Employees) != 1 {
		t.Fatal(out)
	}
	for _, tc := range []struct {
		name     string
		data     []byte
		metadata string
	}{
		{"bad.xlsx", []byte("malformed"), metadata},
		{"large.xlsx", make([]byte, (8<<20)+1), metadata},
		{"formula.xlsx", workbook(t, `<worksheet><row><c r="A1"><f>1+1</f><v>2</v></c></row></worksheet>`), metadata},
		{"legacy.xls", []byte("binary"), metadata},
		{"mapping.xlsx", valid, `{"expected_version":0,"worksheet":"xl/worksheets/sheet1.xml","mapping":{"employee_id":0,"phone":0,"active":2,"zone":-1,"department":-1},"skip_header":false}`},
	} {
		if w = uploadDirectory(t, h, a, tc.name, tc.data, tc.metadata); w.Code != 400 {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	w = directoryRequest(h, a, "POST", "approve", map[string]any{"preview_id": out.Data.PreviewID, "digest": out.Data.Digest, "expected_version": 0})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
