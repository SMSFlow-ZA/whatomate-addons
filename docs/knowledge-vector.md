# Durable governed pgvector retrieval

`knowledge.VectorRepository` adds durable native cosine search to the existing
publication/page repository. The existing FTS retrieval remains FTS. The existing
in-memory candidate reranker remains a separate optional behavior. This adapter
does not rename either mechanism as pgvector.

## Host integration

The database administrator must install the real `vector` extension in `public`
using a reviewed PostgreSQL/pgvector image. Application migration and startup never
run `CREATE EXTENSION` and do not need extension-installation/superuser privileges.
Run the existing knowledge migration before the explicit vector migration:

```go
catalog, err := knowledge.NewPostgresRepository(db, tenant)
// Handle every error; omitted below only to show ordering.
err = catalog.Migrate(ctx)
vectors, err := knowledge.NewVectorRepository(db, tenant, knowledge.VectorPin{
    Model: reviewedExactEmbeddingModelGeneration,
    Dimensions: reviewedDimensions,
})
err = vectors.Migrate(ctx) // trusted migration/bootstrap phase only
err = vectors.Verify(ctx)  // startup verifies extension, profile and owned relations; no DDL
err = vectors.RegisterIndexer(ctx, trustedWorkerPrincipal)
```

Never derive the tenant, model pin or worker principal from an untrusted request.
`RegisterIndexer` is a host bootstrap port with no browser route. It does not
reenable a previously disabled identity. `DisableIndexer` revokes that identity;
both the initial read and post-inference commit check its durable grant. Scope SQL
roles and permissions through the trusted host installer. No host core tables or
case tables are read or written.

The immutable tenant profile pins the exact embedding model generation and
dimension count. It must identify the reviewed actual upstream model/version and
deployment, not merely a moving logical alias. Bind the trusted `PageEmbedder` and
query embedder to that same reviewed generation. Provider configuration and any
locality/international-processing approval remain host responsibilities; an API or
browser assertion is not evidence. The embedder interface supplies vectors, not a
verified model receipt, so trusted composition must establish model identity.

Dimensions are 1–2000, reserving compatibility with a future reviewed HNSW vector
index. Changing a pin causes `Migrate` and `Verify` to refuse the existing tenant
generation; it cannot silently mix/relabel stored embeddings. A model change needs
an explicit approved generation/reindex migration, which this initial adapter does
not implement.

## Trusted bounded indexing

Call `IndexNext(ctx, trustedWorkerPrincipal, trustedEmbedder) (indexed bool, err
error)` from a bounded server worker. One call processes at most one eligible page.
It reads only the latest currently effective publication, validates human approval,
scan/extraction provenance and the exact page manifest, and commits that read
transaction **before** calling the provider. No document body, page text, embedding,
credential or provider response should be logged by the host.

After inference the commit transaction locks publication writes briefly, including
new-version insertion, and rechecks the current edition, retirement/withdrawal,
worker grant, manifest and exact page. Retirement, supersession, erasure or worker
revocation during inference rejects the result. The provider call never occurs
under that lock or any database transaction. The insert is atomic and does not
overwrite a previously stored vector. Concurrent workers can compute a duplicate
page; only one durable row exists. Therefore run one bounded worker per tenant to
avoid unnecessary provider cost, and enforce external request/spending budgets.

Blank pages and pages over 16,000 UTF-8 bytes are ineligible for this bounded vector
adapter and do not block later pages. They remain governed source/FTS pages; there
is no truncation that would change citations. `false, nil` means no eligible page
currently needs indexing, not that every source page was embedded. Larger pages
need an explicitly approved chunking design; this implementation preserves original
page identities. Provider failures return errors and do not mark work successful.
The per-text limit is shared with the production embedding adapter; it does not
expand the provider's existing input or request/PII budget. Serialized transport
and gateway limits remain independently enforced. An unknown inference result
stores no fabricated vector; subsequent worker policy
must account for provider budgets and uncertainty.

Persisted rows bind tenant, document/version, manifest hash, original page number,
page hash, exact model, dimensions and native `public.vector`. The vector is
validated for dimension, finite float32 values and nonzero norm before insertion.
No endpoint accepts arbitrary vector content or caller-supplied scan/ACL claims.

## Retrieval and hybrid composition

