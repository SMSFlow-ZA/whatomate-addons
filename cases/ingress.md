# Optional host ingress authorization

`HTTPConfig.AuthorizeIngress(context.Context, *sql.Tx, api.Actor, key string)
error` is a trusted host hook. The actor is the authenticated current HTTP
actor; the string is the original intake key. The SQL transaction is the
transaction which will write the case, intake replay record, event and outbox.
The hook executes before any case locks, idempotent replay lookup or writes.
Any error aborts the transaction, including attempts to replay an old key.

Direct storage users can set
`SQLRepository.AuthorizeIntake(context.Context, *sql.Tx, organizationID,
key string, proposed Case) error`. The HTTP handler verifies proposed contact
and organization match the authenticated actor before invoking its hook.

The hook may coordinate a host-owned privacy write barrier and tombstones
without importing host storage into this reusable module. Acquire that barrier
before querying/locking source records; the erasure path must take the matching
exclusive barrier before source discovery. Check the actor identity as well as
the intake key if the host needs to prevent new-key resurrection after erasure.
Do not fall back to allowing writes on lookup failures.

The callback is optional for backwards-compatible standalone composition.
Hosts that enable physical erasure must explicitly install their policy. This
module does not claim cross-domain erasure protection when the hook is absent.

`HTTPConfig.BeforeMutationTx(ctx,*sql.Tx,api.Actor) error` installs a current
authority fence on repository transactions and list/fact disclosures. It runs
before source locks; direct trusted users can set the matching
`SQLRepository.BeforeMutationTx(ctx,*sql.Tx) error`. The host must compare the
original trusted actor's exact permissions, grants and historical aliases with
current authority, and hold revocation locks until commit/rollback. A captured
HTTP Principal is not refreshed by this source module. A changed actor must be
denied rather than silently retaining the original Principal's broader access.

Use one lock order across host paths: privacy barrier, access-version barrier,
session/membership/factor, entitlement/directory, then source. `api.AccessStore`
offers `LoadTx` to read scopes/aliases on that same connection. Do not invoke a
second transaction that reacquires a barrier already held by its caller. Trusted
workers may instead carry explicitly validated durable delegation; this hook
does not invent an HTTP session or MFA proof for them.

`BeforeAssignmentTx(ctx,tx,operator,candidates)` fences candidate identity and
exact grants for reassignment and participant linking. It executes before the
operator fence, so the host can acquire all operator/candidate access barriers
in sorted, deduplicated order before taking membership locks. ResolveCandidate
alone is a preview and cannot authorize a later write from a stale snapshot.

`ResolveIntakeUnit(ctx,actor,requestedUnit,classification)` is an optional trusted
host mapping port. Without it, the legacy exact `classification.Zone == unit`
rule remains. Hosts with explicit owner-appointed case units may validate the
requested exact SubmitOwn unit independently, retaining the original geographic
Zone and Department as reporting facts. This callback never grants SubmitOwn;
the HTTP and transaction authority boundaries must still require that grant.
