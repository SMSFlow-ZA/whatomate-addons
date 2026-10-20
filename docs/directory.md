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
import UI, orchestration and release security approval remain outstanding.

## Approval and replacement

The host must authenticate/authorize the approver, display all preview records
and require explicit full-replacement approval bound to `Preview.Digest()`.
Approval is a trusted host decision, not a security token supplied by an end user.
The host must persist the approved artifact, approval actor, timestamp, scope and
audit evidence and prevent unintended approval replay. No transport/UI exists.

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
adapter implements durable directory state; authenticated approval artifacts,
approval UI, host SDK wiring and deployment acceptance remain host work.

The PostgreSQL integration test uses `DIRECTORY_DATABASE_URL` (otherwise skips)
and verifies competing initial writes, tenant isolation, a second connection
observing revocations, import/revoke conflict, audit count and rollback after
audit insertion failure. Run it against the actual deployment PostgreSQL version
and runtime role before enabling the feature. Restart/outage acceptance remains
required for deployment; a second pool is not a process-restart test. The
test-only atomic store demonstrates CAS contention, not durability.
