package cases

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
)

// PrivacyProtection retains only the policy facts needed by a derivative owner
// while an approved erasure still has pending physical copies. It is not content
// and cannot grant authority. Custodians must scrub it when cleanup is confirmed.
type PrivacyProtection struct {
	ID, OrganizationID, UnitID string
	Participants               []Participant
	Assignments                []Assignment
}

// CapturePrivacyProtection takes the current source lock and applies ordinary
// source visibility before returning a minimal, immutable protection projection.
func CapturePrivacyProtection(ctx context.Context, tx *sql.Tx, a api.Actor, id string) (PrivacyProtection, error) {
	c, e := PrivacyCase(ctx, tx, a, id)
	if e != nil {
		return PrivacyProtection{}, e
	}
	return PrivacyProtection{ID: c.ID, OrganizationID: c.OrganizationID, UnitID: c.UnitID, Participants: append([]Participant(nil), c.Participants...), Assignments: append([]Assignment(nil), c.Assignments...)}, nil
}

// AuthorizePrivacyProtection uses exactly the current operator-read policy.
// The derivative owner must additionally prove that the original source is
// genuinely absent after approved erasure; this helper never proves erasure.
func AuthorizePrivacyProtection(a api.Actor, p PrivacyProtection) error {
	if p.ID == "" || p.UnitID == "" || p.OrganizationID != a.OrganizationID || !operatorRead(PrincipalForActor(a), Case{ID: p.ID, OrganizationID: p.OrganizationID, UnitID: p.UnitID, Participants: p.Participants, Assignments: p.Assignments}) {
		return governance.ErrDenied
	}
	return nil
}
