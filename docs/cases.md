# Protected cases

`cases/` owns durable protected case metadata, narratives, drafts, human category
corrections, participant conflict facts, assignments and lifecycle events. The
package uses only its own PostgreSQL tables and never sends messages.

## HTTP and operator UI

`NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler` performs no DDL.
`HTTPConfig.Enabled` defaults false. The host authenticates a current MFA session
and resolves current explicit permissions and exact unit grants into `api.Actor`.
`PrincipalForActor` copies only grants that pass `Actor.Allows(permission, unit)`.
Neither bodies nor query parameters can supply actors, grants, contact identity or
organization. API keys cannot obtain the operator session context.

All routes are below `/api/extensions/cases` and return the shared `{data: ...}`
or `{error:{code:...}}` envelope with `Cache-Control: no-store`:

| Method/path | Behavior |
| --- | --- |
| GET `/` | Current authorized inbox, optional `q` reference/state/category search |
| POST `/` | Intake with `unit_id`, `key`, `narrative`, `snapshot:{zone,department,case_type}`, optional `draft` |
| GET `/{id}` | Current authorized detail; unknown and denied return the same 404 |
| PUT `/{id}/draft` | Save narrative on a Draft |
| POST `/{id}/state` | Validated lifecycle transition |
| POST `/{id}/assignment` | Assignment/reassignment using verified `candidate_id`, optional `backup_ids`, `role`, reason |
| POST `/{id}/category` | Human category correction and mandatory reason |
| POST `/{id}/participants` | Verified `candidate_id`, `participant_role` subject/supervisor, mandatory reason |
| GET `/facts` | Operator reporting facts; requires `reports:operational` and current assigned/triage case-read scope |

Mutations of an existing case require `expected_version`; an optional
`occurred_at` defaults to server time. Unknown JSON fields and oversized bodies
are rejected. Invalid input is 400, unavailable/denied actions 403, version/key
conflicts 409, persistence failure 503. Uncertain commits use 503
`commit_unknown_retry_same_key`, returning no provisional case ID. Error bodies
never include narratives, identities, SQL errors or authentication secrets.

Intake requires an exact `cases:submit.own` unit grant. The authenticated
principal becomes the linked complainant; the request cannot choose another
complainant. Operator intake classification values are explicit captured inputs,
not inferred historical membership. A host employee-channel adapter must resolve
verified eligibility, identity and classification before invoking the domain
service. The WhatsApp adapter is outside this package.

The host's CasesPage uses the existing authenticated Axios service and CSRF
interceptor. It reads current grants from `/api/extensions/catalog`, renders a
live inbox and detail, and exposes draft, lifecycle, assignment/reassignment,
verified participant and human correction actions. Its intake retry keeps an
unchanged serialized payload and idempotency key while the outcome is unknown.
Narratives remain in component memory and are not stored in browser persistence.

## Authorization and privacy

Participants distinguish complainant, subject and participant supervisor. Links
never grant read/work rights. Subject conflict overrides every grant. Assigned
handlers/supervisors need `cases:work.assigned` for the exact organization/unit.
Unassigned triage requires `cases:read.triage`; triage management separately
requires `cases:assign.triage`. There is no implicit administrator, wildcard unit
or support bypass. Own reads/submission require a matching verified complainant
identity and `cases:read.own`/`cases:submit.own` for that exact scope.

`HTTPConfig.ResolveCandidate` must resolve a live same-organization identity and
its current explicit work grants. It returns a verified Principal even if that
identity has no work grant, allowing conflict facts to be recorded. Assignment
routing independently requires `cases:work.assigned` and a conflict-free target,
trying primary before backup candidates. No eligible route fails closed. Linking
a subject immediately removes that identity's existing assignment. Participant
link changes, assignments and category corrections retain actor, reason and
identity facts in internal versioned audit events.

A pure own-case read receives `EmployeeDTO` only: `id`, `state`, `version`.
Narratives, assignment and participant identities, reasons, classification,
internal events and contact IDs are omitted. Operator detail uses a separate
explicit DTO. The fact API excludes own-only cases. Module repository APIs and
MemoryRepository diagnostics bypass policy and must never be exposed directly.

