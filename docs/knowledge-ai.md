# Governed knowledge and assisted answers

These are public reusable domain packages, not production-ready integrations.
They contain synthetic tests only. They do not scan files, run OCR, create vector
embeddings, or prove hosting locality. All gates below require host integration
and release acceptance before enabling the feature.

## Publication and quarantine

Keep uploads in a restricted quarantine outside this repository. A trusted adapter
must hash the exact original bytes, obtain a successful malware scan and complete
extraction, and supply `knowledge.Evidence` tied to that SHA-256 hash and scanner
revision. Failure, timeout, unsupported format, partial extraction or absent
evidence must leave the upload quarantined. Never expose quarantine to search.
The evidence struct is a trusted adapter contract, not proof that a scanner ran.

Extraction must preserve every original page in sequence, including blank pages,
and record `text` or `ocr` and the extractor/OCR revision per page. Adapters must
verify page completeness and the text-to-original-page relationship. The domain
checks sequence and required provenance; it cannot independently validate OCR.

A host-authorized human reviewer approves `ManifestHash(document)`, which binds
original content, content hash, identifier, version, page text/provenance,
audiences, effective time and optional withdrawal time. Any change requires new
review. `Publish` denies absent or mismatched scan, extraction or review evidence,
and snapshots the input to prevent later mutation. Hosts must enforce reviewer
permissions and retain immutable approval and processing audit evidence.

`Catalog` is a concurrency-safe **in-memory development adapter**. It rejects
replacement of an existing identifier/version or ambiguous effective times. The
latest effective version supersedes prior versions; withdrawal never resurrects
an older version. Scheduled withdrawal is exclusive: the source is unavailable
at that instant. Explicit withdrawal is irreversible in this instance.
Production requires durable version history, quarantine, processing and approval
records, withdrawal propagation and an audited authorized administration path.

## Retrieval and answers

`Catalog.Retrieve` requires a principal, audience, explicit time and an authorizer.
No authorizer or failed authorization returns no sources. It filters current,
reviewed publications by their audience before returning matched pages. This is
bounded, case-insensitive literal substring matching (20 pages), **not vector or
semantic retrieval**; a natural-language question may miss. A future index adapter
must revalidate publication/version/audience/withdrawal against authoritative
state before returning each result. Tenant isolation and document ACL checks must
be implemented by the host authorizer and catalog tenancy boundaries.

Use `ai.Service.Answer` as the public answering boundary. It retrieves sources
itself, calls the configured generator only when sources exist, and rechecks
current authorization and availability after generation. Citations are resolved
from the actual retrieved reference to the original document hash, approved
manifest, version and page provenance. A miss, provider failure, inference,
ungrounded result, unsupported answer or invented citation returns one fixed
response and `Handoff=true`. The service returns only an exact excerpt present
in at least one cited approved page. This deliberately conservative rule does
not attempt semantic entailment, synthesis or arbitrary generated prose. Hosts
must still assess excerpt completeness and suitability for their use case.

`Generator.Generate`/`Provider.Generate` are lower-level provider interfaces, not
an authorized user answering endpoint. Do not expose caller-supplied `Request`
sources as a public API. Treat source text as untrusted data. A prompt-injected
source cannot grant tool access: the provider sends no tools, rejects tool-call
responses, and the domain exposes no execution or authorization APIs.

Classification output is a `ClassificationSuggestion` only. `Apply` always
returns `ErrHumanApprovalRequired`; an independently authorized, audited human
workflow must approve any label assignment. Model labels never authorize access,
change permissions, execute tools or make a final protected workflow decision.
The host supplies the actual handoff queue and approved user-facing delivery.

## Provider deployment and egress gate

`NewProvider` accepts only HTTPS endpoints exactly present in `AllowedEndpoints`,
with `Country=ZA`, a non-empty externally reviewed `DeploymentAttestation`
reference, an explicit model identifier, prompt revision, timeout (at most 60
seconds), and response limit (at most 1 MiB). It disables environment proxies and
redirects, checks the exact destination on every transport call, caps serialized
source input and response reads, and rejects a response naming another model.
There is one endpoint and no retry/fallback provider. API keys belong in external
secret configuration. Neither requests, errors nor test fixtures log keys.

The attestation reference is configuration supplied by a trusted operator; it is
**not independently verified by this package**. A South African hostname or
`Country=ZA` is not evidence of South African processing. Production acceptance
must independently verify the exact deployment/model and current attestation,
including inference, retention, backups, telemetry, abuse monitoring and support
access. Pin deployment revisions, prohibit external failover, and re-attest after
changes. The pinned model response string is an additional consistency check,
not cryptographic proof of provider identity or residency.

