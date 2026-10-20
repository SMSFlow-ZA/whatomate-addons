# Isolated synthetic local runtime

This profile runs on a developer PC. It is not an approved production locality,
security certification or live WhatsApp/SMS deployment. Public source contains
no customer documents, passwords, factors or case data. Generated private state
lives in the sibling host's ignored `.work/synthetic-local/` directory.

## Start from a clean clone

Prerequisites: Docker Linux containers, PowerShell 7, Node.js, installed frontend
dependencies, and Python with openpyxl for the optional XLSX fixture generator.
Place this repository beside `whatomate-addons-host`; the host's development
module replacement expects that layout. Production releases need an immutable
paired source/build, not a developer checkout replacement.

From this repository:

```powershell
./scripts/local/initialize.ps1
./scripts/local/runtime.ps1 seed
python ./scripts/local/roster.py ../whatomate-addons-host/.work/synthetic-local
./scripts/local/configure-modules.ps1
./scripts/local/build-ui.ps1
./scripts/local/runtime.ps1 start
./scripts/local/runtime.ps1 status
```

The initial configuration enables directory, cases, knowledge and AI; the explicit
configuration step enables conversation, reports and governance once those modules are built.
The UI build explicitly selects compiled modules; omission leaves them disabled.
The backend listens only at `127.0.0.1:18280`. Its internal listen address is
container-local `0.0.0.0:8080`; PostgreSQL and Redis have no published ports.
Use a separate browser profile to avoid localhost cookie collisions with other
development applications. `synthetic-demo.localhost` may work in Chromium but is not
resolved by Windows PowerShell on every machine; `127.0.0.1` works for API calls.

Initialization creates random DB, JWT, encryption and MFA encryption secrets and
refuses to overwrite an existing configuration. Seed refuses existing
organisations, creates eight separate synthetic identities/roles, and writes
credentials only to the private `identities.json`. No factor is pre-enrolled.
Perform genuine enrollment/challenge through the MFA screen/API. Passwords and
factor state are never reset by a rerun. The upstream default administrator
configuration points at the seeded administrator, preventing a known default
login from being created by startup migrations.

Employee aliases are append-only verified identity links. Visibility permission
`cases:read` does not confer a domain read; exact own/triage/assigned policies
still apply. Extra scoped grants intersect the current host role on every use.
`SYNTHETIC_SYNC_VISIBILITY=true` on the seed tool supports only the explicitly
named synthetic case visibility/content-review/privacy exercise grants without
resetting users or running migrations. ER and HRBP privacy grants are restricted
to `demo-er`; document administrator/publisher privacy grants are restricted to
the `employees` document scope. These broad demonstration roles are for a
controlled disposable acceptance exercise, not an approved production role plan.

The fixture roster has 36 fictional employee IDs and reserved `+120255501xx`
numbers. Never dial these numbers or use them with a live carrier. The generator
does not approve a roster: upload, review and approve through the real API/UI.
Knowledge documents likewise require actual processing, a reviewer and a
different publisher. Seed creates no approved documents, answers, cases or reports.

## Actual isolated PDF processing

Build the worker using the immutable base/package arguments in
[documents.md](documents.md), record its image ID, and provision official
ClamAV CVDs in the separately managed `whatomate-addons-clamav` volume. The local
profile currently pins the previously accepted image ID and corpus digest in
`scripts/local/processing.ps1`; update those explicitly after a reviewed rebuild.
Do not silently replace its tag or overwrite accepted definitions while running.

```powershell
docker compose --env-file ../whatomate-addons-host/.work/synthetic-local/compose.env -f deploy/local/compose.yaml build controller
./scripts/local/processing.ps1 -AcceptSyntheticSignatureWindow
```

The script verifies each official CVD signature with `sigtool --info`, creates a
one-hour **synthetic test acceptance window**, copies operator-owned configuration
into a Linux private volume with mode 0400, and starts the separate trusted queue
controller. It does not assert production freshness policy. An expired window
fails closed; inspect and revalidate before another local window. ClamAV 1.4.4
has an updater recommendation for a newer engine; production acceptance remains
an explicit unresolved gate.

Only the controller receives the Docker socket. The HTTP app receives neither
that socket nor controller configuration. The controller claims durable uploads,
maps upload metadata into the document worker's distinct metadata representation,
starts an offline bounded worker, and verifies identities, both metadata hashes,
input bytes, exact serialized artifact hash (including encoder newline), image,
definition digest, acceptance expiry and page provenance before completion.
Unknown leases are not automatically retried. A completion DB error leaves an
unknown outcome for reconciliation. Worker stdout/errors are not copied into
user-visible records or logs.

## Backup, restore, reset and rollback

```powershell
./scripts/local/runtime.ps1 backup
./scripts/local/restore-check.ps1 -Backup ../whatomate-addons-host/.work/synthetic-local/backups/<timestamp>.backup
./scripts/local/manifest.ps1
```

Backup creates a real PostgreSQL custom archive under the private directory.
Restore checks accept only that directory and create a **new timestamped
database**, preserving the active database. They verify restored user/case counts.
That check does not establish full application consistency: media, worker replay
markers, encryption keys, privacy deletion/hold journal reconciliation and a
restored application rehearsal are separate release gates. Preserve required
private configuration/keys and data volumes alongside a release; never place
them in public Git or the tender evidence bundle.

