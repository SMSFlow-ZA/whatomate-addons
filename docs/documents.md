# Offline PDF quarantine and extraction worker

`documents` implements a real local CLI adapter. It accepts bounded PDF bytes and
publication metadata, never uploaded `knowledge.Evidence`. `documents.New`
requires Linux, absolute trusted executable paths, a private quarantine root,
configured scanner/extractor/OCR revisions, a provisioned ClamAV signature
directory, a future signature-validity deadline and explicit resource limits.
Unavailable tools, absent definitions, expired configuration and invalid limits
fail readiness. No readiness default asserts that an antivirus or OCR service
exists.

`Process(ctx, pdfBytes, metadata)` copies at most the configured input size into
a private randomly named directory (0700), with an original file mode of 0600.
The directory is never served. It invokes genuine `clamscan` first, with a fixed
signature database, scan size controls, no following symlinks, and detection of
scan limits. Every nonzero exit, timeout or output overflow fails the job. No
PDF parser is invoked after a failed scan. The public API exposes no executor
injection; the private executor seam is used only by contract tests.

After scan success, `pdfinfo` must report one valid bounded page count and no
encryption. Each original page is extracted separately with `pdftotext`. Pages
without text are rendered by `pdftoppm` at a bounded resolution into bounded
stdout, written to a private raster file and processed by `tesseract` with the
fixed English language and page segmentation configuration. Blank pages are
preserved; a successful OCR command can yield empty text for a blank page.
Unsupported/encrypted/partial/oversized documents fail closed. Stderr diagnostics
are separately bounded and never appended to source text. Tool paths and options
are trusted startup configuration; no shell, document filename or uploaded
metadata can inject command arguments or select executables.

Only after every page succeeds does the worker return `knowledge.Document`,
`knowledge.Evidence` and page provenance. It rechecks the original file hash
before releasing output. Provenance includes original SHA-256, original page
number, extraction method/revision, normalized text SHA-256 and raster SHA-256
for OCR pages. The scanner revision comes from trusted startup configuration,
not metadata. This is a real scanner invocation contract, not a guarantee of
perfect malware detection or OCR accuracy. Human review and exact-manifest
approval remain required before publication.

Limits have hard upper bounds: PDF 20 MiB; 100 pages; page text 1 MiB; total text
16 MiB; page raster 16 MiB; command 30 seconds; processing job 5 minutes. Config
must supply tighter explicit values as appropriate. Subprocesses get a minimal
environment, separate bounded stdout/stderr, context deadlines, Linux process
group cancellation and bounded pipe waits. The host must enforce concurrent-job
limits and a hard container deadline covering startup and filesystem I/O as well
as extraction. The worker is intended to run once per isolated job, not inside
the main application process.

## One-job executable and container

`workers/documents` reads only these fixed paths:

- `/run/config/documents.json`: trusted JSON `documents.Config`, read-only.
- `/input/document.pdf`: one regular, read-only, quarantined PDF.
- `/input/metadata.json`: JSON `documents.Metadata`; unknown fields are rejected.
- `/input/job.json`: trusted `documents.JobManifest`, binding the authenticated
  job/tenant/actor and original-input/metadata SHA-256 values.
- `/output/result.json`: completed result, created exclusively with mode 0600.

Inputs reject symlinks and oversized/nonregular files. The configured
`QuarantineRoot` must be `/tmp/quarantine`. A temporary output is fully written,
synced and closed before linking it exclusively as `result.json`; an existing
result is never overwritten. Failure emits only a generic error and no successful
result. The result is `documents.JobArtifact`, containing the unchanged manifest
and `Output`. The worker independently verifies both input hashes before parsing.
Extracted content goes to the private result file, not application logs.
Temporary spool files are removed on return; the host retains the original
quarantine record on failure. A killed container requires tmpfs teardown. Treat
worker output as a trusted job artifact bound to the host's authenticated job
identifier; never accept an equivalent JSON body from an upload endpoint.

