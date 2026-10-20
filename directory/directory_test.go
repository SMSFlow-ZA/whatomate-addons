package directory

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentApprovalsOnlyOneCASWins(t *testing.T) {
	p, _ := NewPreview(employees())
	s := &atomicTestStore{ready: make(chan struct{}, 2), release: make(chan struct{})}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := Apply(context.Background(), s, p, p.Digest(), time.Now()); results <- err }()
	}
	<-s.ready
	<-s.ready
	close(s.release)
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 || s.snapshot.Version != 1 {
		t.Fatal(successes, conflicts, s.snapshot.Version)
	}
}

type atomicTestStore struct {
	mu       sync.Mutex
	snapshot Snapshot
	ready    chan struct{}
	release  chan struct{}
}

func (s *atomicTestStore) Load(context.Context) (Snapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	s.ready <- struct{}{}
	<-s.release
	return snapshot, nil
}
func (s *atomicTestStore) CompareAndSwap(_ context.Context, v uint64, n Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.Version != v {
		return ErrConflict
	}
	s.snapshot = n
	return nil
}

func TestImportAndMatchFailClosed(t *testing.T) {
	p, _ := NewPreview(employees())
	now := time.Now()
	snapshot, _ := Replace(Snapshot{}, p, p.Digest(), now)
	snapshot.Employees["e2"] = Employee{ID: "e2", Phone: "+27820000001", Active: true}
	if _, err := Match(snapshot, "+27820000001", now, time.Hour); err == nil {
		t.Fatal("ambiguous snapshot permitted")
	}
	if _, err := ReadXLS(bytes.NewReader([]byte("not an xls")), 10, DefaultLimits()); !errors.Is(err, ErrLegacyXLSUnsupported) {
		t.Fatal(err)
	}
	invalid := []struct {
		xml    string
		limits Limits
	}{
		{`<worksheet><row><c r="XFD1"><v>1</v></c></row></worksheet>`, DefaultLimits()},
		{`<worksheet><row><c r="A1" t="s"><v>400</v></c></row></worksheet>`, DefaultLimits()},
		{`<worksheet><row><c r="A1"><v>1</v></c><c r="A1"><v>2</v></c></row></worksheet>`, DefaultLimits()},
	}
	for _, tc := range invalid {
		data := workbook(t, tc.xml)
		if _, err := ReadXLSX(bytes.NewReader(data), int64(len(data)), "xl/worksheets/sheet1.xml", tc.limits); err == nil {
			t.Fatal("unsafe workbook accepted")
		}
	}
	if _, err := MapRows([][]string{{"e1", "+27820000001", "maybe"}}, Mapping{0, 1, 2, -1, -1}, false); err == nil {
		t.Fatal("ambiguous active flag accepted")
	}
}

func employees() []Employee {
	return []Employee{{ID: "e1", Phone: "+27820000001", Active: true, Zone: "north", Department: "ops"}}
}
func TestPreviewRejectsAmbiguity(t *testing.T) {
	for _, rows := range [][]Employee{
		{employees()[0], employees()[0]},
		{employees()[0], {ID: "e2", Phone: "+27820000001", Active: false}},
		{{ID: "e1", Phone: "0820000001", Active: true}},
	} {
		if _, err := NewPreview(rows); err == nil {
			t.Fatal("unsafe preview accepted")
		}
	}
}
func TestReplacementRevocationAndFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := Snapshot{Version: 7, Employees: map[string]Employee{"missing": {ID: "missing", Phone: "+27820000002", Active: true}}, Revoked: map[string]bool{"e1": true}}
	p, err := NewPreview(employees())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replace(old, p, "wrong", now); err == nil {
		t.Fatal("unapproved accepted")
	}
	next, err := Replace(old, p, p.Digest(), now)
	if err != nil {
		t.Fatal(err)
	}
	if next.Employees["missing"].Active || next.Version != 8 {
		t.Fatal("replacement failed")
	}
	if _, err = Match(next, "+27820000001", now, time.Hour); err == nil {
		t.Fatal("revoked user matched")
	}
	next.Revoked = map[string]bool{}
	if got, err := Match(next, "+27820000001", now, time.Hour); err != nil || got.ID != "e1" {
		t.Fatal(got, err)
	}
	for _, age := range []time.Duration{0, time.Hour, 2 * time.Hour, -time.Second} {
		ttl := time.Hour
		if age == 0 {
			ttl = 0
		}
		if _, err := Match(next, "+27820000001", now.Add(age), ttl); err == nil {
			t.Fatal("stale/unconfigured/future snapshot allowed", age)
		}
	}
	revoked, err := Revoke(next, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Match(revoked, "+27820000001", now, time.Hour); err == nil {
		t.Fatal("urgent revocation ignored")
	}
	if next.Revoked["e1"] {
		t.Fatal("input mutated")
	}
}
func TestCASFailurePropagatesWithoutRetry(t *testing.T) {
	p, _ := NewPreview(employees())
	repo := &conflictStore{}
	_, err := Apply(context.Background(), repo, p, p.Digest(), time.Now())
	if !errors.Is(err, ErrConflict) || repo.calls != 1 {
		t.Fatal(err, repo.calls)
	}
}

type conflictStore struct{ calls int }

func (s *conflictStore) Load(context.Context) (Snapshot, error) { return Snapshot{Version: 9}, nil }
func (s *conflictStore) CompareAndSwap(_ context.Context, v uint64, next Snapshot) error {
	s.calls++
	if v != 9 || next.Version != 10 {
		panic("wrong CAS version")
	}
	return ErrConflict
}

func workbook(t *testing.T, sheet string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, data := range map[string]string{"xl/worksheets/sheet1.xml": sheet} {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte(data))
	}
	z.Close()
	return b.Bytes()
}
func TestXLSXBoundsAndFormulaRejection(t *testing.T) {
	xml := `<worksheet><sheetData><row><c r="A1" t="inlineStr"><is><t>e1</t></is></c><c r="B1" t="inlineStr"><is><t>+27820000001</t></is></c><c r="C1" t="b"><v>1</v></c></row></sheetData></worksheet>`
	data := workbook(t, xml)
	limits := DefaultLimits()
	rows, err := ReadXLSX(bytes.NewReader(data), int64(len(data)), "xl/worksheets/sheet1.xml", limits)
	if err != nil || len(rows) != 1 || rows[0][1] != "+27820000001" {
		t.Fatal(rows, err)
	}
	tiny := limits
	tiny.MaxExpandedBytes = 10
	if _, err := ReadXLSX(bytes.NewReader(data), int64(len(data)), "xl/worksheets/sheet1.xml", tiny); err == nil {
		t.Fatal("expansion limit ignored")
	}
	formula := workbook(t, `<worksheet><sheetData><row><c r="A1"><f>NOW()</f><v>3</v></c></row></sheetData></worksheet>`)
	if _, err := ReadXLSX(bytes.NewReader(formula), int64(len(formula)), "xl/worksheets/sheet1.xml", limits); err == nil {
		t.Fatal("formula accepted")
	}
	if _, err := MapRows(rows, Mapping{EmployeeID: 0, Phone: 1, Active: 2, Zone: -1, Department: -1}, false); err != nil {
		t.Fatal(err)
	}
}
