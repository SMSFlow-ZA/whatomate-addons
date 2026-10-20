# Record governance and restore journal

`governance/` is an AGPL, host-independent policy, request, physical storage lifecycle and restore-journal component. The original `Service.RecordDeletion` remains a reference-only workflow receipt. The new `PhysicalService.EraseRequest` invokes trusted owner adapters and physically removes governed SQL content in the same transaction as current retention/hold checks and its receipt. Neither method implies legal compliance or complete erasure across external stores.

## Inventory and policy

Inventory sources are typed: core, case, knowledge, vector, audit, backup and provider. Each record carries a canonical subject ID, exact organization/unit scope, stable lineage ID, opaque external reference, creation/retention dates and restore generation. No narrative, transcript, attachment content or support incident body belongs in these records. Support incidents are represented by opaque references only. Reference validation rejects whitespace/control characters and limits length; it cannot determine semantic sensitivity, so the host must ensure a reference is actually an identifier.

A root record has its own ID as lineage. Derived records must reference an existing root with the same subject and scope. This makes a root legal hold apply to its knowledge/vector/backup/provider copies. Inventory registration is immutable and idempotent; restored instances use `ReconcileRestore`, which increments their generation. The host must enumerate all copies and preserve lineage. This component does not discover unregistered stores or identities.

Every source requires an explicitly approved typed retention rule. Missing/unapproved rules block deletion. Negative retention durations are rejected. Both minimum age under the rule and record-specific `RetainUntil` must have elapsed. There is no automatic purge scheduler. An approved deletion request and operational permission remain necessary even after retention elapses.

## Authorization and subject requests

Permissions are explicit `governance:{action}` strings, matched to both organization and unit. There is no administrator/support bypass or wildcard scope. Access, correction, export and deletion requests require separate permissions. A new request cannot supply its own approval: `Request` resets approval and starts version 1. Replaying the same request ID requires identical subject, kind and reference. Only an actor with `governance:deletion.approve` may approve a deletion request, using its expected version. Other request kinds cannot become deletion approvals.

The host must authenticate principals, verify subject identity/authority and supply trusted scope/claims. These are workflow records, not self-service endpoints. Actual access responses, corrected source data, export packages and legal review are not implemented. The module stores the requested action and reference so those host workflows can be reviewed and audited.

Policy management, legal-hold management, inventory writes, deletion planning/receipt recording, journal reading and restore reconciliation each require their own scoped permission. Repository/transaction interfaces bypass policy and must remain internal.

## Deletion and restore behavior

`PlanDeletion` requires an approved deletion request. It writes durable entries for all registered subject records in the scope. Entries distinguish pending work, blocked legal hold, blocked retention, blocked policy, recorded application and awaiting external expiry. Journal IDs hash structured request/record keys rather than concatenating ambiguous identifiers.

`RecordDeletion` records an opaque confirmation reference for one record generation. It uses expected-version CAS and rechecks current approval, retention policy and all active lineage holds in the same serialized scope transaction. A legal hold blocks both roots and derived copies. Previously recorded application remains a historical receipt; it is not evidence that a later restored generation was deleted. No physical deletion happens in this method, and callers must not treat a plan or receipt API as authorization to perform side effects outside an appropriately governed storage workflow.

Provider and backup entries stay `awaiting_external_expiry`. The current API cannot mark them erased, even when another store has a recorded application receipt. Provider receipts, backup expiry verification and their external deletion workers are future integration work.

`ReconcileRestore` takes a stable restore key and record ID. It atomically increments the inventory generation and rewrites every approved subject deletion journal entry for that record to the current blocked/pending state, clearing prior confirmation references. Replaying the same key is idempotent; applying it to another record fails. A hold remains authoritative during reconciliation. New inventory registered after an approved deletion request automatically receives journal entries, so late restored copies do not silently escape the outstanding deletion workflow.

Restoration must preserve the durable governance journal or restore it from an independent authoritative journal before applications process restored business data. Restoring business data together with an old journal snapshot can lose newer deletion intent; the module cannot recover evidence absent from all surviving storage.

