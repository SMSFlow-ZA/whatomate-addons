# Protected cases

`cases/` is a standalone AGPL domain and PostgreSQL persistence adapter. It has no host internal imports and stores only generic case metadata. Identity claims, membership, participant links and routing conflict facts are trusted host inputs; never populate them directly from an unauthenticated request. The package sends no messages.

## Implemented

- Draft → Submitted → Assigned → InProgress, AwaitingInfo, Escalated, Resolved and Closed. Resolved/Closed can reopen to InProgress only with a nonblank reason. `Transition` is a pure domain operation; `Service.ChangeState` persists it with optimistic compare-and-swap. Intake starts at Submitted; a host draft editor is not provided.
- Participants distinguish complainant, subject and participant supervisor. These links alone grant no read or work access. A subject is denied even when another role or permission would allow access. The host must link all identities of a subject before assigning a case.
- `WorkAssigned` requires both an assignment as handler/supervisor and an exact organization/unit grant. `ReadOwn` and `SubmitOwn` require a matching complainant link and exact scope. There are no organization admin, support or wildcard bypasses.
- Unassigned triage uses separate `ReadTriage` and `AssignTriage` grants. `Service.Assign` only assigns an unassigned Submitted case. It rejects a subject as the triage actor and routes to an explicitly scoped, conflict-free handler/supervisor, trying primary candidates before backup candidates. No eligible route returns `ErrNoRoute`; the host must arrange manual handling.
- `Service.Read` filters access and treats unknown and denied IDs alike. Repository methods and the memory diagnostic `Snapshot` bypass authorization and must remain internal. There is no public list/search endpoint.
- Multiple cases may share a contact. Intake deduplicates by organization plus caller-supplied message key, with a SHA-256 fingerprint of the complete structured request. Reusing a key with different fields fails. Participant order is significant to the fingerprint, so replay the identical request.
- Every intake, assignment and lifecycle command records actor, monotonically increasing aggregate version, organization/unit at event time, server recording time, occurrence time, from/to state and reason. The current scope cannot change through `Save`. Occurrence time is an asserted historical fact and can precede recording time. Reopen reason remains on the aggregate and in the event.
- Intake commits the case, idempotency record, initial audit event and one acknowledgement outbox record in the same transaction. No acknowledgement is dispatched in that transaction. An ID is returned only after confirmed commit with no error. Commit errors are conservatively Unknown; callers retry the exact intake key/request to discover the durable result. Never generate a new key after an uncertain commit.

## Persistence and transaction contract

`SQLRepository` accepts a host-owned `*sql.DB` with a PostgreSQL driver. Its explicit `Migrate(ctx)` installs the v1 `addon_cases_case`, `addon_cases_intake`, `addon_cases_outbox` and `addon_cases_event` tables and the contact lookup index. Schema changes run atomically. Run migration with a migration identity, then give the runtime identity only the DML it needs. The adapter never writes host core tables. The host must supply and configure its driver, connection pool, encryption, credentials, backups and tenant deployment boundary.

Intake holds a transaction-scoped advisory lock over a hash of the structured organization/key pair. Full composite uniqueness remains enforced by PostgreSQL; hash collisions only serialize unrelated keys. Read-committed isolation and a version-conditional UPDATE protect concurrent mutations. Audit insertion is in the same transaction as the updated aggregate. Separate event rows retain event-time scope and are not reconstructed from present membership.

`Repository.Begin` returns a `Transaction` with provisional writes. A caller must roll back after any operation error and must never expose a provisional case ID. `Commit` returns Committed only when persistence confirms it; RolledBack means known not committed; Unknown means confirmation was lost. Rollback must be safe after terminal operations. Transactions are single-goroutine objects. A host adapter implementing this interface must preserve these semantics, full idempotency keys, immutable scope, CAS and atomic outbox/audit writes. Read-only service operations roll back their transaction after reading.

`MemoryRepository` is a cancellation-aware serialized reference adapter for tests/development, not durable storage. Its `Seed` and `Snapshot` are diagnostics and deliberately bypass policy.

## Verification

Unit tests exercise scope/default denial, subject conflict overriding grants, separate unassigned triage, participant validation, protected reads, detached aggregates, lifecycle/reopen rules, invalid-transition immutability, conflict/backup selection, concurrent intake deduplication and CAS, rollback, cancellation, structured idempotency keys, and suppressed IDs on failed/uncertain commit.

PostgreSQL tests use a disposable database supplied in `CASES_DATABASE_URL`:

```sh
go test ./cases
go test -race ./cases
go vet ./cases
go test -tags integration ./cases
go test -race -tags integration ./cases
```

Integration tests verify sixteen concurrent identical intakes produce one case, intake key, outbox record and audit row; a reconstructed service replays the same ID; sixteen competing assignments have one winner and two persisted events; explicit rollback leaves none of the four records. A test driver wraps real pgx/server transactions, commits on the server, then simulates a lost commit confirmation. The service reports Unknown with no ID, and a normal replay recovers one durable case without a duplicate acknowledgement. The driver is test-only; production code depends only on the standard library.

## Production work still required

This is a domain/persistence component, not an enabled host feature. HTTP/API handlers, authenticated identity and eligibility integration, host extension registration, permission administration, participant updates, reassignment, draft persistence/editor, attachments and sensitive narrative storage, retention/deletion/legal hold, immutable audit export, list/search query filtering, redacted complainant views, operational reporting, migrations beyond v1 and an acknowledgement dispatcher are not provided. Own-case reads currently return the full metadata aggregate, so a host exposing complainant reads must explicitly design/redact its response. Disabling `ReadOwn` prevents that surface until the view exists.

The outbox is durable intent only. There is no claim/lease/retry/delivery acknowledgement API, provider idempotency contract or exactly-once messaging guarantee. Production release requires an independently reviewed dispatcher, privacy/security review, failure/recovery exercises and host acceptance tests. No deployment or chargeable messaging is authorized by this package.
