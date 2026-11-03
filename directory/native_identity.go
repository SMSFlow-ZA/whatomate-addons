package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// NativeIdentityBinding is trusted verified native sign-in/approved enrollment evidence.
// It must never be populated from an unauthenticated employee ID assertion. It is never
// inferred from an account name, request body or inbound employee assertion.
type NativeIdentityBinding struct {
	EmployeeID            string `json:"employee_id"`
	ExpectedRosterVersion uint64 `json:"expected_roster_version"`
}

func (b NativeIdentityBinding) Validate() error {
	if strings.TrimSpace(b.EmployeeID) != b.EmployeeID || b.EmployeeID == "" || len(b.EmployeeID) > 256 || strings.ContainsAny(b.EmployeeID, "*\x00\r\n") || b.ExpectedRosterVersion == 0 || b.ExpectedRosterVersion > math.MaxInt64 {
		return ErrDenied
	}
	return nil
}

// ValidateNativeIdentityTx validates the directory-owned current approved snapshot
// inside the caller's authorization transaction. The held shared row lock must
// remain until the grant/link/audit transaction commits or rolls back.
func ValidateNativeIdentityTx(ctx context.Context, tx *sql.Tx, tenant string, binding NativeIdentityBinding, freshness time.Duration, now time.Time) error {
	if tx == nil || strings.TrimSpace(tenant) == "" || freshness <= 0 || now.IsZero() || binding.Validate() != nil {
		return ErrDenied
	}
	var version int64
	var payload []byte
	// Every directory approval/replacement/revocation uses an UPDATE of this
	// same owner row. FOR SHARE therefore fences those writes until the caller
	// commits the link, grants and audit, or rolls the entire operation back.
	if err := tx.QueryRowContext(ctx, `SELECT version,payload FROM directory_snapshots WHERE tenant=$1 FOR SHARE`, tenant).Scan(&version, &payload); err != nil {
		return err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return ErrDenied
	}
	if version <= 0 || snapshot.Version != uint64(version) || snapshot.Version != binding.ExpectedRosterVersion {
		return ErrDenied
	}
	var approved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM directory_events WHERE tenant=$1 AND version=$2 AND actor<>'')`, tenant, version).Scan(&approved); err != nil {
		return err
	}
	if !approved {
		return ErrDenied
	}
	employee, err := MatchNative(snapshot, binding.EmployeeID, now, freshness)
	if err != nil || employee.ID != binding.EmployeeID {
		return ErrDenied
	}
	return nil
}

// MatchNative selects only a stable employee identifier, never a blank phone alias.
func MatchNative(s Snapshot, id string, now time.Time, maxAge time.Duration) (Employee, error) {
	if id == "" || id != strings.TrimSpace(id) || now.IsZero() || maxAge <= 0 || s.ImportedAt.IsZero() || now.Before(s.ImportedAt) || now.Sub(s.ImportedAt) >= maxAge {
		return Employee{}, ErrDenied
	}
	employee, ok := s.Employees[id]
	if !ok || employee.ID != id || !employee.Active || s.Revoked[id] {
		return Employee{}, ErrDenied
	}
	return employee, nil
}
