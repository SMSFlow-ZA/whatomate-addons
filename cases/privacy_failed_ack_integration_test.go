//go:build integration

package cases

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/url"
	"os"
	"testing"
	"time"
)

func failedAckPrivacyRepository(t *testing.T) *SQLRepository {
	t.Helper()
	dsn := os.Getenv("CASES_DATABASE_URL")
	if dsn == "" {
		t.Skip("CASES_DATABASE_URL required")
	}
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("case_ack_privacy_%d", time.Now().UnixNano())
	if _, e = base.Exec(`CREATE SCHEMA ` + schema); e != nil {
		base.Close()
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := sql.Open("pgx", u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	r := &SQLRepository{DB: db}
	if e = r.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = r.MigrateOutboxDelivery(context.Background()); e != nil {
		t.Fatal(e)
	}
	return r
}

func TestFailedAcknowledgementPrivacyPreservesExternalLineage(t *testing.T) {
	for _, mode := range []string{"provider-reference", "authenticated-receipt", "never-dispatched-held"} {
		t.Run(mode, func(t *testing.T) {
			r := failedAckPrivacyRepository(t)
			ctx := context.Background()
			result, e := (Service{Repository: r}).Intake(ctx, IntakeRequest{OrganizationID: "org", UnitID: "zone", ContactID: "employee", Key: "case", Narrative: "A synthetic case narrative."})
			if e != nil || result.Outcome != Committed {
				t.Fatal(result, e)
			}
			now := time.Now().UTC()
			claim, ok, e := r.ClaimOutbox(ctx, "org", "fixture", now, now.Add(time.Minute))
			if e != nil || !ok {
				t.Fatal(ok, e)
			}
			if e = r.BindOutboxRoute(ctx, claim, "approved-account"); e != nil {
				t.Fatal(e)
			}
			if mode == "never-dispatched-held" {
				if e = r.FinishOutbox(ctx, claim, "held", ""); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = r.FinishOutbox(ctx, claim, "accepted", "provider-proof"); e != nil {
					t.Fatal(e)
				}
				if mode == "provider-reference" {
					if _, e = r.DB.Exec(`UPDATE addon_cases_delivery SET state='failed' WHERE id=$1`, claim.Message.ID); e != nil {
						t.Fatal(e)
					}
				} else {
					receipt, verify := signedCaseReceipt(api.Receipt{Tenant: "org", Account: "approved-account", IntentID: claim.Message.ID, ProviderID: "provider-proof", ReceiptID: "authenticated-failure", ObservedAt: now, Outcome: "failed"})
					if e = r.ReconcileOutbox(ctx, receipt, verify); e != nil {
						t.Fatal(e)
					}
					// A restored projection can omit the provider reference while retaining its
					// immutable authenticated receipt. That evidence still prevents false expiry.
					if _, e = r.DB.Exec(`UPDATE addon_cases_delivery SET provider_id='' WHERE id=$1`, claim.Message.ID); e != nil {
						t.Fatal(e)
					}
				}
			}
			tx, e := r.DB.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			actor := api.Actor{OrganizationID: "org", UserID: "privacy-reviewer", SessionID: "current", MFA: true, Permissions: []string{string(ReadTriage)}, Grants: []api.Grant{{Permission: string(ReadTriage), UnitID: "zone"}}}
			scope := governance.Scope{OrganizationID: "org", UnitID: "zone"}
			records, e := (PrivacyAdapter{}).Discover(ctx, tx, actor, scope, "employee")
			if e != nil {
				t.Fatal(e)
			}
			want := 2
			if mode == "never-dispatched-held" {
				want = 1
			}
			if len(records) != want {
				t.Fatalf("%s: inventory=%+v want %d", mode, records, want)
			}
			entries, e := governance.Plan(governance.SubjectRequest{ID: "delete", Scope: scope, SubjectID: "employee", Kind: governance.Delete, Approved: true}, records, []governance.Rule{{Source: governance.Case, Approved: true}, {Source: governance.Provider, Approved: true}}, nil, time.Now().UTC())
			if e != nil {
				t.Fatal(e)
			}
			for _, entry := range entries {
				if entry.Source == governance.Provider && (entry.State != governance.AwaitingExpiry || entry.ConfirmationReference != "") {
					t.Fatal("provider retention was falsely claimed deleted", entry)
				}
			}
		})
	}
}
