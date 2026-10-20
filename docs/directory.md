# Employee directory domain

`directory` validates an explicit employee ID, canonical international phone
(`+` plus 8–15 digits), active flag, zone and department. IDs are stable opaque
strings, never row numbers. Phone/ID duplicates reject the entire preview,
including inactive duplicates. The domain grants no roles and has no OTP flow.
Eligibility requires an exact registered phone and one active, non-revoked
employee. Possession verification and authorization of privileged host operations
are separate host responsibilities.

## Import boundary

`ReadXLSX` accepts an already local `io.ReaderAt`, its size, an explicit worksheet
ZIP path and configured limits. Default bounds are 8 MiB compressed, 32 MiB
expanded across all ZIP entries, 256 entries, 100,001 rows, 64 columns and 4,096
bytes per cell. Callers may tighten limits; hard ceilings reject excessive
configuration. ZIP checks include duplicate entries and declared expansion;
actual decompression is bounded too. XML is parsed without external resolution.
The reader supports inline/shared text and scalar cells, rejects formulas on the
selected sheet and binary/macro parts throughout the archive. It does not fetch
relationships or evaluate formulas, macros or external links. Encrypted files
and malformed archives fail. It does not interpret display formats: store IDs
and phones as text to preserve leading zeros and exact values.

`Mapping` uses explicit zero-based columns; zone/department may be `-1` to omit.
Only explicit active values `1/true/active` or `0/false/inactive` are accepted.
Blank rows may be skipped. An empty replacement rejects, avoiding accidental
mass deactivation. Every nonblank row must validate before preview creation.
`Preview.Employees()` returns a copy for review. Its digest binds the canonical
validated records, including active status and organizational attributes.

Legacy binary `.xls` uses an optional local `LegacyWorker`, backed by the
hash-pinned Python reader in [workers/xls](../workers/xls/README.md). Actual BIFF
parsing, bounds and Go-to-worker execution have synthetic tests. The Linux worker
requires resource limits; the local container test disables networking and uses
a read-only filesystem, memory/process limits and an unprivileged identity.
The unconfigured `ReadXLS` entry point still returns `ErrLegacyXLSUnsupported`;
the host must explicitly provision and invoke the worker adapter. Production
release security approval remains outstanding.

## Approval and replacement

The host must authenticate/authorize the approver, display all preview records
and require explicit full-replacement approval bound to `Preview.Digest()`.
Approval is a trusted host decision, not a security token supplied by an end user.
The host must persist the approved artifact, approval actor, timestamp, scope and
audit evidence and prevent unintended approval replay. The HTTP adapter and operator screen below implement this flow.

`Replace` produces a new snapshot without changing its input. All missing
employees become inactive. Invalid preview/approval leaves prior state untouched.
`Apply` loads a snapshot and invokes one `Store.CompareAndSwap` with its expected
version. A stale concurrent approval returns a conflict, never silently retries.
The host reloads and re-reviews the proposed replacement after conflict.

`UrgentRevoke` uses the same versioned atomic persistence contract. It must
succeed durably before the host reports revocation complete; conflicts must be
handled explicitly. Revocation tombstones survive subsequent imports, including
imports that again mark the employee active. There is intentionally no implicit
unrevocation path. Revocations do not reset directory freshness.

## Freshness and durable adapter requirements

`Match` requires a positive configured maximum age and rejects missing import
time, future import time and age greater than or equal to that maximum. The host
must supply a trusted clock and load a current strongly consistent snapshot for
each protected eligibility decision. Snapshot loading errors deny access. Do not
cache eligibility beyond a revocation or freshness deadline.

`Store` defines the persistence boundary. `PostgresStore` is a durable adapter
using standard `database/sql`; the host registers a PostgreSQL driver and owns
the pool. Construct it with a tenant and authenticated actor, then invoke `Init`
explicitly using a migration role. Use a narrower role at runtime. The dedicated
`directory_snapshots` and `directory_events` tables store one JSON snapshot and
versioned audit events per tenant. A version-checked state write and actor event
commit in one transaction. An audit failure rolls back the state write. This
adapter implements durable directory state; the HTTP adapter below adds authenticated
approval artifacts and operator UI. Deployment acceptance remains host work.

The PostgreSQL integration test uses `DIRECTORY_DATABASE_URL` (otherwise skips)
and verifies competing initial writes, tenant isolation, a second connection
observing revocations, import/revoke conflict, audit count and rollback after
audit insertion failure. Run it against the actual deployment PostgreSQL version
and runtime role before enabling the feature. Restart/outage acceptance remains
required for deployment; a second pool is not a process-restart test. The
test-only atomic store demonstrates CAS contention, not durability.


## Authenticated operator HTTP and screen

`NewHTTPHandler(db, HTTPConfig{Freshness: maximumAge, Clock: trustedClock})`
constructs a handler without executing schema changes. Startup must explicitly
invoke `MigrateHTTP(ctx, db)` with the migration role. This creates the snapshot,
event and `directory_previews` tables. Runtime access needs SELECT/INSERT/UPDATE
on snapshots/previews and INSERT on events. The host supplies `api.WithActor`
only after current authenticated MFA and permission resolution. Tenant and audit
actor always come from that context. Invalid DB/clock/freshness configuration
returns 503; there is no administrator or wildcard permission bypass.

