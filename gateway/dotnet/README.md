# Per-instance ER inference gateway (.NET 10)

AGPL-3.0, as covered by this repository's license. This is generic application
source. Keep deployment identifiers, credentials, private model configuration,
customer material and release evidence outside this repository.

The gateway accepts one trusted Whatomate service identity. It does not accept
user identities, tenant IDs, permissions or case actions from a request. A unique
bearer token binds the service to one server-configured instance. The public
`GET /healthz` reports only process liveness; it does not attest Azure readiness.
There are no case mutation, tools, public chat or runtime plug-in APIs. A private
append-only monetary reservation journal stores only instance, currency, approval,
time and reserved amounts; no prompts or employee information are written there.

## Host and Azure contracts

`POST /v1/chat/completions` accepts the bounded request emitted by `ai.Provider`:
logical `model`, two string `messages` (system then user), optional JSON object
`response_format`, zero `temperature`, and bounded `max_tokens`. The user message
must contain the Go `ai.Request` shape (`Question`, `Sources`, including source
`Reference`, `ManifestHash`, `Page.Number`, `Page.Text`). A separate classifier
shape contains `text` and `allowed_labels`. Unknown fields and duplicate property
names are rejected at these boundaries. Source ownership, approval, current ACLs,
publication and withdrawal checks remain authoritative in the Go service. A
service token is not proof of publication or employee authorization.

Client wrapper instructions are discarded. Generation sends only minimized
question/page text and temporary opaque source references to Azure. Document IDs,
manifest hashes, original citation references, contacts, actor/permission data
and case identities are not forwarded as metadata. Common email, South African
phone, numeric identity and UUID patterns are masked. This is **not complete PII
redaction**: names, unusual identifiers and personal narratives cannot be detected
reliably by patterns. Host callers must supply approved policy context rather
than raw cases, narratives or contacts. Recognized personal narratives and
sensitive/consequential topics take a human handoff before any upstream call.
The conservative risk list may also hand off informational sensitive questions.

Simple policy questions use GPT-5 Nano with minimal reasoning; comparisons, conflicts, exceptions and
other recognized nuance use GPT-5 Mini with low reasoning. This is deterministic advisory routing,
not an employment decision. Unsupported/ungrounded output, missing sources,
model refusals, incomplete reasoning or tool calls produce a safe handoff.
Only literal approved source excerpts and mapped source citations are returned.
Go must still validate the original exact excerpt and recheck current sources.
If masking makes an excerpt differ from the original, Go correctly hands off;
do not undo masking to force a successful answer.

Classification extracts one unambiguous fixed topic tag (attendance, workplace
concern, conflict of interest or escalation). Only that tag and the fixed allowed
taxonomy are sent to Nano; the original free text is never forwarded. Missing,
ambiguous, unsupported or sensitive signals return `422 human_review_required`.
A matching model label receives a fixed provisional rationale and metadata
`classification_basis=derived_topic_only`. It carries no action authority.

`POST /v1/embeddings` accepts logical `model`, 1–20 bounded text strings in `input`,
`dimensions` (1–4096), and `encoding_format=float`. Embedding inputs pass the same
minimization and risk checks and processing-scope approval as chat. Responses
require every explicit index, finite nonzero vectors of the requested size and
the configured actual model identity. This adapter does not implement pgvector
or replace the existing governed PostgreSQL retrieval catalog.

The Azure target is fixed to a resource HTTPS URL ending `/openai/v1/`, with a
`.openai.azure.com` hostname and no query, credentials, alternate port or fragment.
Outbound TLS uses platform trust, no redirect, proxy, retry or alternate endpoint.
Service credentials are never forwarded. Azure receives its separate API key.
Reasoning requests use `max_completion_tokens` (reasoning plus visible output)
and omit unsupported `temperature` and legacy `max_tokens`, following the
[Azure reasoning contract](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/reasoning).

