# Local assisted-answer evaluation

The AI module is advisory. It retrieves its own approved current knowledge, calls
an explicitly configured generator, validates an exact source excerpt and citation,
then retrieves again before returning. HTTP also requires a trusted current-role
and exact-audience callback before and after inference, and current access to the
linked real case. Caller-provided sources, actor identities and labels are rejected.

`NewHTTPHandler(db, HTTPConfig)` does no migration. Run `SQLStore{DB: db}.Migrate`
explicitly at composition. Configure `Enabled`, tenant-specific `ServiceFor`,
`Authorize(ctx, actor, permission, audience)`, and
`Cases(ctx, actor, caseID, permission)`. Both callbacks must re-read current access.
Assistance requires `ai:assist`, exact `knowledge:read` audience and
`cases:work.assigned`; correction and handoff additionally require current
`ai:review`. The host should call `knowledge.SynchronizeActor` with its trusted
knowledge authorizer when constructing a tenant repository. Never reuse a
fixed tenant service for multiple organizations.

Routes under `/api/extensions/ai/`:

- `POST answers`: `case_id`, `audience`, `question`.
- `POST suggestions`: same inputs; host-owned `Classifier` and `Labels` required.
- `GET records/{id}`: scoped durable record, including exact source provenance.
- `POST records/{id}/correct` or `/handoff`: `expected_version`, `reason`.

Records and append-only versioned audit entries commit together in module-owned
PostgreSQL tables. Corrections retain the original answer and add human review;
none changes a case category, case assignment or sends a message. A handoff is a
durable request for human attention; the authorized case workflow assigns a human.
Unknown commit outcomes return persistence unavailable; reload a known record
before repeating a review. Answer creation currently has no idempotency key, so
an unknown outcome can leave a durable record whose ID was not delivered.

## Explicit synthetic development provider

`NewDevelopmentProvider(DevelopmentConfig{SyntheticDevelopment:true, ...})`
is a separate local rehearsal adapter. It permits only
`http://127.0.0.1:11434` and `http://host.docker.internal:11434`. It never enables
live carrier traffic, claims South African hosting attestation, downloads models,
uses a cloud fallback, inherits process proxy settings, or follows redirects.
The existing production `NewProvider` still requires HTTPS, exact allowlisting,
ZA country, deployment attestation and its bounded response/time limits.

Pinned installed models:

| Model | SHA-256 digest |
| --- | --- |
| qwen3:14b | bdbd181c33f2ed1b31c972991882db3cf4d192569092138a7d29e973cd9debe8 |
| nomic-embed-text:latest | 0a109f422b47e3a30ba2b10eca18548e944e8a23073ee3f3e947efcf3c45e59f |

Every local call verifies both installed digests through `/api/tags`. Native
`/api/chat` uses `think:false`, no streaming/tools, a strict schema, temperature
zero and bounded context/output. The answer schema selects task intent and a
bounded evidence index. The server extracts immutable compact source paragraphs
(maximum 900 bytes), and returns only the selected original paragraph and citation.
An independent second model call checks the question against only that compact
selected passage; topical resemblance alone is insufficient. Unrelated questions,
privileged actions and requests for missing material cause handoff. Explicit
requests to override authority also receive a deterministic refusal. That guard
supplements exact current authorization; it is not an access-control boundary.
The shared Service enforces the compactness limit and exact source substring check
for every generator. Model relevance decisions remain fallible and require
semantic evaluation. Classification uses a separate strict bounded host taxonomy and has
no execution authority. HTTP inference timeout is 110 seconds; frontend requests
allow 120 seconds.

`HybridRetriever` uses existing PostgreSQL FTS first. Only if that returns no
sources does it request bounded authorized current candidates from the same
repository, embed them with Nomic and return up to eight highest cosine scores.
It cannot retrieve an unauthorized page or resurrect a retired version. Embedding
responses require 768 finite dimensions and bounded inputs.

## Evidence and limits

Opt-in Go tests read an externally supplied evaluation corpus and write actual
model outputs outside this public checkout:
`AI_EVALUATION_CORPUS`, `AI_EVALUATION_OUTPUT`, `AI_EMBEDDING_OUTPUT` and
`AI_CLASSIFICATION_OUTPUT`. The model evaluation captures native raw responses,
actual elapsed times, source-hash checks, resulting source spans and handoffs.
No customer corpus or document is embedded in these tests.

On 2026-10-20 a 26-scenario external synthetic oracle run exercised 19 real Qwen
calls; seven scenarios without an authorized oracle source skipped inference.
The final run returned 17 exact-source excerpts and nine safe handoffs. Earlier
failed prompting runs were retained. Nomic produced actual 768-dimensional
vectors. These counts are mechanical observations, not a semantic acceptance
score. The oracle substitutes approval/scanning/retrieval and does not publish
anything; it is not evidence that full-stack document publication, actor-role
changes or case routing passed. Semantic review remains required. Package tests
cover source/authority boundaries, durable optimistic corrections, audit linkage,
revocation during inference and retrieval-first hybrid behavior.


A later evaluation against genuinely scanned, Poppler-extracted and separately
reviewed/published SQL sources exposed defects hidden by the hand-selected source
oracle: unrelated full-page quotations and avoidable handoffs for relevant
reviewer-conflict and intake-field questions. The old run is not semantic acceptance
evidence. The compact paragraph selector, independent relevance verifier and
bounded larger semantic candidate set were introduced to address those failures.
`TestActualPublishedModelEvaluation` reads the actual SQL repository and existing
durable audience grants without creating publications or changing permissions.
Its evidence explicitly distinguishes direct Service evaluation from authenticated
HTTP/session acceptance; the host must repeat API tests after rebuilding.


The final direct-SQL follow-up evaluated 28 questions using the real governed
publications and existing ER employee-audience grant: 11 compact substantive
answers (maximum 365 bytes) and 17 handoffs. All six original adversarial
questions, an unrelated opening-hours question and an absent attachment request
handed off. Reviewer-conflict and required-intake-field questions returned
specific relevant paragraphs. Other supported questions still handed off, so
useful coverage is partial. These are observed results, not a semantic acceptance
score; every oracle-role scenario was executed under the same existing ER scope.
Authenticated API evaluation against the rebuilt host remains separate.

Fresh package verification passed `go test -count=1 -race -tags integration ./ai`
and `go vet ./ai`. The redirect/model/timeout regression passed 20 race-enabled
repetitions. Its redirect and model-mismatch branches now allow one second for
TLS setup; the intentional timeout branch retains its 10ms bound. Previously the
shared 10ms limit could time out before receiving a redirect and falsely report
that a redirect had been followed.
