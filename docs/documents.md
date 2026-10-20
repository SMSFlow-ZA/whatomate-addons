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
- `/output/result.json`: completed result, created exclusively with mode 0600.

Inputs reject symlinks and oversized/nonregular files. The configured
`QuarantineRoot` must be `/tmp/quarantine`. A temporary output is fully written,
synced and closed before linking it exclusively as `result.json`; an existing
result is never overwritten. Failure emits only a generic error and no successful
result. Extracted content goes to the private result file, not application logs.
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
10001. No image was built, published or deployed as part of these source changes.

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
