# Coordinated SQL physical erasure

`LockScopesTx`, `PhysicalService.DiscoverTx`, `EraseRequestsTx` and
`ReconcileRequestsTx` are additive trusted supplied-transaction ports. Existing
single-scope methods retain their behavior. A caller owns commit/rollback and
must roll back on every error; adapters may mutate only their supplied SQL
transaction and must not perform external side effects.

Lock the complete sorted scope set before any source locks. The host must bind
the complete affected unit set, independently approved scoped requests and
current actor authority. The generic package cannot infer shared identity
ownership. Each request must be an approved deletion for the same subject and
the actor needs exact deletion-record authority in every scope. Every source is
rediscovered, and every hold/retention plan is checked before any physical
mutation. Derivatives precede parent cases across the entire batch; core and
directory records follow. All scoped journals share the caller transaction.

`ReconcileRequestsTx` is offline-only: the host must first restore and verify
independently current scoped journals, keep the target inaccessible, and check
all resulting physical states before clearing its own restore gate. It advances
restored generations once per restore reference, then uses the same all-scope
erase preflight. It grants no approval and enables no external action.

The integration regression exercises actual two-unit case storage: a hold in
the second unit leaves both source rows unchanged even before rollback; released
holds permit both scoped receipts in one commit. Host composition must also test
its own shared-object routing, current-session fences and isolated restore gate.
