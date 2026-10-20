//go:build integration

package reports

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"os"
	"testing"
	"time"
)

func TestActivityProjectionRejectsRewrittenEventAndReplaysOnce(t *testing.T) {
	dsn := os.Getenv("API_DATABASE_URL")
	if dsn == "" {
		t.Skip("API_DATABASE_URL required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	s := Store{DB: db}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	org := "activity-report-" + time.Now().Format("150405.000000000")
	a := api.Actor{OrganizationID: org, UserID: "r", SessionID: "s", MFA: true, Permissions: []string{"reports:operational"}, Grants: []api.Grant{{Permission: "reports:operational", UnitID: "u"}}}
	f := ActivityFact{ID: "stable", OrganizationID: org, UnitID: "u", Kind: "inbound", Outcome: "received", OccurredAt: time.Now().UTC()}
	result, e := s.projectActivity(ctx, a, "operational", []ActivityFact{f, f})
	if e != nil || len(result) != 1 {
		t.Fatal("replay", result, e)
	}
	f.Zone = "rewrite"
	if _, e = s.projectActivity(ctx, a, "operational", []ActivityFact{f}); e == nil {
		t.Fatal("rewritten usage history accepted")
	}
	result, e = s.projectActivity(ctx, a, "operational", nil)
	if e != nil || len(result) != 0 {
		t.Fatal("stale scope returned", result, e)
	}
}