Actual Nano/Mini versioned model identities are checked before replacing the
OpenAI response `model` with the host's logical alias. Metadata retains actual
`upstream_model`, Azure `deployment`, validated token usage, `processing_scope`
and configured international disclosure. Alias normalization never asserts that
a single model generated every answer. Embedding model identity is also checked.
An operator-controlled immutable Azure deployment/version binding and independent
release review remain necessary; an API response alone cannot prove weights.

## Configuration

All keys use the `Gateway__` prefix. No customer values are checked in.

| Key | Requirement/default |
| --- | --- |
| `InstanceId` | Explicit server-bound instance identity |
| `ServiceTokenFile` | Recommended absolute readonly file path; unique token of 32–4096 nonwhitespace characters |
| `AzureApiKeyFile` | Recommended separate absolute readonly file path; token differs from service identity |
| `ServiceToken`, `AzureApiKey` | Inline alternatives, mutually exclusive with corresponding file setting |
| `AzureEndpoint` | Fixed `https://<resource>.openai.azure.com/openai/v1/` |
| `NanoDeployment`, `NanoModel` | Deployment name and exact returned `gpt-5-nano-YYYY-MM-DD` identity |
| `MiniDeployment`, `MiniModel` | Deployment name and exact returned `gpt-5-mini-YYYY-MM-DD` identity |
| `EmbeddingDeployment`, `EmbeddingModel` | Explicit embedding deployment and returned model identity |
| `ChatAlias`, `EmbeddingAlias` | Distinct logical identities matching Go configuration |
| `ProcessingScope` | `za_only` by default; international processing requires explicit `global_standard` |
| `DeploymentAttestation` | Genuine reviewed evidence reference for ZA hosting/storage and declared model processing scope |
| `InternationalProcessingApproval` | Mandatory reviewed approval reference for `global_standard` |
| `InternationalProcessingDisclosure` | Mandatory meaningful disclosure text for `global_standard` |
| `MaxConcurrentRequests` | 2, allowed 1–8 |
| `MaxRequestsPerMinute` | 30, allowed 1–600 |
| `MaxTokenBudgetPerMinute` | 65536, allowed 1024–1000000 |
| `MaxInputBytes` | 32768, allowed 1024–65536 |
| `MaxCompletionTokens` | 2048, allowed 128–8192 |
| `TimeoutSeconds` | 45, allowed 1–55 |
| `MaxResponseBytes` | 1048576, allowed 1024–1048576 |

Every request has a 128 KiB body cap, bounded parsing depth and deadline. Budget
reservations conservatively use serialized UTF-8 input bytes plus all allowed
output tokens. Every admitted attempt consumes its complete reservation, including
errors/unknown outcomes; no automatic retry/refund occurs. These minute/concurrency
budgets are per running process and reset on restart. Production additionally
requires a durable monetary reservation journal as described below. Run exactly
one gateway process per instance; independently set Azure quotas, budgets and
alerts. Upstream 429 returns a safe 429 without retry;
provider failures return sanitized 502/504 errors, never upstream response bodies.

`za_only` requires genuine evidence of all-ZA processing and hosting/storage.
`global_standard` requires separately approved international processing and an
honest disclosure for **both inference and embedding generation**. South African
RAG storage does not imply South African model processing. No region, approval
or locality evidence is inferred from an Azure hostname or a configuration label.
Go independently validates the same scope/approval/disclosure. The gateway has no
stored prompts or vector database; host retrieval/persistence remains separate.

### Durable monetary reservations

Production requires `SpendingLedgerPath` (absolute path to an existing journal),
`SpendingCurrency` (three-letter ISO code), `MonthlySpendLimitMinor` (positive minor
currency units), `DailySpendLimitMinor` (zero to omit a separate daily restriction,
or no greater than the monthly limit), `MaxCostMinorPerMillionTokens` and
`SpendingApprovalReference`. There are no inferred prices, currency conversion or
default approvals. Review the maximum tariff across all pinned generation and
embedding deployments, including exchange-rate/tax/surcharge headroom. This is a
conservative reservation ceiling, not billing reconciliation or a promise about
unrelated users/keys calling the Azure resource.

