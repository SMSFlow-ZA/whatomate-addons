package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrustedActorIsDetachedAndRequiresMFA(t *testing.T) {
	actor := Actor{OrganizationID: "org", UserID: "user", SessionID: "session", MFA: true, Permissions: []string{"cases:read"}, Grants: []Grant{{Permission: "cases:read", UnitID: "alpha"}}}
	ctx := WithActor(context.Background(), actor)
	actor.Permissions[0] = "cases:delete"
	actor.Grants[0].UnitID = "beta"
	got, err := ActorFrom(ctx)
	if err != nil || !got.Has("cases:read") || !got.Allows("cases:read", "alpha") || got.Allows("cases:read", "beta") {
		t.Fatalf("trusted context changed: %+v %v", got, err)
	}
	got.Grants[0].UnitID = "beta"
	again, _ := ActorFrom(ctx)
	if !again.Allows("cases:read", "alpha") {
		t.Fatal("returned grants alias trusted context")
	}
	actor.MFA = false
	if _, err := ActorFrom(WithActor(context.Background(), actor)); err == nil {
		t.Fatal("password-only actor accepted")
	}
	if _, err := ActorFrom(context.Background()); err == nil {
		t.Fatal("missing actor accepted")
	}
}

func TestRequireCannotReadBodyIdentityOrUseImplicitAdmin(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"user_id":"admin","mfa":true}`))
	if _, err := Require(r, "cases:read"); err == nil {
		t.Fatal("body identity accepted")
	}
	r = r.WithContext(WithActor(r.Context(), Actor{OrganizationID: "org", UserID: "admin", SessionID: "s", MFA: true, Permissions: []string{"admin"}}))
	if _, err := Require(r, "cases:read"); err == nil {
		t.Fatal("admin bypass accepted")
	}
}

func TestDecodeRejectsUnknownTrailingAndOversizedBodies(t *testing.T) {
	for _, body := range []string{`{"name":"ok","actor":"admin"}`, `{"name":"ok"} {}`, strings.Repeat(" ", MaxBodyBytes) + `{"name":"ok"}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var v struct {
			Name string `json:"name"`
		}
		if Decode(r, &v) == nil {
			t.Fatalf("invalid input accepted (%d bytes)", len(body))
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"ok"}`))
	var v struct {
		Name string `json:"name"`
	}
	if err := Decode(r, &v); err != nil || v.Name != "ok" {
		t.Fatal(err)
	}
}
