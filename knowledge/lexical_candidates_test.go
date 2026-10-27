package knowledge

import (
	"context"
	"testing"
	"time"
)

func TestPostgresNaturalQuestionCandidatesRetainCurrentScopeAndWithdrawal(t *testing.T) {
	r, db, tenant := postgresFixture(t)
	ctx := context.Background()
	for _, action := range []string{"publish", "retire"} {
		grant(t, db, tenant, "publisher", action, "*")
	}
	grant(t, db, tenant, "reviewer", "review", "*")
	grant(t, db, tenant, "reader", "read", "staff")
	publish := func(id, version, audience, text string, effective time.Time) Document {
		t.Helper()
		d := fixture()
		d.ID = id
		d.Version = version
		d.Audiences = []string{audience}
		d.Pages[0].Text = text
		d.Effective = effective
		a, err := r.Approve(ctx, "reviewer", d)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Publish(ctx, "publisher", d, verified(d), a); err != nil {
			t.Fatal(err)
		}
		return d
	}
	now := time.Now().UTC()
	publish("attendance", "1", "staff", "Old synthetic repeated lateness guidance.", now.Add(-2*time.Hour))
	current := publish("attendance", "2", "staff", "Repeated lateness: record observed dates. Consequential disciplinary decisions require human review.", now.Add(-time.Hour))
	publish("restricted", "1", "reviewers", "Repeated lateness: restricted reviewer marker.", now.Add(-time.Hour))
	publish("unrelated", "1", "staff", "Synthetic cafeteria opening hours.", now.Add(-time.Hour))
	q := Query{Principal: "reader", Audience: "staff", Text: "Could you explain the process for repeated lateness?", At: now}
	auth := AuthorizeFunc(func(context.Context, string, string) bool { return true })
	strict, err := r.Retrieve(ctx, q, auth)
	if err != nil || len(strict) != 0 {
		t.Fatal("fixture must miss conjunctive full text retrieval")
	}
	got, err := r.RetrieveCandidates(ctx, q, auth)
	if err != nil || len(got) != 1 || got[0].DocumentID != current.ID || got[0].Version != "2" || got[0].Page != current.Pages[0] || got[0].ManifestHash != ManifestHash(current) {
		t.Fatalf("governed current lexical candidate failed: %v", err)
	}
	if err = r.Retire(ctx, "publisher", current.ID, current.Version); err != nil {
		t.Fatal(err)
	}
	got, err = r.RetrieveCandidates(ctx, q, auth)
	if err != nil || len(got) != 0 {
		t.Fatal("withdrawn latest source resurrected older version or unrelated page")
	}
	publish("attendance", "3", "staff", current.Pages[0].Text, now.Add(-time.Minute))
	if _, err = db.ExecContext(ctx, `DELETE FROM addon_knowledge_grants WHERE tenant=$1 AND principal='reader'`, tenant); err != nil {
		t.Fatal(err)
	}
	got, err = r.RetrieveCandidates(ctx, q, auth)
	if err != nil || len(got) != 0 {
		t.Fatal("host authorizer bypassed revoked durable grant")
	}
}