The Dockerfile intentionally has **no floating image defaults**. Build requires
`GO_IMAGE` and `RUNTIME_IMAGE` containing `@sha256:` plus exact Alpine package
versions for `CLAMAV_VERSION`, `POPPLER_VERSION`, `TESSERACT_VERSION`,
`TESSERACT_ENG_VERSION` and `FONT_DEJAVU_VERSION`. Resolve and review those
versions in the release pipeline and build with repository root as context.
No signature database is downloaded during runtime. The image runs as uid/gid
10001. A local acceptance image was built as described below; none was published
or deployed.

Run each reviewed image with:

- `--user 10001:10001 --read-only --network none --cap-drop ALL`.
- `--security-opt no-new-privileges --pids-limit 64 --cpus 1 --memory 1g` (validate
  actual scanner memory needs before increasing any bound).
- A private `/tmp` tmpfs with `rw,noexec,nosuid,nodev,size=128m,uid=10001,gid=10001`.
- Read-only `/input`, `/run/config` and `/var/lib/clamav` mounts; a private
  nonserved `/output` mount owned by uid 10001 with mode 0700.
- An external hard wall-clock supervisor that kills/removes the entire container
  if the job deadline is exceeded, plus an approved seccomp/AppArmor policy.

The orchestrator must refuse execution if these controls are absent. A Dockerfile
alone does not impose network isolation, runtime mounts or resource limits.
Provision definitions via a separate controlled update workflow; review scanner
version and database revision/digest, verify definition signatures, track expiry,
and mount the accepted corpus read-only. `SignaturesValidUntil` is a trusted
operator gate; this package does not independently attest database freshness or
revocation. Scanner availability/definition quality must pass release acceptance.
Do not attach this worker to a public network or reuse untrusted output as scan
evidence.

## Verification and limits of evidence

Contract tests cover scanner-first denial, OCR page/hash provenance, input/page/
text bounds, OCR failure, expired readiness, private spool modes, changed-original
rejection, diagnostic isolation, actual process timeout/output cancellation,
symlink denial and forged metadata evidence. The generated PDF fixture is original
synthetic test data. When real Poppler, Tesseract, English OCR data and substitute
fonts are installed, `TestRealLocalPDFTextAndOCRTools` exercises genuine page
counting, text extraction, raster rendering and OCR. It explicitly skips when
those CLIs are absent.

Real-tool tests passed in disposable containers with Poppler/Tesseract/English
data/DejaVu and ClamAV installed. A genuine ClamAV integration test used a single
explicitly synthetic local `.ndb` signature: it scanned and extracted the clean
PDF, then detected a known synthetic marker and returned no verified output.
This uses the actual scanner engine; it is not an accepted production signature
corpus or a production malware-detection claim. That test uses the private
constructor solely to permit its isolated test corpus; public readiness still
requires provisioned CVD/CLD definitions. Scanner success in separate contract
tests is an explicitly injected test response. Production still requires
accepted definition-corpus clean/infected/error/limit fixtures, reviewed and
pinned tool/definition images, operational sandbox acceptance, authenticated
job orchestration and human publication review. No customer material is included.

## External authenticated job supervisor

`NewSupervisor(SupervisorConfig)` and `Supervisor.Process(ctx, Job)` provide the
operational controller boundary. The controller runs on Linux separately from
HTTP. Only this external process has Docker access. The web process must enqueue
a durable immutable job using its current authenticated tenant/actor and a
server-computed input hash; it must never invoke Docker, accept worker results
from a browser, or mount a Docker socket. A trusted queue runner claims that job,
loads the original bytes and metadata, and calls the supervisor. Queue ownership,
claim expiry and the transaction that saves verified output belong to the queue
adapter. This package deliberately owns no knowledge tables or publication state.

`Job` carries `ID`, `TenantID`, `ActorID`, `InputHash`, `Input` and `Metadata`.
Identity is trusted queue context, not a user authentication mechanism. The host
must authenticate the claimant and protect the queue/controller pipe. Input and
metadata are copied, hashed and written exclusively to a private job directory.
Atomic directory creation records the tenant/job ID permanently, including failed
jobs, so retries need a fresh ID and queue-level policy. A replay marker is not a
durable queue and must be retained for the queue's replay-retention period.

