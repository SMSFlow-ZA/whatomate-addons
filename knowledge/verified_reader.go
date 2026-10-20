package knowledge

import (
	"context"
	"database/sql"
	"strings"
)

// EnsureVerifiedReader is a trusted channel adapter port, never an HTTP DTO.
// It grants only read for one exact audience. The supplied authorizer must check
// current employee eligibility again on every retrieval and after inference;
// possession of a phone number does not create operator permissions or MFA.
func EnsureVerifiedReader(ctx context.Context, db *sql.DB, tenant, principal, audience string, auth Authorizer) error {
	if db == nil || tenant == "" || !strings.HasPrefix(principal, "employee:") || len(principal) <= len("employee:") || audience == "" || audience == "*" || auth == nil || !auth.Allowed(ctx, principal, audience) {
		return ErrDenied
	}
	_, e := db.ExecContext(ctx, `INSERT INTO addon_knowledge_grants(tenant,principal,action,scope)VALUES($1,$2,'read',$3) ON CONFLICT DO NOTHING`, tenant, principal, audience)
	return e
}
