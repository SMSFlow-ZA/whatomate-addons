package knowledge

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestReferenceCatalogReturnsStableBoundedSources(t *testing.T) {
	c := NewCatalog()
	var at time.Time
	for i := 0; i < 30; i++ {
		d := fixture()
		d.ID = fmt.Sprintf("doc-%02d", i)
		at = d.Effective.Add(time.Hour)
		p, err := Publish(d, verified(d), approval(d))
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	q := Query{Principal: "staff", Audience: "staff", Text: "policy", At: at}
	auth := AuthorizeFunc(func(context.Context, string, string) bool { return true })
	for n := 0; n < 10; n++ {
		got, err := c.Retrieve(context.Background(), q, auth)
		if err != nil || len(got) != 20 {
			t.Fatalf("retrieval %v %v", got, err)
		}
		for i, s := range got {
			if s.DocumentID != fmt.Sprintf("doc-%02d", i) {
				t.Fatalf("nondeterministic capped source order: index %d got %s", i, s.DocumentID)
			}
		}
	}
}
