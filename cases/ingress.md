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