## PostgreSQL persistence

`SQLRepository.Migrate(ctx)` explicitly creates v1 `addon_governance_document` and `addon_governance_event` tables with scope/key indexes. Typed inventory/rules/holds/requests/journal/restore keys are serialized in namespaced JSONB documents. Audit rows include actor, action, reference ID, scope and server recording time. All metadata mutations and audit insertion commit atomically. No host core tables are modified.

A PostgreSQL transaction-scoped advisory lock over structured organization/unit identity serializes policy, hold, receipt and restore changes. Expected versions still reject competing deletion receipts. Hash collisions only add serialization. This is deliberately coarse locking; scope throughput and indexed queries need measurement before enabling a large deployment. The host owns the SQL driver, credentials, pool, migration permissions, encryption and database backup/restore strategy. Production adapter code depends only on the Go standard library; integration tests use the host-selected pgx driver.

Transaction callbacks return provisional data. Service methods expose results only after confirmed commit. Unknown confirmations return `ErrCommitUnknown` and no provisional request/journal result. Stable request/restore keys allow callers to reconcile durable state. Any callback error rolls back metadata and audit. Application identities should not receive schema mutation rights or expose raw repository methods.

## Verification and remaining integration

Unit tests cover default-deny exact-scope permissions, unknown request/source rejection, reference-only boundaries, approval requirements, missing policy, retention, derived holds, provider/backup pending status and unambiguous journal IDs.

With a disposable PostgreSQL DSN in `CASES_DATABASE_URL`, integration tests cover all four independently authorized subject-request kinds; blocked/unblocked derived deletion; twelve concurrent receipt writes with one CAS winner; idempotent restoration; restoration under hold; late restored copies; pending backup expiry; cross-scope/admin denial; persisted audit; lost commit confirmation followed by durable replay; and rollback when audit insertion fails.

```sh
go test ./governance
go test -race -tags integration ./governance
go vet -tags integration ./governance
```

Host composition and production release acceptance remain separate. The new owner adapters, HTTP boundary and operator screen below implement SQL discovery, export, case correction and erasure; external-provider/backup evidence verification, legal review, independently durable journals, immutable audit export, production retention approvals and operational monitoring are not supplied. No production deployment, automatic purge or chargeable messaging is authorized.

## Physical lifecycle and HTTP composition

Run `governance.MigrateHTTP(ctx, db)` explicitly, then compose `NewHTTPHandler(db, HTTPConfig{Enabled: true, RefreshActor: currentSessionResolver, SubjectAliases: verifiedHistoricalIdentityResolver, Adapters: ...})`. Missing current-session refresh fails closed. The refresh must preserve original organization, user, session and current MFA state, and reload current role/unit/audience grants. Only enabled, migrated modules belong in the adapter list. Generic source stays independent of host internals.

Owner ports are `cases.PrivacyAdapter`, `knowledge.PrivacyAdapter`, `directory.PrivacyAdapter`, `conversation.PrivacyAdapter`, `reports.PrivacyAdapter` and `ai.PrivacyAdapter`. These access only their own tables; conversation calls the delivery owner's `EraseEmployee` port. They receive the *same* `*sql.Tx` that holds the governance scope advisory lock. Current case work/triage, conflict exclusions, knowledge audiences and dedicated `directory:privacy` / `conversation:privacy` unit grants remain additional requirements. A privacy permission does not grant source access. Reports raw export additionally requires operational reporting permission; leadership aggregate permission never confers this access.

`SubjectAliases` resolves verified historical employee/operator identities; it never adds operator permissions or changes the authenticated actor. Use a verified canonical employee roster ID for a person request. An explicit document ID targets a shared source document, not every policy uploaded by a person. A source object already registered under another canonical subject/scope produces a conflict for review; the implementation does not silently reassign its lineage. Multi-subject records require source-owner review before deleting the whole object.

All routes are under authenticated `/api/extensions/governance`:

| Method / route | Required intent / fields |
| --- | --- |
| GET `inventory` | `unit_id`; metadata filtered through current source policy |
| POST `discover` | `unit_id`, `subject_id`; actual accessible SQL objects only |
| POST `requests` | `unit_id`, `subject_id`, `request_id`, `kind`, opaque verified-authority `reference` |
| POST `approve` | Deletion request ID and `expected_version` |
| POST `plan` | Deletion request ID; advisory plan, not physical erasure |
| GET `export` | `unit_id`, `subject_id`; original bytes included for authorized knowledge sources; lease tokens omitted |
| POST `correct` | Recorded correction request, inventory `record_id`, owner patch (`expected_version`, current case `narrative`, `reason`) |
| POST `hold` | Root `lineage_id`, `hold_id`, opaque decision reference, explicit `active` |
| POST `rule` | Source class, approved minimum retention seconds and approval state |
| POST `erase` | Approved deletion request ID; owner SQL mutations and journal commit together |
| GET `journal` | Exact unit and request ID; minimal durable decision/receipt metadata |
| POST `restore` | Offline restore gate: subject/request and stable `restore_key`; resets generations then reapplies approved erasure |

Source classes now also include `directory`, `conversation`, `report`, `ai` and `worker_file`. Missing/unapproved rules for *any* discovered class block deletion. No legal, customer or production duration is hard-coded. The UI requires explicit review before an erasure intent, displays holds/conflicts/unknown outcomes, and downloads only real authorized server exports. It stores no export package or narrative in persistent browser storage; downloaded copies remain a separate retention responsibility.

## Stored objects and explicit residuals

| Owner | Physical operation | Deliberately separate residual / limitation |
| --- | --- | --- |
| Cases | Current narrative/payload, event reasons/history, intake dedupe/fingerprint, acknowledgment outbox and dispatch state | Minimal governance request/journal metadata remains. Known sending/accepted/delivered/unknown acknowledgment transport creates a provider-pending inventory derivative. Carrier copies cannot be recalled by SQL. |
| Knowledge | Source/quarantine bytes, metadata/job/receipt rows, extracted publication bytes/text, approvals and FTS pages | A receipt-backed external worker-files derivative remains `awaiting_external_expiry`. Controller job directories/volumes are not deleted by this HTTP API. Old publications without processing jobs need an explicit inventory migration; no exhaustive historical discovery claim. All same-version duplicate uploads/publication audiences are rechecked before deletion. |
| AI | Prompt/question, suggestion/output/correction records and their payload-bearing audit rows | Live model memory, process caches, telemetry and logs require separate worker/process operations and measured expiry. No independent vector table is used by these adapters. |
| Reports | Case event projections (actual and opaque digest keys) plus linked reporting activity | Previously downloaded/exported reports remain external copies. Pre-case content-free activity has no retained subject link; rare-dimension reidentification/retention still requires review. |
| Conversation/delivery | Employee-linked turns, replay responses/fingerprints, case timers, linked case activity, reply/fallback rows | In-flight sender processes must be quiesced and reconciled; deleting a local row cannot cancel an external call already underway. Provider/handset copies and pre-case unlinkable usage metadata remain separate. |
| Directory | Current phone/zone/department/active employee content; entire immutable previews containing that identity are invalidated/purged | Minimal revoked employee identifiers remain to prevent reimport resurrection; append-only verified identity links remain under their separate authority and retention decision. Directory version/audit actor metadata remains. Original uploaded files outside the database are not discovered. |

Each erase rediscovers current owner objects in the governed transaction, checks every recorded class's current retention and lineage holds, then removes derivatives before parent cases. Holds added through the governance service use the same scope lock. A restored/new record inherits the outstanding workflow. A completed receipt is idempotent for its record generation; it cannot prove that an external provider erased a copy.

For destructive maintenance, stop inbound imports, publication, inference and message dispatch, finish or hold in-flight external attempts, then perform the authorized request. Owner row locks and governance transactions prevent partial SQL commits; they do not make model processes, file volumes, senders or another database transaction an atomic distributed erasure.

