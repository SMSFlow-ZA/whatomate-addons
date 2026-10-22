package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func vectorFixture(t *testing.T) (*VectorRepository, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("VECTOR_DATABASE_URL")
	if dsn == "" {
		t.Skip("VECTOR_DATABASE_URL actual pgvector instance not configured")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("vector_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema+",public")
	u.RawQuery = query.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	tenant := "synthetic-tenant"
	catalog, _ := NewPostgresRepository(db, tenant)
	if err = catalog.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := NewVectorRepository(db, tenant, VectorPin{Model: "synthetic-pinned-model-v1", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = v.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return v, db, tenant
}

type vectorEmbedFunc func(context.Context, []string) ([][]float64, error)

func (f vectorEmbedFunc) Embed(ctx context.Context, values []string) ([][]float64, error) {
	return f(ctx, values)
}
func publishVectorFixture(t *testing.T, db *sql.DB, tenant string, d Document) {
	t.Helper()
	ctx := context.Background()
	r, _ := NewPostgresRepository(db, tenant)
	for _, g := range [][3]string{{"publisher", "publish", "*"}, {"reviewer", "review", "*"}, {"reader", "read", "staff"}, {"publisher", "retire", "*"}} {
		_, err := db.Exec(`INSERT INTO addon_knowledge_grants(tenant,principal,action,scope) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tenant, g[0], g[1], g[2])
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Approve(ctx, "reviewer", d); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, "publisher", d, verified(d), approval(d)); err != nil {
		t.Fatal(err)
	}
}
func vectorQuery() Query {
	return Query{Principal: "reader", Audience: "staff", Text: "concept with no lexical overlap", At: time.Now().UTC()}
}
func vectorAllow() Authorizer {
	return AuthorizeFunc(func(context.Context, string, string) bool { return true })
}
func indexVectorFixture(t *testing.T, v *VectorRepository) {
	t.Helper()
	ctx := context.Background()
	if err := v.RegisterIndexer(ctx, "indexer"); err != nil {
		t.Fatal(err)
	}
	indexed, err := v.IndexNext(ctx, "indexer", vectorEmbedFunc(func(context.Context, []string) ([][]float64, error) { return [][]float64{{1, 0}}, nil }))
	if err != nil || !indexed {
		t.Fatalf("trusted index failed: %v", err)
	}
}

func TestVectorPostgresDurabilityAuthorizationAndNativeSearch(t *testing.T) {
	v, db, tenant := vectorFixture(t)
	ctx := context.Background()
	d := fixture()
	publishVectorFixture(t, db, tenant, d)
	calls := 0
	provider := vectorEmbedFunc(func(context.Context, []string) ([][]float64, error) { calls++; return [][]float64{{1, 0}}, nil })
	if _, err := v.IndexNext(ctx, "browser", provider); !errors.Is(err, ErrDenied) || calls != 0 {
		t.Fatal("untrusted indexer reached provider")
	}
	indexVectorFixture(t, v)
	fresh, _ := NewVectorRepository(db, tenant, v.pin)
	if err := fresh.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := fresh.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0})
	if err != nil || len(got) != 1 || got[0].ManifestHash != ManifestHash(d) {
		t.Fatalf("durable native vector result missing: %v %v", got, err)
	}
	// Existing FTS has no match; native vector is not lexical search relabeled.
	lexical, err := v.catalog.Retrieve(ctx, vectorQuery(), vectorAllow())
	if err != nil || len(lexical) != 0 {
		t.Fatal("fixture unexpectedly lexical")
	}
	other, _ := NewVectorRepository(db, tenant+"-other", v.pin)
	if err = other.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	grant(t, db, tenant+"-other", "reader", "read", "staff")
	if got, err = other.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0}); err != nil || len(got) != 0 {
		t.Fatal("cross tenant vector result")
	}
	q := vectorQuery()
	q.Audience = "private"
	if got, err = fresh.RetrieveVector(ctx, q, vectorAllow(), []float64{1, 0}); err != nil || len(got) != 0 {
		t.Fatal("audience authorization bypass")
	}
	if _, err = db.Exec(`DELETE FROM addon_knowledge_grants WHERE tenant=$1 AND principal='reader'`, tenant); err != nil {
		t.Fatal(err)
	}
	if got, err = fresh.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0}); err != nil || len(got) != 0 {
		t.Fatal("revoked read grant survived")
	}
}

func TestVectorPostgresLatestWithdrawnNeverResurrectsOldEmbedding(t *testing.T) {
	v, db, tenant := vectorFixture(t)
	ctx := context.Background()
	d := fixture()
	publishVectorFixture(t, db, tenant, d)
	indexVectorFixture(t, v)
	newer := d
	newer.Version = "2"
	newer.Effective = time.Now().UTC().Add(-time.Minute)
	newer.Pages = []Page{{Number: 1, Text: "New synthetic edition", Method: "text", ExtractorRevision: "extract-v2"}}
	publishVectorFixture(t, db, tenant, newer)
	if got, err := v.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0}); err != nil || len(got) != 0 {
		t.Fatal("unindexed current version resurrected old vector")
	}
	indexVectorFixture(t, v)
	if err := v.catalog.Retire(ctx, "publisher", newer.ID, newer.Version); err != nil {
		t.Fatal(err)
	}
	if got, err := v.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0}); err != nil || len(got) != 0 {
		t.Fatal("retired current edition resurrected old vector")
	}
}

func TestVectorPostgresIndexProviderOutsideTransactionAndCommitCAS(t *testing.T) {
	for _, change := range []string{"retire", "supersede", "revoke", "erase"} {
		t.Run(change, func(t *testing.T) {
			v, db, tenant := vectorFixture(t)
			ctx := context.Background()
			d := fixture()
			publishVectorFixture(t, db, tenant, d)
			v.RegisterIndexer(ctx, "indexer")
			db.SetMaxOpenConns(1)
			provider := vectorEmbedFunc(func(ctx context.Context, _ []string) ([][]float64, error) {
				// One connection proves no read transaction remains open across inference.
				switch change {
				case "retire":
					if err := v.catalog.Retire(ctx, "publisher", d.ID, d.Version); err != nil {
						return nil, err
					}
				case "supersede":
					newer := d
					newer.Version = "2"
					newer.Effective = time.Now().UTC().Add(-time.Minute)
					publishVectorFixture(t, db, tenant, newer)
				case "revoke":
					if err := v.DisableIndexer(ctx, "indexer"); err != nil {
						return nil, err
					}
				case "erase":
					if _, err := db.ExecContext(ctx, `DELETE FROM addon_knowledge_publications WHERE tenant=$1`, tenant); err != nil {
						return nil, err
					}
				}
				return [][]float64{{1, 0}}, nil
			})
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if indexed, err := v.IndexNext(bounded, "indexer", provider); indexed || !errors.Is(err, ErrDenied) {
				t.Fatalf("stale inference committed: %v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM addon_knowledge_vectors WHERE tenant=$1`, tenant).Scan(&count); err != nil || count != 0 {
				t.Fatal("stale vector persisted")
			}
		})
	}
}

func TestVectorPostgresPinMismatchCorruptionAndPrivacyCascade(t *testing.T) {
	v, db, tenant := vectorFixture(t)
	ctx := context.Background()
	d := fixture()
	publishVectorFixture(t, db, tenant, d)
	indexVectorFixture(t, v)
	for _, pin := range []VectorPin{{Model: "other", Dimensions: 2}, {Model: v.pin.Model, Dimensions: 3}} {
		changed, _ := NewVectorRepository(db, tenant, pin)
		if err := changed.Verify(ctx); !errors.Is(err, ErrVectorProfile) {
			t.Fatal("model/dimension migration bypass")
		}
		if err := changed.Migrate(ctx); !errors.Is(err, ErrVectorProfile) {
			t.Fatal("migration mutated an existing generation")
		}
	}
	if _, err := db.Exec(`UPDATE addon_knowledge_vectors SET page_hash='corrupt' WHERE tenant=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := v.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0}); !errors.Is(err, ErrDenied) {
		t.Fatal("stale page hash accepted")
	}
	// The existing knowledge privacy owner deletes page/publication rows. Its FK
	// lineage also removes all derived vectors, never leaving a second content store.
	if _, err := db.Exec(`DELETE FROM addon_knowledge_pages WHERE tenant=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM addon_knowledge_vectors WHERE tenant=$1`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatal("privacy delete left derived vectors")
	}
}

func TestVectorPostgresRequiresActualInstalledExtension(t *testing.T) {
	// This assertion inspects extension ownership on the actual test database,
	// rather than deleting its shared administrator-installed extension.
	v, db, _ := vectorFixture(t)
	var version string
	if err := db.QueryRow(`SELECT extversion FROM pg_extension WHERE extname='vector'`).Scan(&version); err != nil || strings.TrimSpace(version) == "" {
		t.Fatal("actual vector extension required")
	}
	if err := v.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVectorPostgresBlankOrOversizePagesDoNotStarveOtherPages(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", 32769)} {
		t.Run(fmt.Sprintf("bytes_%d", len(text)), func(t *testing.T) {
			v, db, tenant := vectorFixture(t)
			d := fixture()
			d.Pages = []Page{{Number: 1, Text: text, Method: "text", ExtractorRevision: "extract-v1"}, {Number: 2, Text: "Synthetic usable page", Method: "text", ExtractorRevision: "extract-v1"}}
			publishVectorFixture(t, db, tenant, d)
			indexVectorFixture(t, v)
			got, err := v.RetrieveVector(context.Background(), vectorQuery(), vectorAllow(), []float64{1, 0})
			if err != nil || len(got) != 1 || got[0].Page.Number != 2 {
				t.Fatal("ineligible page starved bounded source indexing")
			}
		})
	}
}

func TestVectorPostgresVerifyRequiresOwnedRelationsWithoutRepairDDL(t *testing.T) {
	for _, table := range []string{"addon_knowledge_vectors", "addon_knowledge_vector_workers"} {
		t.Run(table, func(t *testing.T) {
			v, db, _ := vectorFixture(t)
			if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if err := v.Verify(context.Background()); err == nil {
				t.Fatal("missing native vector relation passed runtime verification")
			}
			var exists bool
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)`, table).Scan(&exists); err != nil || exists {
				t.Fatal("runtime verification repaired missing relation")
			}
		})
	}
}