External network controls are a mandatory separate release gate: enforce
South-Africa-only approved egress and DNS/IP policy; block unapproved/public
fallback routes, redirects, proxies and side channels; validate TLS and secret
handling; test actual deployment behavior and failure paths. The application
endpoint allowlist does not pin DNS addresses or independently establish network
locality. Production must remain disabled until those controls are evidenced.

## Verification and remaining integrations

Tests exercise quarantine/publication denial, approval binding, immutability,
audience/current-version/scheduled and explicit withdrawal, invented citations,
unsupported model prose, empty retrieval, withdrawal during generation,
classification authority denial, TLS HTTP completion, model mismatch, response
size, redirect and timeout denial. Run `go test ./...`, `go test -race ./...` and
`go vet ./...` using the repository's approved Go toolchain.

Still required: real malware/extraction/OCR adapters and evidence verification;
production authorization synchronization; authenticated reviewer/admin
workflows and audit history; production retrieval quality acceptance; human
handoff integration; provider deployment attestation review and expiry/revocation
handling; secret management and external egress/locality acceptance. No live
provider, customer documents, external model call or production deployment is
part of this development implementation.

## PostgreSQL adapter

`knowledge.NewPostgresRepository(db, tenant)` provides durable extension-owned
storage with four tables: `addon_knowledge_publications`,
`addon_knowledge_approvals`, `addon_knowledge_pages` and `addon_knowledge_grants`. `Migrate` creates only
these tables, under a transaction-scoped advisory migration lock. Provision
migrations with a dedicated database role and keep runtime grants least-privileged.
Every repository instance requires a tenant. All queries include that tenant.
Original content and extracted text are sensitive data: configure database
encryption, access, retention and backups before production use.

The host's authenticated RBAC bridge provisions grant rows for principal/action/
scope. Actions are `review`, `publish`, `retire` with scope `*`, and `read` with an
exact audience scope. No model or unauthenticated endpoint may write these rows.
Principal strings passed to repository methods must come from authenticated
server-side identity, never a model or untrusted request field. The repository
has no public grant-writing method.

Call `Approve(ctx, reviewer, document)` from the authenticated human review path.
It checks the current review grant and persists the exact manifest approval.
`Publish(ctx, publisher, document, evidence, approval)` checks publisher and
reviewer permissions and requires that persisted exact approval before inserting
an immutable version. Merely naming an authorized reviewer is insufficient.
`Retire` checks its own current grant and atomically records the retirement actor
and timestamp. Grant rows are locked `FOR SHARE` until commit, so a concurrent
permission deletion/update serializes with these operations. PostgreSQL
uniqueness constraints reject duplicate versions or ambiguous effective times.
No API updates an already published payload or approval.

Durable pages have a generated PostgreSQL `tsvector` and GIN index. Durable retrieval uses `plainto_tsquery` with the `simple` dictionary and `ts_rank_cd` ordering, capped at 20 pages. This is actual database full-text search; it does not load the entire corpus into an in-memory search adapter. Indexed page text and extraction metadata must exactly match the approved manifest page before being returned. It is not hybrid/vector retrieval: no embeddings, vector index or embedding revision is configured, and none is simulated. A hybrid adapter remains disabled until a real residency-approved embedding provider, pinned model/revision and vector index with the same authorization/current-source filters are implemented and accepted.

Durable `Retrieve` requires both the host authorizer and the transaction-locked
`read` grant; it selects latest versions before filtering retirement/audiences,
and reconstructs and verifies the stored content/manifest/evidence before
returning provenance. `ai.Service.Catalog` accepts the `Retriever` interface, so
the durable adapter can replace the memory catalog. The host must supply current
time; user-controlled historical query times are not appropriate for the public
answering endpoint. Handoff delivery still requires its own final authorization
check at the host boundary if delivery is asynchronous.

Set `KNOWLEDGE_DATABASE_URL` for real PostgreSQL integration tests. Without it,
those tests skip explicitly; ordinary unit tests remain available. Integration
tests use synthetic per-test tenants and clean their own rows. They cover durable
readback, tenant and read isolation, forged human approval denial, retirement,
and concurrent publish/retire against grant revocation. Durable storage is now
implemented; production RBAC synchronization, operational DB controls, migration
rollout, scanner/OCR integration, approval UI, and the external deployment gates
remain acceptance work.

## Authenticated lifecycle HTTP and durable processing queue

