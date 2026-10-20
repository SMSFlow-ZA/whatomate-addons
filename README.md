# Whatomate add-ons

Reusable, compiled-in modules for employee eligibility, protected case work,
governed knowledge, assisted answers, reporting and record governance. AGPL-3.0;
see LICENSE. Modules are disabled unless the host explicitly configures them.

This is development source, not a production-ready distribution. No customer
data or configuration belongs here. The host SDK lives at
`github.com/shridarpatil/whatomate/pkg/extensions/v1`. Domain packages must not
import host internal code. Build-time integration, durable adapters and release
acceptance are tracked separately from domain behavior.

Run `go test ./...`, `go test -race ./...` and `go vet ./...`.

See [implementation status](docs/implementation-status.md) for implemented
boundaries and remaining integration. `compose.test.yaml` runs real PostgreSQL
tests; individual docs describe local XLS/PDF/scan/OCR worker verification.