| Method | Route under `/api/extensions/directory/` | Exact permission | Input |
|---|---|---|---|
| GET | `status` | `directory:read` | None |
| GET | `roster` | `directory:read` | None; includes current records and revocations |
| POST | `preview` | `directory:import` | `expected_version`, `employees` |
| POST | `preview-file` | `directory:import` | Multipart `file` and strict JSON `metadata` |
| GET | `preview/{preview_id}` | `directory:approve` | Saved review ID |
| POST | `approve` | `directory:approve` | `preview_id`, `digest`, `expected_version` |
| POST | `revoke` | `directory:revoke` | `employee_id`, `expected_version` |
| POST | `validate` | `directory:read` | `phone`; protected operator eligibility check |

Employee inputs use `id`, `phone`, `zone`, `department`, `active`. All JSON uses
the bounded strict decoder and standard data/error envelopes. The HTTP roster
input supports canonical employee JSON, maximum 1 MiB per JSON request. The primary
screen imports an XLSX workbook through `preview-file`, streaming multipart bytes
with an 8 MiB file bound and 64 KiB overall overhead bound, without temporary disk
files. Only one `file` and one `metadata` part are allowed. Metadata is bounded to
8 KiB and uses the strict decoder with `expected_version`, explicit `worksheet`,
`mapping` (`employee_id`, `phone`, `active`, `zone`, `department`, all zero-based),
and `skip_header`. The existing `ReadXLSX` and `MapRows` APIs perform validation;
the server never trusts browser-parsed rows. It persists both the uploaded bytes'
SHA-256 and the canonical roster digest in the saved review artifact. XLS rejects
with `legacy_xls_unconfigured` unless trusted host configuration explicitly supplies
`HTTPConfig.Worker`; executable paths and arguments never come from an upload.
The screen displays the sheet path and column defaults for adjustment before upload,
then every validated record, both hashes, original version, actor and creation time.
JSON file upload and pasted canonical arrays remain available as an alternative.

The preview records, digest, base version, tenant, creation actor/time and eventual
approval actor/time persist in PostgreSQL. Approval reloads and revalidates the
saved records, checks the exact requested digest and original version, and atomically
writes the snapshot, actor audit event and consumed approval. A digest mismatch,
already consumed approval or changed roster returns 409. An audit failure rolls
back consumption and the replacement. The stored preview can be reopened after
reloading the screen. Review IDs from another tenant return 404. Concurrent
approvals cannot silently replace each other's state or an urgent revocation.

`status` reports freshness independently of active flags; operator inspection of
stale records remains possible. `validate` reloads the current snapshot on every
request and denies inactive, duplicate, unknown or revoked numbers and missing,
stale or future import times. This endpoint does not establish phone possession
or grant employee channel authentication. The Vue `DirectoryPage.vue` uses the
host's cookie/CSRF API client, explicit action grants, a full-replacement review
checkbox and version-bound immediate revocation. A conflict requires a new review;
no UI retry silently advances the expected version.

`TestHTTPDurableApprovalRevocationAndConcurrency` and `TestHTTPWorkbookImport` execute against PostgreSQL with
an isolated synthetic tenant. It exercises strict payload rejection, hash binding,
persistent reviews, tenant isolation, replay rejection, stale/future eligibility,
sticky revocation, competing approvals, trusted actor audit and rollback of both
state and approval consumption on audit failure. Workbook coverage includes a real XLSX ZIP, malformed bytes, oversized files, formulas, invalid mapping and unconfigured XLS. `DIRECTORY_DATABASE_URL` enables
this test alongside the original adapter tests. Production process restart,
least-privilege runtime role and outage acceptance remain release requirements.

### Worksheet names and spreadsheet column letters

The workbook screen now accepts the visible worksheet tab name (default `Sheet1`)
and displays column choices as A, B, C through BL. Optional zone and department
selectors offer `Not provided`. The transport mapping remains zero-based for API
compatibility; operators do not need to enter column numbers or package XML paths.

`ReadXLSX` accepts a visible worksheet name as well as its original explicit local
`xl/worksheets/*.xml` selector. Named selection reads only the already bounded ZIP
entries `xl/workbook.xml` and `xl/_rels/workbook.xml.rels`. It rejects external or
URL relationships, encoded path syntax, traversal, repeated relationship IDs,
ambiguous worksheet names/IDs/references, and non-worksheet relationship types for
sheet references. Safe absolute package targets such as `/xl/worksheets/sheet1.xml`
and ordinary `worksheets/sheet1.xml` targets normalize to the same local ZIP entry.
No network, relationship fetching or formula execution occurs. Existing workbook
expansion, row, cell, macro and formula boundaries continue to apply.

`workbook_named_test.go` covers an XLSX generated by openpyxl 3.1.5 with the visible
sheet `Synthetic roster`, safe absolute/relative targets, rejection cases and a
real PostgreSQL named-sheet upload/preview/approval using an isolated synthetic
tenant. The fixture contains only synthetic employee details.