`NewHTTPHandler(db, HTTPConfig{Enabled: true, Authorize: currentHostResolver})`
registers the knowledge module under `/api/extensions/knowledge`. Construction
performs no DDL. Run `MigrateHTTP` explicitly with the migration role first.
The host must supply a current role and exact audience resolver; a missing
resolver disables the module. Each action also requires a trusted `api.Actor`,
MFA session, exact permission and audience grants. Permissions are
`knowledge:upload`, `knowledge:review`, `knowledge:publish`, `knowledge:retire`
and `knowledge:read`. All audiences of an upload must be allowed for the action.
Reviewer permission is resolved again at publication; publisher and reviewer
must be different people. A role name or administrator flag grants no access.

Upload `POST /uploads` as multipart `file` (at most 20 MiB original bytes) and
`metadata` (at most 32 KiB strict JSON): `document_id`, `version`, `filename`,
`media_type`, `audiences`, `effective_at` and optional `withdraw_at`. The bounded
original bytes, canonical metadata hashes and actor/tenant binding persist in
`addon_knowledge_jobs`. The response is a queued job identifier. There is no
HTTP scan/evidence/extraction completion endpoint. Status and immutable review
pages are available through `GET /jobs` and `GET /jobs/{id}` to the scoped
uploader or scoped reviewers, publishers and retirement operators.

A separately deployed trusted controller uses `NewProcessingQueue(db).Claim`
to obtain original bytes, exact hashes and a random lease token, runs the
pinned isolated document worker, verifies its receipt, then calls
`CompleteWithReceipt` with pages, evidence and the receipt linkage. This stores
job/input/metadata/output hashes, worker profile, image digest and scanner
definitions revision. The controller verifies the worker's own format metadata
hash separately from the knowledge upload metadata hash. The lower-level
`Complete` method is a trusted adapter/testing port; neither completion method
is exposed to HTTP. No Docker socket is needed or permitted in the web app.
Claims last one second to fifteen minutes. Completion verifies the exact stored
bytes, metadata, tenant, original actor and unexpired one-use lease. Replay,
changed hashes and unsuccessful scan/extraction are denied. `Fail` records only
fixed non-sensitive outcome codes. Expired leases and indeterminate worker
outcomes become `unknown`; neither unknown nor failed jobs are automatically
replayed or eligible for review/publication. An operator must investigate and
upload a fresh version when appropriate.

`POST /jobs/{id}/approve`, `/publish` and `/retire` accept only
`{"expected_version": n}`. Review loads the worker-created artifact from durable
storage and binds approval to its exact manifest; clients cannot submit pages or
evidence. Mutations return a conflict for stale versions and inappropriate
states. Publication or final persistence failures return
`operation_outcome_unknown`; callers must inspect durable status before retrying.
Domain publication, approval or retirement and the corresponding job status commit
atomically in one transaction. A lost commit acknowledgement still requires
reading authoritative state rather than inferring rollback from a failed response.

`GET /search?audience=...&q=...` returns current full-text sources with original
page, approved manifest and content hash citations. `GET /download?audience=...
&document_id=...&version=...` revalidates current version, audience, effective and
withdrawal dates, retirement and manifest before serving the original attachment.
Every request requires the same authenticated exact audience access. Scheduled
expiry and retirement exclude both retrieval and downloads, including forms.
KnowledgePage provides upload, manual status refresh, exact-page review,
publication, withdrawal, citation search and authenticated original downloads.
Quarantine and originals in PostgreSQL remain sensitive: provision DB encryption,
role restrictions, storage quotas, retention, backups and operational monitoring
before production acceptance. Full-text retrieval is implemented; semantic/vector
retrieval remains disabled pending the separately documented provider gates.


Trusted assisted-answer adapters may call `SynchronizeActor(ctx, db, cfg, actor)`
with the actor obtained from `api.ActorFrom`. This replaces durable knowledge
grants with the intersection of that actor's exact permissions/audiences and
the current host resolver. Call it before retrieval and recheck current host
access after inference. It is not an administration API or an HTTP body format.
`RetrieveCandidates` preserves the same durable authorization and source checks
while broadening lexical question recall to OR terms, removing common English
question stop words, and capping queries at 64 distinct terms and 20 pages. It
provides lexical candidates for a separately configured reranker; it does not
claim semantic search or an embedding implementation.

Verified employee channels use EnsureVerifiedReader for one exact read audience with a current eligibility authorizer on each retrieval and after inference. This trusted owner port grants no operator MFA, review or publication permission. Its employee: principal namespace is separate from operator UUID identities.
