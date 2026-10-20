# Local legacy XLS worker

The optional directory LegacyWorker adapter invokes this parser by a trusted
absolute executable path. It never opens URLs or evaluates formulas/macros.
`xlrd` reads stored formula results; such cached values must still pass mapping,
preview validation and human approval. Registered numbers must be canonical text.
The default Go `ReadXLS` remains disabled unless this worker is configured.

Build with a reviewed immutable Python image digest:

```sh
docker build --build-arg PYTHON_IMAGE=python@sha256:<reviewed-digest> -t local-xls-worker .
docker run --rm -i --network none --read-only --memory 512m --pids-limit 32 \
  --cap-drop ALL --security-opt no-new-privileges local-xls-worker --sheet Staff < synthetic.xls
```

The Linux executable adds CPU, address-space, file-size and descriptor limits.
Timeout and output limits also apply in the Go adapter. stdin contains at most
the configured upload size; stdout is bounded JSON; stderr never contains cells.
Use a disposable non-serving quarantine volume and no cloud/customer credentials.
Do not grant a web process Docker socket access. A production worker supervisor
must enforce these settings independently of uploaded content.

`test_reader.py` generates synthetic BIFF files using test-only xlwt 1.3.0 and
tests real parsing, bounds, malformed data and absence of formula evaluation.
Production installs only hash-pinned xlrd 2.0.2. Tests of the pure parser can run
on Windows; the executable refuses Windows because required resource limits
are unavailable. No worker image is production-approved by these tests.
