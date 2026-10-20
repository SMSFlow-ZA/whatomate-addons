# Record governance and restore journal

`governance/` is an AGPL, host-independent policy and reference-only workflow component. It has no host internal imports, sends no messages and physically deletes no data. No legal compliance or complete-erasure claim follows from recording a workflow receipt.

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

Host extension registration, APIs and UI, identity linkage, source discovery, export generation, source correction/deletion workers, provider/backup evidence verification, legal-review processes, immutable audit export, journal retention policy, independent-journal restore coordination, migrations beyond v1, operational monitoring and release acceptance are not supplied. A storage worker must coordinate legal holds with physical side effects; this metadata journal alone cannot make an external deletion transaction atomic with PostgreSQL. No production deployment, automatic purge or chargeable messaging is authorized.
