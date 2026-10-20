# Implementation status

Development source. No production release, external model evaluation or customer
acceptance is claimed. Packages own their persistence and have no host internal
imports. All examples are synthetic.

| Component | Implemented boundary | Remaining integration |
| --- | --- | --- |
| directory | Bounded XLSX, optional isolated XLS worker, preview/replacement/revocation/freshness, PostgreSQL CAS/audit | Authenticated approval UI, scheduling, trusted roster delivery and production worker supervisor |
| cases | Lifecycle, assigned/triage/subject policy, conflict routing, durable atomic intake/dedupe/outbox/audit, CAS and unknown-commit reconciliation | Narratives/attachments, draft/reassignment, current-case selector, redacted own-case responses, list/search/export, dispatcher and case SLA |
| knowledge | Immutable approval/publication/provenance, current audience filtering, ranked PostgreSQL FTS | Production scanning/extraction supervision, ingestion UI, pinned embeddings and vector index |
| ai | Bounded local-compatible HTTPS client, allowlist/attestation configuration, excerpt grounding/citation validation, safe handoff result | Real model deployment/evaluation, hybrid retrieval, durable human corrections and handoff routing |
| reports | Distinct event counts, event-time case dimensions, authorized assigned view, conservative board suppression | Durable historical projections, disclosure review across releases, export rendering/scheduling, SLA and adoption metrics |
| governance | Scoped inventory/requests/rules/lineage holds and durable deletion/restore journal | Actual access/export/correction/deletion workers, independently preserved restore journal and provider/backup expiry evidence |

The host SDK, frontend extension registry and eligibility hook are developed in
the separate Whatomate worktree. Its default catalog is empty. Protected mode
refuses startup until read/event/MFA and composition acceptance is complete.
No domain package alone enables a host feature or establishes compliance.

For database/race verification run:

```sh
docker compose -f compose.test.yaml run --rm test
docker compose -f compose.test.yaml down
```

The database is tmpfs-backed and has no published port. Test dependency downloads
need networking. Production worker execution must use the network and OS limits
documented with each worker. Unit tests without database environment variables
skip integration checks; that is not evidence of durable acceptance.
