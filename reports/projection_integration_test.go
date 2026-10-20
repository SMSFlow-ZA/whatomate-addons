//go:build integration

package reports

import (
	"context"
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"testing"
	"time"
)

func TestProjectionReplayAndScopeRemoval(t *testing.T) {
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
	org := "reports-" + time.Now().Format("150405.000000000")
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	events := []cases.ReportFact{{OrganizationID: org, UnitID: "u", CaseID: "c", Version: 1, From: cases.Draft, To: cases.Submitted, OccurredAt: at, RecordedAt: at, Snapshot: cases.Classification{Zone: "z", Department: "d", CaseType: "t"}}}
	actor := api.Actor{OrganizationID: org, UserID: "reviewer", SessionID: "s", MFA: true, Permissions: []string{"reports:operational"}, Grants: []api.Grant{{Permission: "reports:operational", UnitID: "u"}}}
	f, e := s.Project(ctx, actor, "operational", events, at.Add(time.Hour))
	if e != nil || len(f.Cases) != 1 {
		t.Fatal(f, e)
	}
	if _, e = s.Project(ctx, actor, "operational", events, at.Add(time.Hour)); e != nil {
		t.Fatal("replay", e)
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM addon_report_events WHERE organization_id=$1`, org).Scan(&n); e != nil || n != 1 {
		t.Fatal("duplicate event", n, e)
	}
	f, e = s.Project(ctx, actor, "operational", nil, at.Add(time.Hour))
	if e != nil || len(f.Cases) != 0 {
		t.Fatal("previous scope leaked", f, e)
	}
	changed := append([]cases.ReportFact(nil), events...)
	changed[0].Snapshot.Zone = "rewritten"
	if _, e = s.Project(ctx, actor, "operational", changed, at.Add(time.Hour)); e == nil {
		t.Fatal("history overwritten")
	}
	second := events[0]
	second.Version = 2
	second.From = cases.Submitted
	second.To = cases.Resolved
	second.OccurredAt = at.Add(time.Minute)
	second.RecordedAt = second.OccurredAt
	if _, e = s.Project(ctx, actor, "operational", append(events, second), at.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	f, e = s.Project(ctx, actor, "operational", events, at.Add(time.Hour))
	if e != nil || len(f.Cases) != 1 || !f.Cases[0].ResolvedAt.IsZero() {
		t.Fatal("restored source inherited future projection", f, e)
	}
	actor.Grants = nil
	if _, e = s.Project(ctx, actor, "operational", events, at); e == nil {
		t.Fatal("unscoped facts accepted")
	}
}
