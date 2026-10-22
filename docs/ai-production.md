# Reviewed HTTPS AI composition

`ai.NewProductionProvider(ProviderConfig, EmbeddingConfig)` supplies generation,
embedding and provisional classification through OpenAI-compatible HTTPS APIs.
This constructor validates configuration; it cannot attest that infrastructure
actually meets a locality or privacy policy. Operators must supply a genuine
reviewed deployment evidence reference covering inference, embedding, storage,
monitoring and support access for the declared processing scope, with `Country: "ZA"` for hosting/storage. `ProcessingScope` defaults to `za_only`, preserving the all-ZA processing requirement. Explicit `global_standard` requires both `InternationalProcessingApproval` and a meaningful `InternationalProcessingDisclosure`; the reviewed evidence must cover international inference and embedding generation without claiming all-ZA processing. Evidence is never generated
from an endpoint name, a model identity or a development profile.

Generation and embedding have independent explicit endpoint URLs, each present
verbatim in `AllowedEndpoints`. URLs must use HTTPS, with no user information,
query or fragment. Generation model, embedding model, embedding dimensions and
prompt revision are explicit identities that must match the reviewed deployment.
Model identity in both response envelopes must match the configured identity.
Use an operator-controlled immutable deployment identity; a mutable alias alone
cannot prove the underlying weights. The optional bearer token is shared between
these explicitly reviewed endpoints; allowlist only trusted destinations.

The client constructs its own TLS-verifying transport, bypassing process-global
transport overrides and proxy configuration. It follows no redirects, retries or
alternate endpoints. Request bodies, responses and deadlines are bounded. The
embedding response requires indexed, correctly sized, finite, nonzero vectors;
duplicate or invalid indices and model mismatches fail closed. Classification
accepts only host-provided taxonomy entries with a bounded rationale and rejects
extra fields or tool calls. Suggestions carry no action authority and still
require independently authorized, durably audited human approval.

Host operator assistance and employee conversations should use the same explicit
provider constructor. The host selector is `https` for production; `development`
is restricted to synthetic development and pinned local models. A legacy blank
selector is accepted only for an explicitly synthetic development profile.
Production plus synthetic development is rejected. Missing or invalid production
configuration never falls back to a development model.

Keep `ai.Service` and governed retrieval around the provider. Current audience,
source publication and provenance are checked by retrieval before inference and
rechecked before release of an answer. The model may return only a literal
approved excerpt with validated citations. No approved source, withdrawn source,
provider failure or unsupported output yields `FixedResponse` with a human
handoff. Do not call raw generation as a replacement for this service boundary.

The production adapter unit tests use isolated controlled TLS transports. They
verify transport and response contracts, not deployed residency, real model
quality, operational availability or acceptance of a live ER workload. Those
remain release gates for the actual reviewed deployment.

The optional per-instance .NET 10 Azure gateway is documented in [gateway/dotnet](../gateway/dotnet/README.md). It keeps Azure credentials separate from the host service bearer and verifies actual model versions before returning logical compatibility aliases. Its gateway metadata records actual deployment/model, usage and processing scope; aliases are not model-weight attestations.
