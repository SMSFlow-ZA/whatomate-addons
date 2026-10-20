package cases

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// ReadEmergency is a read-only exception to assignment visibility, not to
// organisation/unit scope or subject conflicts. The host must verify the current
// signed session and durable independent approval for this exact case ID.
// No assignment, lifecycle, version or case event is changed by this method.
func (s Service) ReadEmergency(ctx context.Context, p Principal, id string, authorize func(context.Context, string) error) (Case, error) {
	if authorize == nil || p.ID == "" || id == "" {
		return Case{}, ErrDenied
	}
	if s.Repository == nil {
		return Case{}, ErrInvalid
	}
	tx, e := s.Repository.Begin(ctx)
	if e != nil {
		return Case{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	c, e := tx.Load(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return Case{}, ErrDenied
	}
	if e != nil {
		return Case{}, e
	}
	if c.ID != id || subject(p, c) || !scoped(p, c, ReadTriage) {
		return Case{}, ErrDenied
	}
	if e = authorize(ctx, c.ID); e != nil {
		return Case{}, ErrDenied
	}
	if ctx.Err() != nil {
		return Case{}, ErrDenied
	}
	// A concurrent subject link, reassignment or scope change must not turn a
	// decision made against one version into disclosure of another version.
	current, e := tx.Load(ctx, id)
	if errors.Is(e, ErrNotFound) {
		return Case{}, ErrDenied
	}
	if e != nil {
		return Case{}, e
	}
	if current.ID != id || current.Version != c.Version || subject(p, current) || !scoped(p, current, ReadTriage) {
		return Case{}, ErrDenied
	}
	return current, nil
}

func serveEmergencyRead(w http.ResponseWriter, r *http.Request, s Service, cfg HTTPConfig, a api.Actor, p Principal, id string) {
	deny := func() { api.Error(w, http.StatusNotFound, "not_found") }
	if r.Method != http.MethodPost || cfg.AuthorizeEmergency == nil || !a.Has(string(ReadTriage)) {
		deny()
		return
	}
	var in struct {
		ApprovalID string `json:"approval_id"`
	}
	if api.Decode(r, &in) != nil || in.ApprovalID == "" || len(in.ApprovalID) > 256 || strings.TrimSpace(in.ApprovalID) != in.ApprovalID {
		deny()
		return
	}
	c, e := s.ReadEmergency(r.Context(), p, id, func(ctx context.Context, caseID string) error {
		return cfg.AuthorizeEmergency(ctx, a, caseID, in.ApprovalID)
	})
	if e != nil {
		caseError(w, e)
		return
	}
	api.Write(w, http.StatusOK, operatorView(c))
}