Each upstream attempt reserves its serialized UTF-8 bytes plus the maximum output
tokens and 1,024 tokens of framing headroom, all at that maximum tariff. The minor
amount rounds upward. The reservation is appended and fsynced before network I/O;
no provider error, timeout, cancellation or unknown result refunds it. Minute and
concurrency refusal occurs before monetary reservation. A full cap returns 429
without contacting Azure. UTC calendar months/days apply; clock rollback denies
requests. A 16 MiB history bound stops further requests rather than rotating away
history. Missing, incomplete, identity-mismatched, linked or already-open history
refuses startup; storage/write uncertainty stops further calls.

Initialize only once as an explicit offline operator operation with the same
configuration: `dotnet ErGateway.dll --initialize-spending-ledger`. Initialization
uses exclusive creation and cannot overwrite an existing journal. Create the
existing parent directory on approved persistent storage owned by gateway UID/GID
1654 with mode 0700; the journal is mode 0600. Mount the directory read-write into
the otherwise read-only gateway container. Normal startup never initializes or
resets history. The runtime holds an exclusive file share for its lifetime; prove
cross-process locking and durable fsync on the actual NFS target before release.

Include the journal in encrypted backups and accounting reconciliation. A valid
older snapshot is not detectable using a standalone file alone: restored gateways
must remain outbound-held until independently current spending history is
reconciled, or the remaining billing period is conservatively exhausted. Do not
reset a ledger, change instance identity or prune history to reopen allowance.
Application hash chaining detects accidental chain damage; it does not defend
against a privileged operator rewriting both data and hashes.

Production startup requires mounted certificate and private key paths:

```text
ASPNETCORE_URLS=https://+:8443
ASPNETCORE_Kestrel__Certificates__Default__Path=/run/secrets/gateway.crt
ASPNETCORE_Kestrel__Certificates__Default__KeyPath=/run/secrets/gateway.key
Gateway__ServiceTokenFile=/run/secrets/gateway-service-token
Gateway__AzureApiKeyFile=/run/secrets/gateway-azure-key
```

Plaintext production bindings are rejected. Supply a private-network certificate
whose name matches the host-configured gateway endpoint. Mount only its trusted
CA into the caller; do not disable TLS verification. Certificates/private keys
are runtime mounts, never image layers. Runtime UID/GID is 1654; use root-owned
readonly secret files readable by that group. Do not expose gateway host ports
or share its upstream key with Whatomate. Use a read-only root filesystem, bounded
tmpfs, separate instance network, CPU/memory limits and dropped capabilities.

Audits log instance ID, request hash, model, status, processing scope and token
counts. Prompts, answers, identities, bearer headers and upstream errors are not
logged by this application. Do not enable body/header logging or third-party
telemetry capture around it. Review reverse-proxy and Azure diagnostic retention
separately as part of the deployment evidence.

## Build and verification

```sh
dotnet run --project ErGateway.Tests --no-launch-profile
dotnet publish ErGateway -c Release -o <release-directory>
docker build --build-arg SDK_IMAGE=<reviewed-dotnet10-sdk-image@sha256:digest> \
  --build-arg RUNTIME_IMAGE=<reviewed-aspnet10-image@sha256:digest> \
  -t <private-release-tag> .
```

Docker build context is this `gateway/dotnet` directory. The Dockerfile runs the
dependency-free executable test suite before publishing and has no floating base
image defaults. Production release tooling must enforce reviewed digest arguments.
The suite starts real in-process Kestrel HTTP/TLS listeners and uses a controlled
upstream transport, covering configuration, service auth, instance binding, model
routes, PII minimization, derived-topic classification, citations, handoff, upstream
errors, budgets, concurrency and deadlines. It performs no cloud calls. Run it as
a bounded executable (`dotnet run`), not `dotnet test` discovery.

This source and synthetic test suite do not establish deployed residency, live
Azure availability, clinical/legal/ER quality, cost acceptance or customer policy
approval. Those remain independent reviewed deployment and release gates.