## Durability, history and recovery

`SQLRepository.Migrate(ctx)` explicitly installs/upgrades versioned module-owned
storage atomically under a migration advisory lock. Versions 1 and 2 are recorded
in `addon_cases_schema`; the v2 aggregate fields are additive JSON and preserve
legacy zero-value intake fingerprints. Schema work requires a migration identity;
runtime identities need only DML. No host core table is modified.

Intake atomically commits the case, complete structured request fingerprint,
organization/key idempotency record, initial audit event and one acknowledgement
outbox intent. It serializes duplicate keys with a transaction-scoped advisory
lock and retains full-key uniqueness. Participant order affects fingerprints.
Replaying identical requests recovers the durable result; a changed request with
the same key conflicts. Commit uncertainty never produces a successful ID. Retry
the original key and identical payload rather than generating another key.

Draft and narrative edits, category corrections, participant facts and assignment
changes use compare-and-swap and append an event in the same transaction. Scope,
contact and creation-time zone/department/type snapshots cannot change through
Save. Current corrected Category is separate from the immutable CaseType at
intake. Historical facts therefore retain the original snapshot even after human
correction. Events preserve stable case/version IDs, recording and occurrence
times, event-time organization/unit and internal audit facts.

Lifecycle is Draft → Submitted → Assigned → InProgress, AwaitingInfo, Escalated,
Resolved and Closed with defined permitted transitions. Resolved/Closed reopen to
InProgress only with a nonblank reason. Occurrence time may precede recording.

`ReportFacts(ctx, db, actor)` is the authorized cross-module event/fact producer.
It returns no narratives, participant/assignment IDs, actor identities or reasons.
Each fact has case ID, version, event-time organization/unit, immutable snapshot,
from/to state and occurrence/recording times. It filters against current operator
case-read grants and subject conflicts at delivery. Reports must use this API,
not query private case tables. `AggregateFacts(ctx, db, actor)` is a separate server-only boundary for exact
`reports:aggregate` unit grants. It replaces case IDs with stable one-way digests
and returns only classification/state/time facts. It excludes subject conflicts
and is never wired as a raw HTTP endpoint. Its reporting consumer must apply
aggregate suppression before delivery. Aggregate-only leadership permissions do not grant
raw facts; report suppression remains owned by the reporting module.

Transactions are single-goroutine objects. A transaction's writes and IDs remain
provisional until confirmed Committed. Unknown is distinct from RolledBack.
Rollback remains safe after terminal operations. MemoryRepository is a serialized,
cancellation-aware reference adapter for tests only, not durable production storage.

## Verification and remaining operational work

Synthetic unit/HTTP and real PostgreSQL concurrency tests cover scope/default
denial, unknown/denied equivalence, strict input, subject overrides, primary and
backup routing, verified conflict links, redacted employee responses, current
grant revocation, draft durability, immutable classification, category audit,
concurrent intake/CAS and uncertain-commit replay without duplicate outbox rows.
Run the disposable test database with `CASES_DATABASE_URL` configured:

```sh
go test ./cases
go test -race ./cases
go vet ./cases
go test -race -tags integration ./cases
```

Operational work remains: attachments, retention/deletion/legal hold, audit
export, and an acknowledgement dispatcher with leases/retries/provider
idempotency. The outbox records intent only and makes no delivery or exactly-once
claim. Production encryption, runtime credentials, backups, privacy/security
review and release acceptance belong to the host/deployment boundary. No
production deployment or chargeable messaging is authorized by this component.

HTTP intake requires a trusted ResolveIntake callback. The host derives immutable zone/department/type from the current approved employee directory. Client snapshot fields are ignored for persistence; missing policy denies creation. Assigned case assistance uses exported CanWork, excluding triage-only visibility and all subject aliases. Escalate atomically records conflict-aware routing and the Escalated lifecycle state.
