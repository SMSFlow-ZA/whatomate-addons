// Package directory implements employee eligibility without granting roles.
package directory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrConflict = errors.New("directory version conflict")
var ErrDenied = errors.New("directory eligibility denied")
var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

type Employee struct {
	ID, Phone, Zone, Department string
	Active                      bool
}

// Preview is immutable to callers. Approvers review Employees and bind approval
// to Digest; the host authenticates and authorizes that approver separately.
type Preview struct {
	employees []Employee
	digest    string
}

func (p Preview) Digest() string        { return p.digest }
func (p Preview) Employees() []Employee { return append([]Employee(nil), p.employees...) }
func NewPreview(rows []Employee) (Preview, error) {
	if len(rows) == 0 || len(rows) > 100000 {
		return Preview{}, errors.New("empty or oversized directory")
	}
	copyRows := append([]Employee(nil), rows...)
	ids := map[string]bool{}
	phones := map[string]bool{}
	for i, e := range copyRows {
		if e.ID == "" || e.ID != strings.TrimSpace(e.ID) || len(e.ID) > 256 || len(e.Zone) > 256 || len(e.Department) > 256 || !phonePattern.MatchString(e.Phone) {
			return Preview{}, fmt.Errorf("invalid employee at row %d", i+1)
		}
		if ids[e.ID] || phones[e.Phone] {
			return Preview{}, fmt.Errorf("duplicate employee ID or phone at row %d", i+1)
		}
		ids[e.ID] = true
		phones[e.Phone] = true
	}
	sort.Slice(copyRows, func(i, j int) bool { return copyRows[i].ID < copyRows[j].ID })
	b, _ := json.Marshal(copyRows)
	h := sha256.Sum256(b)
	return Preview{copyRows, hex.EncodeToString(h[:])}, nil
}

type Snapshot struct {
	Version    uint64
	ImportedAt time.Time
	Employees  map[string]Employee
	Revoked    map[string]bool
}

func clone(s Snapshot) Snapshot {
	n := s
	n.Employees = map[string]Employee{}
	n.Revoked = map[string]bool{}
	for k, v := range s.Employees {
		n.Employees[k] = v
	}
	for k, v := range s.Revoked {
		n.Revoked[k] = v
	}
	return n
}

// Replace builds a full replacement, retaining missing employees as inactive and
// retaining urgent revocations. It never mutates the supplied snapshot.
func Replace(s Snapshot, p Preview, approvedDigest string, now time.Time) (Snapshot, error) {
	if p.digest == "" || p.digest != approvedDigest || now.IsZero() || s.Version == ^uint64(0) {
		return Snapshot{}, errors.New("invalid approval, clock, or version")
	}
	n := clone(s)
	n.Version++
	n.ImportedAt = now
	for k, e := range n.Employees {
		e.Active = false
		n.Employees[k] = e
	}
	for _, e := range p.employees {
		n.Employees[e.ID] = e
	}
	return n, nil
}
func Revoke(s Snapshot, id string) (Snapshot, error) {
	if strings.TrimSpace(id) == "" || s.Version == ^uint64(0) {
		return Snapshot{}, errors.New("invalid revocation")
	}
	n := clone(s)
	n.Version++
	n.Revoked[id] = true
	return n, nil
}

// Match requires exact canonical registered phone, active status and configured
// freshness. This is eligibility, not proof of possession or role authorization.
func Match(s Snapshot, phone string, now time.Time, maxAge time.Duration) (Employee, error) {
	if !phonePattern.MatchString(phone) || maxAge <= 0 || s.ImportedAt.IsZero() || now.Before(s.ImportedAt) || now.Sub(s.ImportedAt) >= maxAge {
		return Employee{}, ErrDenied
	}
	var found Employee
	count := 0
	for id, e := range s.Employees {
		if e.Phone == phone {
			count++
			if id != e.ID || !e.Active || s.Revoked[id] {
				return Employee{}, ErrDenied
			}
			found = e
		}
	}
	if count != 1 {
		return Employee{}, ErrDenied
	}
	return found, nil
}

// Store must provide durable, strongly consistent snapshots and atomically CAS
// the entire snapshot. Failed CAS MUST preserve prior state. Cross-process
// concurrency, tenant isolation and audit transactions belong to the adapter.
type Store interface {
	Load(context.Context) (Snapshot, error)
	CompareAndSwap(context.Context, uint64, Snapshot) error
}

func Apply(ctx context.Context, store Store, p Preview, approvedDigest string, now time.Time) (Snapshot, error) {
	s, err := store.Load(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	n, err := Replace(s, p, approvedDigest, now)
	if err != nil {
		return Snapshot{}, err
	}
	if err = store.CompareAndSwap(ctx, s.Version, n); err != nil {
		return Snapshot{}, err
	}
	return n, nil
}
func UrgentRevoke(ctx context.Context, store Store, id string) error {
	s, err := store.Load(ctx)
	if err != nil {
		return err
	}
	n, err := Revoke(s, id)
	if err != nil {
		return err
	}
	return store.CompareAndSwap(ctx, s.Version, n)
}
