# Protected carrier composition

Development source; disabled by default. Carrier deployment and permitted live
recipient acceptance remain separate release gates. Host protected-mode startup
refusal must remain until all required host surfaces pass acceptance.

The host resolves an exact active organization/account/phone/business tuple and
verifies the complete raw webhook HMAC before calling `carrier.AcceptBatch`.
All entries in a protected batch must belong to the protected composition.
A wholly ordinary batch retains legacy processing only after every account is
resolved uniquely and its business identity and current secret authenticate the
complete raw body. Protected account UUIDs cannot escape through a changed phone
identifier. Mixed, unsigned, unknown or ineligible traffic is denied before generic
processing. `AcceptBatch` is a
trusted server port, not an HTTP handler or user asserted receipt verifier.

Signed text is canonicalized to an international number without country guessing,
checked against the fresh trusted roster, and committed atomically into extension
owned ingress. The worker serializes tenant ingress and invokes `Receive`; the
conversation owner retains provider ID replay protection. Changed payload replays
fail closed. The original employee/unit ownership is immutable: phone reassignment
cannot transfer queued text, and the conversation owner rechecks the binding at
processing time. Privacy erasure continues to belong to that original employee. Queue processing errors are held for investigation, without logging
text, recipient numbers or error bodies. Queue commit uncertainty cannot cause an
additional logical conversation turn. Existing held synthetic intents remain held.

Replies use the delivery owner's one-shot `SendObserved` worker. Case
acknowledgements use claim, current authorization, route binding, one carrier
submission and finish. An accepted provider response is not delivered. A crash or
unknown response holds the original intent and cannot trigger a retry or fallback.
The root must allow only one configured carrier account per organization until
case acknowledgement source-account routing has its own durable owner contract.

Free-form replies require a genuine signed inbound provider timestamp within the
last 24 hours. Processing time cannot reopen that window, and future timestamps
are rejected. The host rechecks active account credentials and configured
recipient restrictions immediately before its bounded HTTPS transport call.

Authenticated terminal callbacks persist first and reconcile using durable
account/intent/provider correlation. Duplicate observations can arrive in a
changed batch without becoming conflicts. Changed observations, unknown provider
IDs and conflicting outcomes never manufacture delivery. Early unmatched events
retain their proof and use durable attempt ordering and backoff, so they cannot
starve later matching observations. SMS fallback is disabled unless the host supplies an exact approved SMS route.
Only known WhatsApp failure may enqueue the stable `:sms-fallback` intent, with
exactly the minimal notice and current employee/case authorization. SMS unknowns
and failures never retry or produce another fallback. SMS does not inherit the
WhatsApp reply window. The host owns current account credentials and recipient
allowlisting immediately before the optional provider submission. Opaque provider
client UUIDs are bound durably to the original SMS intent before submission;
account-scoped resolution accepts the provider's normalized wire UUID without
exposing case references.

SMS receipt polling accepts only authenticated metadata for the bound route.
The receipt page and provider cursor commit together, so partial invalid pages
and failed commits cannot skip evidence. Polling writes no generic transcript or
inbound SMS body. Failed case acknowledgements are scanned through the cases
owner's explicit known-failure port; a durable round-robin scan cursor and stable
fallback IDs prevent missed work and duplicate sends.

Register `carrier.PrivacyAdapter{}` with physical governance and migrate its
storage before discovery. It requires the existing scoped conversation privacy
grant. Ingress copies and correlated receipts are exportable and erased by their
storage owner; replay tombstones prevent erased old webhook text from rebuilding
records. Delivery-owner tombstones also prevent a wrapped failed-case scan from
recreating an erased fallback. Case-linked carrier copies require current ordinary
case access as well as the exact scoped privacy grant; queued API-case fallbacks
remain discoverable even without conversation turns. Existing conversation/delivery owners retain external transport expiry
lineage. Unmatched receipt metadata contains opaque provider/intent identifiers,
with no recipient number or transcript body. External carrier copies and backups
require their independently evidenced retention process.

Targeted checks use `CARRIER_DATABASE_URL`; each test creates and removes its own
PostgreSQL schema. Host carrier tests likewise isolate their schema and never
invoke shared test database cleanup. No carrier network submission is part of
these tests.

The host supervises ingress, dispatch, durable reconciliation, receipt polling and
escalation with independent bounded contexts. Slow inference or an SMS polling
outage must not delay an already durable WhatsApp receipt or another worker lane.
