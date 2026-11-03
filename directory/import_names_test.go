package directory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImportNamesLegacyDigestAndUpsert(t *testing.T) {
	old := []Employee{{ID: "e1", Phone: "+27820000001", Zone: "north", Department: "ops", Active: true}}
	p, e := NewPreview(old)
	if e != nil {
		t.Fatal(e)
	}
	const historical = `[{"ID":"e1","Phone":"+27820000001","Zone":"north","Department":"ops","Active":true}]`
	b, _ := json.Marshal(p.Employees())
	if string(b) != historical {
		t.Fatalf("legacy serialization changed: %s", b)
	}
	if p.Digest() != "2ef46ba7a4cbb1fe1a4ac76ce93eafadc02b2176d6b23a357e60839108d0be5a" || p.Digest() != importHash(json.RawMessage(historical)) {
		t.Fatal("historical canonical digest changed", p.Digest())
	}
	named := old[0]
	named.FirstName = "Lindiwe"
	named.LastName = "Dlamini"
	withName, e := NewPreview([]Employee{named})
	if e != nil || withName.Digest() == p.Digest() {
		t.Fatal("supplied names not bound", e)
	}
	h := directoryHTTP{cfg: HTTPConfig{ImportDefaultMode: "upsert", AllowPhoneLessEmployees: true, RejectUnsafeImportText: true}}
	snap := Snapshot{Version: 4, Employees: map[string]Employee{"e1": named}}
	updated, _, _, e := h.prepareImport(snap, []ImportRow{{ID: "e1", Phone: "+27820000002", Active: "true"}}, "upsert", nil, "")
	if e != nil || updated.Employees()[0].FirstName != "Lindiwe" || updated.Employees()[0].LastName != "Dlamini" {
		t.Fatal("unrelated update cleared names", updated, e)
	}
	empty := ""
	updated, _, _, e = h.prepareImport(snap, []ImportRow{{ID: "e1", FirstName: &empty, LastName: &empty, Active: "true"}}, "upsert", nil, "")
	if e != nil || updated.Employees()[0].FirstName != "" {
		t.Fatal("explicit name clear failed", e)
	}
	raw, _ := json.Marshal(toDTO(named))
	if !strings.Contains(string(raw), `"first_name":"Lindiwe"`) {
		t.Fatal(string(raw))
	}
}
func TestImportNativeUnsafeTextAndPhoneException(t *testing.T) {
	h := directoryHTTP{cfg: HTTPConfig{ImportDefaultMode: "upsert", AllowPhoneLessEmployees: true, RejectUnsafeImportText: true}}
	for _, bad := range []string{"=HYPERLINK(\"https://invalid\")", " +cmd", "-cmd", "@SUM(A1)", "\t=cmd", "<script>alert(1)</script>", "javascript:alert(1)", "hidden\u202evalue"} {
		for _, field := range []string{"id", "first_name", "last_name", "zone", "department"} {
			r := ImportRow{ID: "safe", Phone: "+27820000001", Active: "true"}
			switch field {
			case "id":
				r.ID = bad
			case "first_name":
				r.FirstName = &bad
			case "last_name":
				r.LastName = &bad
			case "zone":
				r.Zone = bad
			case "department":
				r.Department = bad
			}
			_, _, checks, e := h.prepareImport(Snapshot{}, []ImportRow{r}, "", nil, "")
			if e != nil || len(checks) != 1 || checks[0].Error == "" {
				t.Fatalf("unsafe %s accepted or not row-specific: %q %v %v", field, bad, checks, e)
			}
		}
	}
	r := ImportRow{ID: "EMP-17", Phone: "+27820000001", Active: "true"}
	p, _, checks, e := h.prepareImport(Snapshot{}, []ImportRow{r}, "", nil, "")
	if e != nil || len(checks) != 0 || p.Employees()[0].Phone != r.Phone {
		t.Fatal("valid E164 rejected", e)
	}
	h.cfg.RejectUnsafeImportText = false
	r.Zone = "=legacy"
	if _, _, checks, e = h.prepareImport(Snapshot{}, []ImportRow{r}, "replace", nil, ""); e != nil || len(checks) != 0 {
		t.Fatal("legacy policy changed", e)
	}
	h.cfg.RejectUnsafeImportText = true
	r.ID = " EMP-17 "
	_, _, checks, e = h.prepareImport(Snapshot{}, []ImportRow{r}, "", nil, "")
	if e != nil || len(checks) != 1 || checks[0].ID != r.ID {
		t.Fatal("identifier silently stripped", checks, e)
	}
}