func TestVectorPostgresProductionOversizeFirstPageCannotStarveValidSecondPage(t *testing.T) {
	v, db, tenant := vectorFixture(t)
	ctx := context.Background()
	d := fixture()
	d.Pages = []Page{{Number: 1, Text: strings.Repeat("x", 16001), Method: "text", ExtractorRevision: "extract-v1"}, {Number: 2, Text: "Synthetic usable second page", Method: "text", ExtractorRevision: "extract-v1"}}
	publishVectorFixture(t, db, tenant, d)
	if err := v.RegisterIndexer(ctx, "indexer"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	indexed, err := v.IndexNext(ctx, "indexer", vectorEmbedFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		calls++
		// Match the actual production provider's pre-network byte bound. Skipping an
		// oversized page must not truncate/relabel it or send it to that provider.
		if len(texts) != 1 || len(texts[0]) > 16000 {
			return nil, ErrDenied
		}
		if texts[0] != d.Pages[1].Text {
			return nil, ErrDenied
		}
		return [][]float64{{1, 0}}, nil
	}))
	if err != nil || !indexed || calls != 1 {
		t.Fatalf("oversized first page blocked production-compatible second page: %v", err)
	}
	got, err := v.RetrieveVector(ctx, vectorQuery(), vectorAllow(), []float64{1, 0})
	if err != nil || len(got) != 1 || got[0].Page.Number != 2 || got[0].Page.Text != d.Pages[1].Text {
		t.Fatal("bounded page provenance changed")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM addon_knowledge_vectors WHERE tenant=$1 AND page_number=1`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatal("oversized page was indexed")
	}
}
