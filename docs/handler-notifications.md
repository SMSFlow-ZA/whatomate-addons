# Protected handler notifications

The opt-in `notifications` module projects committed escalation events into the
existing durable delivery owner. It sends one fixed, content-free instruction to
open the protected workspace. Delivery is not human acknowledgement or reading.
It never includes a case type, employee name, narrative, source quote or bearer
link. Employee conversation replies retain their existing authorization path.

## Host contract

Run `notifications.Migrate(ctx, db)` after the cases and delivery migrations.
Register `notifications.PrivacyAdapter{}` with the governance physical adapters,
alongside the case owner adapter, before enabling the worker. The three new
tables are `addon_notifications_activation`, `addon_notifications_intent` and
`addon_notifications_erased`. Runtime grants and the protected schema catalog
must explicitly include those tables. Delivery payloads add optional `purpose`,
`recipient_user_id` and `event_version`; no delivery column changes are required.

Construct `notifications.Service` with DB, Tenant, Account, Units, Clock,
Freshness and Resolve. Resolve is a trusted host callback returning Identity:
active same-organization user, a current cases.Principal, and every linked
employee ID. The principal must retain every historical conflict alias and
intersect current role permissions with exact current unit grants. Reject links
to another organization. These are background authority facts, not fabricated
MFA/session claims. Protected workspace access still uses normal authentication.
Resolver policy denials return `notifications.ErrDenied` or `api.ErrDenied`.
Operational errors remain distinct: projection rolls back and can resume after
recovery, rather than permanently consuming an event during a temporary outage.

Call Activate only when explicitly enabled. It fixes the database activation
time and immutable account/unit configuration. Old events are excluded. The
same configuration can restart safely; changing that scope requires an explicit
reviewed migration, rather than silently replaying historical work. Call Project
in a bounded worker loop before normal carrier dispatch, and set
`carrier.Service.AuthorizeHandler = service.Authorize`. Omitted configuration
must omit both projection and the authorization callback.

The case owner exports only organization, unit, case, version, event time and
handler ID through an explicit background NotificationScope. Project scans at
most 100 facts and atomically commits scan progress, deduplication and enqueue.
Circular rescans catch late commits without assuming event timestamps or sequence
allocation are commit order. Identical replays are harmless. Conflicting delivery
payloads fail, including legacy employee intents; transient state/lease fields
are excluded from intent meaning.

Every enqueue and dispatch rechecks the exact assignment and work scope, all
subject aliases, active host membership and one unique currently eligible linked
roster identity. A handler's home zone need not equal an authorized case unit.
Missing or ambiguous mapping leaves a durable blocked projection. Reassignment,
closure, conflicts or revoked permission before dispatch hold the unsent delivery.
Blocked/held notices are not automatically retried after policy changes.
For handler notices, a temporary authorization outage returns the typed
`delivery.ErrAuthorizationRetryable` before committing a sending lease. The
queued transaction rolls back, the bounded worker returns the error, and a later
scheduled tick can retry authority evaluation. Once a send adapter has been
called, errors remain unknown outcomes and never trigger automatic resend.

## Transport and governance

The initial route is text inside an existing authenticated WhatsApp window.
Outside that window the existing carrier policy holds the notice. No template
transport or handler SMS fallback is implemented. Unknown submission and expired
leases retain held_unknown, never creating a retry or fallback. Signed receipt
correlation remains tenant/account/intent/provider bound; receipt reconciliation
also invokes current handler policy, without requiring the sending window to
remain open. Revoked authority leaves receipt evidence pending and preserves the
last known delivery state.

Notification inventory uses case lineage, so case holds cover notice derivatives.
Recipient-owned notices have separate per-subject inventory IDs and an immutable,
owner-validated `HoldLineageIDs` dependency on the same case. This protects the
notice under the case hold without giving recipient deletion authority over the
reporter's case. These bounded dependencies cannot be submitted by HTTP or
removed during rediscovery; deletion, correction and restore all honor them.
Holds on a shared notification must explicitly target its canonical case lineage.
The hold API rejects dependent recipient roots without changing storage; it never
silently expands a recipient-root request into a hold on the entire case.
Handler carrier routes retain the case reference but omit personal employee/unit
bindings, and ordinary employee delivery inventory/erasure excludes typed notices.
The notification owner alone composes their delivery, route and receipt erasure.
Subject exports contain only state, purpose and case reference; recipient phone,
employee identity and host-user identity are never exported to the complainant.
Governed local erasure composes each storage owner's transaction port and retains
opaque replay tombstones. Possible external transport copies remain awaiting
expiry. Restore must keep messaging disabled until independent governance journals
and replay tombstones have been reconciled. This module does not itself certify
an external backup or provider's deletion.

Run the real database tests with `go test -race -tags integration ./notifications
./cases ./delivery ./carrier` in the disposable compose test runner. Unit tests
alone do not prove durable projection or delivery. Host deployment, recipient
consent, account policy, actual carrier acceptance/delivery and human response
remain separately verified operational boundaries.
