package knowledge

import (
	"context"
	"testing"
	"time"
)

func TestVerifiedReaderCannotCreateOperatorOrWildcardGrants(t *testing.T) {
	for _, v := range []struct{ principal, audience string }{{"user", "employees"}, {"employee:", "employees"}, {"employee:e", "*"}} {
		if EnsureVerifiedReader(context.Background(), nil, "o", v.principal, v.audience, AuthorizeFunc(func(context.Context, string, string) bool { return true })) == nil {
			t.Fatal("unsafe reader", v)
		}
	}
}

func TestVerifiedChannelReaderRechecksEligibilityWithoutOperatorAuthority(t *testing.T) {
	repo, db, tenant := postgresFixture(t)
	ctx := context.Background()
	d := fixture()
	grant(t, db, tenant, "publisher", "publish", "*")
	grant(t, db, tenant, "reviewer", "review", "*")
	if _, e := repo.Approve(ctx, "reviewer", d); e != nil {
		t.Fatal(e)
	}
	if e := repo.Publish(ctx, "publisher", d, verified(d), approval(d)); e != nil {
		t.Fatal(e)
	}
	eligible := true
	auth := AuthorizeFunc(func(_ context.Context, p, a string) bool { return eligible && p == "employee:123" && a == "staff" })
	if e := EnsureVerifiedReader(ctx, db, tenant, "employee:123", "staff", auth); e != nil {
		t.Fatal(e)
	}
	q := Query{Principal: "employee:123", Audience: "staff", Text: "synthetic", At: time.Now()}
	if v, e := repo.Retrieve(ctx, q, auth); e != nil || len(v) != 1 {
		t.Fatal(v, e)
	}
	eligible = false
	if v, e := repo.Retrieve(ctx, q, auth); e != nil || len(v) != 0 {
		t.Fatal("revoked reader", v, e)
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM addon_knowledge_grants WHERE tenant=$1 AND principal='employee:123' AND action<>'read'`, tenant).Scan(&n); e != nil || n != 0 {
		t.Fatal("operator authority", n, e)
	}
}
