package directory

import (
	"testing"
	"time"
)

func testImportTime() time.Time { return time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC) }
func TestImportUpsertPreservesAndBindsSelection(t *testing.T) {
	h := directoryHTTP{cfg: HTTPConfig{ImportDefaultMode: "upsert", ImportMaxRows: 2000, AllowPhoneLessEmployees: true}}
	s := Snapshot{Version: 7, Employees: map[string]Employee{"old": {ID: "old", Active: true}}, Revoked: map[string]bool{"old": true}}
	rows := []ImportRow{{ID: "new", Active: "true"}, {ID: "bad", Phone: "bad", Active: "true"}}
	_, _, v, e := h.prepareImport(s, rows, "", nil, "source")
	if e != nil || len(v) != 2 || v[1].Error == "" {
		t.Fatal(v, e)
	}
	selected := []int{1}
	p, m, v, e := h.prepareImport(s, rows, "", &selected, "source")
	if e != nil || len(v) != 0 || len(p.Employees()) != 2 || m.Mode != "upsert" {
		t.Fatal(m, v, e)
	}
	n, e := Replace(s, p, p.Digest(), testImportTime())
	if e != nil || !n.Employees["old"].Active || !n.Revoked["old"] {
		t.Fatal(n, e)
	}
	first := importDigest(p, m)
	m.Mode = "replace"
	if first == importDigest(p, m) {
		t.Fatal("mode not bound")
	}
	selected = []int{2}
	if _, _, _, e = h.prepareImport(s, rows, "upsert", &selected, "source"); e == nil {
		t.Fatal("invalid selected row allowed")
	}
}
func TestImportDuplicateUnionReplacementAndLimits(t *testing.T) {
	h := directoryHTTP{cfg: HTTPConfig{ImportDefaultMode: "upsert", ImportMaxRows: 2, AllowPhoneLessEmployees: true}}
	s := Snapshot{Version: 1, Employees: map[string]Employee{"a": {ID: "a", Phone: "+27820000001", Active: true}}}
	if _, _, _, e := h.prepareImport(s, []ImportRow{{ID: "b", Phone: "+27820000001", Active: "true"}}, "upsert", nil, ""); e == nil {
		t.Fatal("union collision")
	}
	p, m, _, e := h.prepareImport(s, []ImportRow{{ID: "b", Active: "true"}}, "replace", nil, "")
	if e != nil || len(p.Employees()) != 1 || m.RemovalCount != 1 {
		t.Fatal(m, e)
	}
	if _, _, _, e = h.prepareImport(s, []ImportRow{{ID: "b", Active: "true"}, {ID: "c", Active: "true"}}, "upsert", nil, ""); e != nil {
		t.Fatal("preserved rows must not count against upload limit", e)
	}
	legacy := directoryHTTP{}
	p, m, _, e = legacy.prepareImport(s, []ImportRow{{ID: "b", Phone: "+27820000002", Active: "true"}}, "", nil, "")
	if e != nil || m.Mode != "replace" || len(p.Employees()) != 1 {
		t.Fatal(m, e)
	}
}