## Restore gate and evidence

Keep restored business data offline/inaccessible. Recover the independent authoritative governance journal *before* restored business records can serve traffic. Supply an immutable restore identity, reset all registered subject-record generations with `restore`, and reapply approved physical erasure. Active holds or missing policies block reopening until reviewed; pending provider/worker/backup copies remain visible. Rerun directory revocation, publication withdrawal and source-level access checks before opening the application. Restoring an old journal together with business data cannot recover newer deletion intent; this module has no magic recovery of lost independent evidence.

Real PostgreSQL acceptance in `governance/physical_integration_test.go` exercises atomic hold-denied erasure, cross-unit denial, actual content/event/outbox deletion, idempotent replay, independent-journal restore and repeated physical erasure; genuine quarantine byte removal with worker-file pending status; current case narrative correction/version conflicts; verified identity aliases; and physical AI/report/directory/conversation/delivery cleanup. HTTP unit tests reject absent refresh, revoked MFA and identity replacement before storage access. Frontend compilation and final integrated release checks remain host responsibilities.

## Trusted offline reconciliation and controller completion

`PhysicalService.ReconcileSubjectRestore(ctx, actor, scope, subjectID, requestID, restoreKey)` is the trusted CLI/controller port also used by the authenticated restore endpoint. It requires current MFA, exact restore and deletion grants, and an approved deletion request for that exact subject. It atomically resets every registered subject object's generation and all approved deletion journals before calling physical erasure. A mismatched request cannot reset another subject's generations. Use a unique operation identity for each actual restore, stable only across retries of that operation; a backup hash alone is not a fresh restore identity. Any returned error leaves the restored service unavailable until reviewed. External pending entries still require their owner controller.

An offline host command must validate the user's *original signed session*, current Redis/session revocation, MFA generation and current roles against the original authoritative host. Creating an `api.Actor{MFA:true}` from operator-entered IDs is not authentication. Import the newer, full independently captured governance authority only into a target whose governance tables were verified blank before import; preserve its inventory, policies, holds, approvals, journals, restore keys, controller receipts and audit together. Do not combine an old approval snapshot with a newer receipt-only fragment. Business service startup follows proof of successful reconciliation, never precedes it.

`PhysicalService.CompleteWorkerErasure(ctx, actor, scope, requestID, recordID, expectedJournalVersion, trustedController)` has **no browser route**. It verifies current deletion permission, approved matching request, exact worker record/generation/version, retention and lineage holds under the same governance scope lock used by hold changes. Only then does it invoke the trusted controller. The callback must obtain independently expected original tenant/uploader/input hash and audiences from authoritative processing evidence, recheck current source-owner grants, remove only the validated immutable job's artefacts, and verify a durable controller receipt plus actual directory absence. Merely returning a supplied JSON receipt is not an implementation of this callback.

The returned `WorkerErasureReceipt` binds job/tenant/uploader/input hash/immutable manifest hash/controller receipt hash/time; governance cross-checks its job and tenant identity and hash shape, stores generation-specific evidence and records `recorded_applied` only after the callback succeeds. A callback failure, mismatched receipt or hold cannot record completion. Filesystem removal cannot roll back if a later SQL commit fails or is unknown: reconcile the independently durable controller receipt and actual absence before retrying journal completion, with the service still unavailable. This confirms the exact controller directory, not backups, downloads, carrier copies, process memory or all logs.

The delivery owners additionally retain `addon_cases_delivery_receipt` and `addon_conversation_delivery_receipt` as minimal control audit. They contain tenant/account/receipt/intent/provider IDs, receipt fingerprint, outcome and observed/recorded times. They contain no raw evidence, signature, message body, narrative, phone or employee ID, have no parent-content foreign key and deliberately survive content erasure. Their potentially linkable control identifiers require their own approved audit retention/hold policy; erasing case/reply payloads is not authority to blindly delete these journals.
