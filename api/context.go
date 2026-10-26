// Package api is the trusted HTTP boundary shared by compiled add-on modules.
// Only host authentication may construct actors. Nothing decodes an Actor from
// an HTTP body, header, query parameter or module configuration.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

var ErrDenied = errors.New("extension access denied")

const MaxBodyBytes = 1024 * 1024

type Grant struct {
	Permission string `json:"permission"`
	UnitID     string `json:"unit_id,omitempty"`
	Audience   string `json:"audience,omitempty"`
}
type Actor struct {
	OrganizationID, UserID, SessionID string
	MFA                               bool
	// PrimarySessionAuthorized is supplied only by the authenticated host's
	// explicitly configured demo policy. It does not assert MFA verification.
	PrimarySessionAuthorized bool
	Permissions              []string
	Grants                   []Grant
	// Aliases are verified, historical identity links loaded by AccessStore.
	// They are not accepted from HTTP input or used to grant a permission.
	Aliases []string
}
type actorKey struct{}

func (a Actor) HasProtectedAuthentication() bool {
	return a.MFA || a.PrimarySessionAuthorized
}

func clone(a Actor) Actor {
	a.Permissions = append([]string(nil), a.Permissions...)
	a.Grants = append([]Grant(nil), a.Grants...)
	a.Aliases = append([]string(nil), a.Aliases...)
	return a
}
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, clone(a))
}
func ActorFrom(ctx context.Context) (Actor, error) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	if !ok || strings.TrimSpace(a.OrganizationID) == "" || strings.TrimSpace(a.UserID) == "" || strings.TrimSpace(a.SessionID) == "" || !a.HasProtectedAuthentication() {
		return Actor{}, ErrDenied
	}
	return clone(a), nil
}
func (a Actor) Has(permission string) bool {
	for _, p := range a.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}
func (a Actor) Allows(permission, unit string) bool {
	if unit == "" || unit == "*" || !a.Has(permission) {
		return false
	}
	for _, g := range a.Grants {
		if g.Permission == permission && g.UnitID == unit {
			return true
		}
	}
	return false
}
func (a Actor) AudienceAllowed(permission, audience string) bool {
	if audience == "" || audience == "*" || !a.Has(permission) {
		return false
	}
	for _, g := range a.Grants {
		if g.Permission == permission && g.Audience == audience {
			return true
		}
	}
	return false
}
func Require(r *http.Request, permission string) (Actor, error) {
	a, e := ActorFrom(r.Context())
	if e != nil || !a.Has(permission) {
		return Actor{}, ErrDenied
	}
	return a, nil
}
func Decode(r *http.Request, v any) error {
	data, e := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if e != nil || len(data) > MaxBodyBytes {
		return errors.New("invalid or oversized request")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("one JSON value required")
	}
	return nil
}
func Write(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}
func Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