Configuration requires a private existing `JobRoot` (0700), trusted absolute
`DockerPath` and definition directory, a reviewed immutable `Image` (`name@sha256:`
or a local `sha256:` image ID), a SHA-256 `DefinitionsHash`, explicit worker tool
configuration/expiry and explicit concurrency/container/deadline bounds. No job
can supply executable paths, images, mount options or tool arguments. Use
`DefinitionsDigest(path)` after a separate official definition update and review;
it requires genuine provisioned CVD/CLD filenames, rejects links/nested entries,
and hashes every regular file in the directory. It records the accepted corpus;
it does not independently attest upstream signatures or freshness. The Docker
runner rechecks that digest before and after every job. Mount a frozen directory,
not one concurrently being updated by freshclam.

Each invocation creates a new uid/gid 10001 container with `--pull=never`,
`--network none`, read-only root and input/config/definition mounts, all
capabilities dropped, no-new-privileges, explicit CPU/memory/swap/PID bounds and
a private bounded noexec/nosuid/nodev tmpfs. The output mount is private and
nonserved. Docker's default seccomp policy remains active; release acceptance
must review the host's seccomp/AppArmor policy. A controller must have permission
to set private input/output directory ownership to 10001. The hard job deadline
cancels the Docker CLI and separately force-removes the named container; the
runner verifies no such container remains before returning an artifact. Cleanup
or verification uncertainty fails the job. `MaxConcurrent` bounds one supervisor
process; bound the number of controller processes/queue claims externally too.

Only after successful worker exit, unchanged input hashes, strict bounded JSON,
exact job/metadata linkage, scanner revision and every page/provenance/text hash
check does `Process` return `Output` plus `Receipt`. Receipt includes job,
tenant, actor, input/metadata/output hashes, immutable image, definition digest,
completion time and configured signature expiry. The output hash covers the
exact worker artifact bytes. The private immutable receipt/manifest and replay
marker remain; input/config/output spool files are removed. The durable queue
retains the governed original and verified artifact. Failed jobs return neither
scan evidence nor a receipt. Receipts are trusted in-process/pipe results, not
signed capabilities that can safely be accepted from an HTTP client. Human
review and exact-manifest approval remain necessary before publication.

`workers/controller` is a one-claim command adapter. Build with:

```sh
CGO_ENABLED=0 go build -trimpath -o document-controller ./workers/controller
```

Install its operator-owned, non-world/group-writable strict JSON
`SupervisorConfig` at `/run/config/supervisor.json`. Feed one bounded `Job` JSON
value on stdin; `Input` uses Go JSON's base64 byte encoding. The command emits
one `{ "Output": ..., "Receipt": ... }` value on its private stdout only after
verification. Any failure returns nonzero with generic diagnostics and no result.
The queue runner must check exit status, bound and strictly decode stdout, bind
all receipt IDs/hashes to its active claim, and save completion transactionally.
Never put the controller binary/socket in the public HTTP process or pipe its
output to a browser. The adapter loads no plugins or runtime uploaded programs.

## Local official-corpus acceptance (20 October 2026)

The official `freshclam` updater downloaded and validated `daily.cvd` version
28159 (355,771 signatures), `main.cvd` version 63 (3,287,027 signatures) and
`bytecode.cvd` version 339 (80 signatures) into a private local Docker volume.
TLS and signature validation were not disabled. The updater's final NotifyClamd
diagnostic reported a missing local daemon config; the offline CLI needs no
daemon, and each downloaded database had already passed its validation test.
The updater (ClamAV 1.4.3) warned that 1.4.6 was recommended. The available pinned
Alpine worker uses 1.4.4, so this evidence does not assert an approved production
engine version or independently accepted production deployment.

The local build used the existing Dockerfile with these exact arguments:

```sh
docker build -f workers/documents/Dockerfile \
  --build-arg GO_IMAGE=golang@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 \
  --build-arg RUNTIME_IMAGE=alpine@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 \
  --build-arg CLAMAV_VERSION=1.4.4-r0 \
  --build-arg POPPLER_VERSION=25.12.0-r0 \
  --build-arg TESSERACT_VERSION=5.5.1-r0 \
  --build-arg TESSERACT_ENG_VERSION=5.5.1-r0 \
  --build-arg FONT_DEJAVU_VERSION=2.37-r6 \
  -t whatomate-addons-documents-acceptance:20261020 .
```

