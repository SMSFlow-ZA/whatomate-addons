package directory

import (
	"testing"
	"time"
)

func TestNativePreviewOptionalPhoneKeepsStrictDefault(t *testing.T) {
	rows := []Employee{{ID: "one", Active: true}, {ID: "two", Active: true}}
	if _, e := NewPreview(rows); e == nil {
		t.Fatal("strict default accepted blank phones")
	}
	p, e := NewPreviewWithOptions(rows, PreviewOptions{AllowPhoneLessEmployees: true})
	if e != nil || len(p.Employees()) != 2 {
		t.Fatalf("native preview: %v", e)
	}
	for _, bad := range [][]Employee{{{ID: "one"}, {ID: "one"}}, {{ID: "one", Phone: "0820000001"}}, {{ID: "one", Phone: " "}}, {{ID: "one", Phone: "+27820000001"}, {ID: "two", Phone: "+27820000001"}}} {
		if _, e := NewPreviewWithOptions(bad, PreviewOptions{AllowPhoneLessEmployees: true}); e == nil {
			t.Fatal("invalid or ambiguous phone accepted")
		}
	}
	s, e := Replace(Snapshot{}, p, p.Digest(), time.Now().Add(-time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Match(s, "", time.Now(), time.Hour); e == nil {
		t.Fatal("blank phone matched employee")
	}
	for _, id := range []string{"one", "two"} {
		if _, e = MatchNative(s, id, time.Now(), time.Hour); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{"", "unknown"} {
		if _, e = MatchNative(s, id, time.Now(), time.Hour); e == nil {
			t.Fatal("unknown identity accepted")
		}
	}
	for _, age := range []time.Duration{time.Hour, time.Hour * 2, -time.Minute} {
		s.ImportedAt = time.Now().Add(-age)
		if _, e = MatchNative(s, "one", time.Now(), time.Hour); e == nil {
			t.Fatal("invalid freshness accepted")
		}
	}
	s.ImportedAt = time.Time{}
	if _, e = MatchNative(s, "one", time.Now(), time.Hour); e == nil {
		t.Fatal("zero date accepted")
	}
	s.ImportedAt = time.Now()
	s.Revoked["one"] = true
	if _, e = MatchNative(s, "one", time.Now(), time.Hour); e == nil {
		t.Fatal("revoked accepted")
	}
	s.Revoked["one"] = false
	v := s.Employees["one"]
	v.Active = false
	s.Employees["one"] = v
	if _, e = MatchNative(s, "one", time.Now(), time.Hour); e == nil {
		t.Fatal("inactive accepted")
	}
}
func TestNativeWorkbookMayOmitPhoneColumn(t *testing.T) {
	rows := [][]string{{"one", "true"}, {"two", "true"}}
	m := Mapping{EmployeeID: 0, Phone: -1, Active: 1, Zone: -1, Department: -1}
	if _, e := MapRows(rows, m, false); e == nil {
		t.Fatal("strict mapping accepted omitted phone")
	}
	if _, e := MapRowsWithOptions(rows, m, false, PreviewOptions{AllowPhoneLessEmployees: true}); e != nil {
		t.Fatal(e)
	}
	m.Phone = 0
	if _, e := MapRowsWithOptions(rows, m, false, PreviewOptions{AllowPhoneLessEmployees: true}); e == nil {
		t.Fatal("overlapping mapping accepted")
	}
}
