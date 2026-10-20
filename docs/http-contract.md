# Trusted compiled-module HTTP contract

Module handlers use standard `net/http` under `/api/extensions/<module>/`.
The host authenticates a signed, current, MFA-verified session, resolves current
explicit role permissions and scoped grants, then supplies `api.WithActor`.
No module trusts an actor, role, MFA value, unit grant or audience from a body.
API keys cannot access protected operator endpoints. Employee channel eligibility
is separate from operator MFA and is not represented as an operator session.

`api.Require` validates trusted MFA/session/identity and an exact permission;
case/work scope additionally requires `Actor.Allows(permission, unitID)`.
Knowledge access additionally requires the exact audience grant. There is no
implicit administrator, wildcard unit or wildcard audience grant. Host grant
changes must be resolved on each request and each protected event delivery.

Inputs use snake_case JSON, one bounded value and rejected unknown fields.
Success is `{ "data": ... }`; failures are `{ "error": { "code": ... } }`.
Return 400 invalid input, 403 unavailable/denied module action, 404 equally for
unknown and unauthorized case, 409 version conflict, 503 persistence unavailable.
Never return raw database errors, narratives or authentication secrets in errors.
Responses have `Cache-Control: no-store`. Mutation DTOs carry expected_version.

Each module provides `NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler`
and owns HTTPConfig and routes under its prefix. Constructors do not migrate.
Root composition runs explicit package migrations before serving requests.
Module code neither imports host internal models nor writes host core tables.

Case creation captures immutable zone/department/type facts; case events retain
stable case/version IDs and recorded/occurred timestamps. Reports use the case
package's authorized event/fact API, never another package's private SQL tables.
Workers use trusted immutable job/input/output hash linkage. Unknown processing,
commit or provider outcomes remain explicit; no claim of successful completion.

The compiled host's canonical case collection URL ends in a slash: `/api/extensions/cases/`. Clients must POST directly to that URL so an HTTP redirect cannot change the method. Single-record/action URLs remain unsuffixed.