For rollback, stop the app/controller, retain the failing state and current
backup, restore the reviewed source/configuration and embedded frontend, then
restart and run health/acceptance checks. Do not run an older binary against an
incompatible migrated schema. Schema rollback requires a separate restored DB
and explicit privacy-journal reconciliation. Automated production rollback is
not claimed by these local tools.

The separate rehearsal tools preserve the active app:

```powershell
./scripts/local/capture-baseline.ps1
# After a populated backup has been restored into a separate DB and its
# independent deletion/hold journal has been reconciled OFFLINE:
./scripts/local/rehearse-rollback.ps1 -Baseline ../whatomate-addons-host/.work/synthetic-local/rollback/<baseline> -RestoredDatabase synthetic_restore_<timestamp> -ConfirmIndependentJournalReconciled
```

Capture copies the actual running binary (including its embedded UI) and private
configuration with SHA-256 identities. The rehearsal verifies that binary,
changes only the restored database and separate Redis connection, starts without
migrations, and binds its separate server to `127.0.0.1:18281`. The explicit
confirmation flag asserts a prerequisite; it does not itself reconcile a
journal. The tool records `functional verification pending` until restored
authorization, schema compatibility and deletion suppression have been tested.
Never use it to expose restored business objects before journal reconciliation.

The controlled local owner sequence is:

1. Capture original worker provenance from the older offline snapshot using
   `owner-reconcile.ps1 -Mode capture-provenance -Target synthetic_restore_<timestamp>`.
2. Use `owner-reconcile.ps1 -Mode erase-worker -Target source` to complete only
   the isolated approved request's worker artifacts under the governance hold
   lock. The helper validates actual original manifest/worker receipt, removes
   only the two known retained files and exact hashed job directory, fsyncs a
   durable independent receipt, and verifies absence. It never recursively
   deletes the worker volume or accepts a browser removal assertion.
3. `capture-authority.ps1 -Name <unique-name>` captures the **full newer**
   governance document/event authority, including the confirmed worker receipt.
   `import-authority.ps1 -Name <unique-name> -Target synthetic_restore_<timestamp>`
   verifies the private archive hash, schema fingerprint and matching organisation,
   requires both target governance tables empty, then imports data atomically.
   It will not upsert or overwrite an existing journal.
4. `owner-reconcile.ps1 -Mode restore -Target synthetic_restore_<timestamp>
   -RestoreKey <unique-logical-restore-id>` invokes the storage owner to increment
   restored object generations and actually reapply the approved SQL erasure.
   Follow with `-Mode erase-worker` against that restored database to reverify
   the independent disk receipt/absence for its new journal generation.
5. Only after checking source objects absent and all relevant owner journals
   confirmed may the separate previous-binary application be started.

The host `cmd/extensions-reconcile` is explicitly synthetic-only. It reparses an
original signed access token, validates its current active identity, Redis session,
MFA generation and exact current grants against the active source, and only then
supplies that verified actor to storage-owner ports. It never manufactures an
MFA actor, enrolls factors or resets credentials. Only the fixed isolated synthetic
request is supported by this development command. It has no HTTP endpoint.

A populated local rehearsal on 20 October 2026 restored four cases and seven
published documents, reapplied removal of one separate retired fixture including
its retained worker artifacts, and started the captured previous six-module
binary against the reconciled database on port 18281. Twenty-five actual API
checks passed using existing-factor authentication and separate restored Redis,
including access denial, redaction, subject conflicts, removed-content denial,
preserved PDF equality and closed-month report controls. The active seven-module
app remained available. Subsequent topology review found that the original
rollback service names implicitly registered `redis` and `app` aliases on the
main network. The original 25 functional checks therefore did not establish
network isolation. The corrected topology uses uniquely named `rollbackapp`
and `rollbackredis`; rollback Redis joins only its own internal network. Only
the rollback app joins the main network to reach the dedicated restored database.
`assert-network.ps1` inspects live container aliases before and after rollback
startup and refuses foreign owners of the reserved main `redis`, `app` and `db`
aliases. Failed post-start inspection stops only the rollback services.
After correction, the original checks were rerun with fresh existing-factor
source authentication: 27 checks passed, including two explicit source login/MFA
checks. Redis DNS resolved to one correct address from each app. No factors,
passwords or keys were reset. These results establish this bounded synthetic rehearsal;
they do not establish production rollback, backup expiry or carrier erasure.

`reset.ps1 -ConfirmSyntheticReset` stops only this fixed Compose project and
checks each of four fixed volume ownership labels before deleting its dedicated
DB/media/build caches. It never deletes external ClamAV definitions or document
replay markers. Private credentials and backups remain; archive old identities
before seeding again. The reset operation has intentionally not been run against
the in-progress acceptance environment.

## Evidence and limitations

`manifest.ps1` records source HEADs/dirty paths, exact running base-image IDs,
configuration and roster hashes, and expected model digests without secret values.
For a recorded release also retain reviewed source tree hashes, actual installed
model digests, content/processing receipts and passing test outputs. A dirty HEAD
alone is not an immutable release identity.

The protected-host refusal remains intact. Do not enable protected mode merely
to remove that safeguard. This profile uses isolated add-on data and synthetic
input; generic host compatibility, provider transport, deployment locality,
production resource limits and instance-specific workload preservation require
their own acceptance before activating the intended managed instance.