Transitive APK dependencies are repository-resolved; release reproducibility
still requires a reviewed package snapshot/SBOM. Acceptance uses the immutable
local image ID from `docker image inspect`, rather than the mutable tag.
Definition file SHA-256 values were:

| File | SHA-256 |
|---|---|
| daily.cvd | `94fead17812734280917fbc623828c1462943762c96895736468ffffc0068eb3` |
| main.cvd | `0b2182d229f46981ec8f535382222f7c9dfdd656b250ad47988b910a8d302365` |
| bytecode.cvd | `6d4aa01f219e988060fc419f495d07f27e0cdf1a2cccc065971da922c76f7ffb` |

`TestRealSupervisorWithOfficialDefinitions` is an opt-in operational acceptance
test. Supply `DOCUMENTS_TEST_IMAGE`, `DOCUMENTS_TEST_JOB_ROOT` and
`DOCUMENTS_TEST_DEFINITIONS` inside a separate Linux controller with the trusted
Docker CLI at `/usr/local/bin/docker`. Job and definition paths must identify
the same private directories to both controller and Docker daemon (ordinary
Linux host paths, or explicit shared private mounts). The test sets a one-hour
local signature-acceptance expiry, executes the actual read-only offline worker
and retains the resulting private receipt for inspection. Production operator
acceptance, signature refresh/revocation, deployment resource-policy review,
queue restart/outage acceptance and publication review remain required.

The full public-supervisor test passed using the official corpus: the clean PDF
produced verified extracted text, and a PDF containing an EmbeddedFile EICAR
antivirus fixture produced no output/evidence/receipt. A trailing EICAR string
after the PDF EOF was not detected in an initial test fixture; the passing
infected fixture uses a valid PDF attachment. Real PDF text/raster/OCR CLI tests
and the separate genuine synthetic-signature scanner test also passed.

The successful local receipt recorded completion at
`2026-10-20T08:58:38.59472207Z`, configured acceptance expiry
`2026-10-20T09:58:28.911128898Z`, and corpus-directory digest
`0ceb5d53a3980183d880d3e81f68d13bcb1a9ad5bd3ca673e23b3ea6f7350816`
(including `freshclam.dat`). These timestamps describe that test only; they do
not confer ongoing readiness. The receipt image ID was
`sha256:deeb600b4673055387c333172c4a420d5ee1ad26fbcb431db92cd8912ff0268b`;
rebuild and inspect the new immutable ID after further source changes.

The definitions and receipt remain outside the Git checkout. Inspect them with:

```sh
docker run --rm --network none --entrypoint sh \
  -v whatomate-addons-clamav:/defs:ro golang:1.26.9 \
  -c 'ls -l /defs; sha256sum /defs/*.cvd'
docker run --rm --network none --entrypoint sh \
  -v whatomate-addons-docjobs:/jobs:ro golang:1.26.9 \
  -c 'find /jobs/acceptance-2507598464 -name receipt.json -exec cat {} \;'
```

For the local Docker Desktop acceptance controller, mount the job volume at
`/var/lib/docker/volumes/whatomate-addons-docjobs/_data` and the definition volume
read-only at `/var/lib/docker/volumes/whatomate-addons-clamav/_data`, matching their
daemon paths. The controller configuration uses those paths, `DockerPath` set to
`/usr/local/bin/docker`, and the current immutable worker image ID. Production
uses equivalent private Linux host paths. Build the controller with the command
above; its fixed `/run/config/supervisor.json` is separate operator configuration,
never a file supplied by the upload. The local updater used:

```sh
freshclam --stdout --datadir=/var/lib/clamav
```

Run updates in a separate controlled online updater, validate accepted definitions
and expiry, then mount the frozen corpus read-only in the offline controller and
workers. Never update the mounted accepted corpus concurrently with a job.
