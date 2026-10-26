package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrimarySessionAuthorizationPreservesScopeAndActualMFA(t *testing.T) {
	a := Actor{OrganizationID: "org", UserID: "user", SessionID: "session", PrimarySessionAuthorized: true, Permissions: []string{"knowledge:read"}, Grants: []Grant{{Permission: "knowledge:read", Audience: "demo-staff"}}}
	ctx := WithActor(context.Background(), a)
	a.Grants[0].Audience = "other"
	got, err := ActorFrom(ctx)
	if err != nil || !got.HasProtectedAuthentication() || got.MFA || !got.AudienceAllowed("knowledge:read", "demo-staff") || got.AudienceAllowed("knowledge:read", "other") || got.Has("cases:read") {
		t.Fatalf("primary session changed proof or scope: %+v %v", got, err)
	}
	got.Grants[0].Audience = "other"
	again, _ := ActorFrom(ctx)
	if !again.AudienceAllowed("knowledge:read", "demo-staff") {
		t.Fatal("returned grants alias context")
	}
}

func TestPrimarySessionStillRequiresTrustedCompleteIdentity(t *testing.T) {
	base := Actor{OrganizationID: "org", UserID: "user", SessionID: "session", PrimarySessionAuthorized: true}
	for _, field := range []string{"organization", "user", "session", "authorization"} {
		t.Run(field, func(t *testing.T) {
			a := base
			switch field {
			case "organization":
				a.OrganizationID = " "
			case "user":
				a.UserID = ""
			case "session":
				a.SessionID = ""
			case "authorization":
				a.PrimarySessionAuthorized = false
			}
			if _, err := ActorFrom(WithActor(context.Background(), a)); err == nil {
				t.Fatal("incomplete actor accepted")
			}
		})
	}
	r := httptest.NewRequest("POST", "/?primary_session_authorized=true", strings.NewReader(`{"primary_session_authorized":true,"user_id":"user","organization_id":"org"}`))
	r.Header.Set("X-Primary-Session-Authorized", "true")
	if _, err := Require(r, "knowledge:read"); err == nil {
		t.Fatal("HTTP supplied authorization accepted")
	}
}