`RetrieveVector(ctx, query, authorizer, queryEmbedding)` returns at most eight
native cosine-ranked governed page sources. The host supplies a trusted current
query timestamp. Every call checks the current authorizer and durable audience read
grant, then joins stored vectors to authoritative latest-effective publications and
their pages. Latest selection occurs **before** retirement, withdrawal or audience
filtering, so a newer withheld/unindexed/retired edition cannot resurrect an older
embedding. Tenant, model and dimensions remain mandatory filters. Stored manifest
and page hashes are checked against revalidated publication evidence before return.
Wrong/stale proof is denied rather than presented as an answer.

```go
retriever := ai.HybridRetriever{
    Catalog: catalog,
    Vectors: vectors,
    Embeddings: trustedPinnedEmbedder,
}
```

FTS remains first. If it has no result and `Vectors` is configured, the hybrid
retriever embeds only the authorized query and delegates native retrieval to the
vector repository; it does not re-embed source pages on each request. Denied or
malformed queries do not reach the embedding provider. Native repository/provider
errors propagate; a failed native search does not silently turn into an alternate
source grant. The existing answer generation must still revalidate selected sources
at its established grounding boundary, including withdrawal during model inference.
When `Vectors` is nil the prior governed-candidate reranker is retained.

The initial SQL uses exact pgvector cosine distance (`<=>`) and a tenant/generation
B-tree metadata index. It deliberately installs no approximate vector index and
makes no throughput/large-workload claim. PostgreSQL table locks during bounded
read/commit phases protect against concurrent source publication changes; capacity
and contention need deployment measurement. Any later HNSW/IVFFlat index requires
separate recall, ACL-filtering, dimension and load validation. See the
[upstream pgvector query/index documentation](https://github.com/pgvector/pgvector).

## Privacy and ownership

Runtime roles need SELECT, never UPDATE, on `addon_knowledge_vector_profiles`
and `addon_knowledge_vector_workers`. Verification, retrieval and indexing take
the shared transaction advisory policy fence `19631025` before reading either
table. `Migrate`, `RegisterIndexer` and `DisableIndexer` acquire its exclusive
transaction fence before policy writes. Readers hold the fence through their
bounded transaction; embedding providers run outside it. Vector transactions
explicitly select READ COMMITTED, independently of the database/session default. Reads
after a fence wait observe the just-committed worker revocation. Registering an
existing disabled worker never re-enables it.

This coordinates the reviewed owner APIs. An operator performing direct policy
SQL must first acquire `pg_advisory_xact_lock(19631025)` in the same transaction,
or stop/isolate all vector readers and workers. Uncoordinated privileged SQL is
outside this fence guarantee. Do not grant runtime policy UPDATE merely to enable
PostgreSQL row locks. The fence is global per database, so owner changes can wait
for unrelated tenants' bounded vector transactions; it is not held over inference.

Vectors contain derived source data and belong to the knowledge owner. A composite
foreign key points at the exact tenant/document/version/page with `ON DELETE
CASCADE`; the existing knowledge privacy owner already deletes those page and
publication rows. Erasure therefore removes their vectors atomically and an
in-flight indexing result cannot recreate the erased source. Profile/model and
trusted worker identities contain no page text or case narrative. No case metadata
is used to build policy embeddings. Upstream inference retention is a separate
deployment/provider obligation and is not claimed erased by a PostgreSQL delete.

## Verification

`VECTOR_DATABASE_URL` must identify an actual administrator-prepared pgvector test
database. These tests create/drop only uniquely named schemas, preserve the shared
extension and never reset databases. They skip if that explicit DSN is absent;
plain PostgreSQL success is not pgvector evidence.

Restricted-role tests additionally create/drop a unique synthetic LOGIN role and
its grants. They exercise real Verify/IndexNext/RetrieveVector with no policy
table/column writes, policy writer blocking, and revocation committed while a
reader waits. This requires a disposable administrator-controlled test database.

Targeted real PostgreSQL 17.11 / pgvector 0.8.7 tests cover durable nonlexical
retrieval, tenant/audience/read-revocation isolation, source supersession/retirement,
model/dimension mismatch, source-hash corruption, privacy FK cascade, bounded-page
progress, and rejection when source state or worker authorization changes during
inference. A one-connection provider-hook test proves inference occurs outside a
transaction. Unit tests cover invalid/zero/nonfinite embeddings and denied hybrid
queries. Targeted race tests and `go vet ./knowledge ./ai` also run; these checks do
not establish full host integration, deployment readiness or workload capacity.
