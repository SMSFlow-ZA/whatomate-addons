package knowledge

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// helper declarations live here so lifecycle tests exercise the real HTTP
// boundary and current host resolver, rather than calling domain methods only.
func knowledgeRequest(h http.Handler, a api.Actor, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/extensions/knowledge/"+path, strings.NewReader(body))
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func uploadRequest(h http.Handler, a api.Actor, m UploadMetadata) *httptest.ResponseRecorder {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	p, _ := mw.CreateFormFile("file", m.Filename)
	p.Write([]byte("synthetic PDF"))
	p, _ = mw.CreateFormField("metadata")
	j, _ := json.Marshal(m)
	p.Write(j)
	mw.Close()
	r := httptest.NewRequest("POST", "/api/extensions/knowledge/uploads", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r = r.WithContext(api.WithActor(r.Context(), a))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
