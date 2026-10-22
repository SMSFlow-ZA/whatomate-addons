package directory

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// IdentityBinding is deployment-owned verified identity evidence. It is never
// inferred from an account name, request body or inbound employee assertion.
type IdentityBinding struct {
	EmployeeID            string `json:"employee_id"`
	Phone                 string `json:"phone"`
	ExpectedRosterVersion uint64 `json:"expected_roster_version"`
}

func (b IdentityBinding) Validate() error {
	if strings.TrimSpace(b.EmployeeID) != b.EmployeeID || b.EmployeeID == "" || len(b.EmployeeID) > 256 || strings.ContainsAny(b.EmployeeID, "*\x00\r\n") || !phonePattern.MatchString(b.Phone) || b.ExpectedRosterVersion == 0 || b.ExpectedRosterVersion > math.MaxInt64 {
		return ErrDenied
	}
	return nil
}

// ValidateIdentityTx validates the directory-owned current approved snapshot
// inside the caller's authorization transaction. The held shared row lock must
// remain until the grant/link/audit transaction commits or rolls back.
func ValidateIdentityTx(ctx context.Context, tx *sql.Tx, tenant string, binding IdentityBinding, freshness time.Duration, now time.Time) error {
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
	employee, err := Match(snapshot, binding.Phone, now, freshness)
	if err != nil || employee.ID != binding.EmployeeID {
		return ErrDenied
	}
	return nil
}
