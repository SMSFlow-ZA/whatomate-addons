# Model-processing notice before employee assistance

`conversation.HTTPConfig.ProcessingDisclosure` is an optional trusted server-owned
`*ProcessingDisclosure{Revision, Text}`. Configure it for the reviewed processing
scope; never accept either value from a browser or inbound message. The host must
validate its processing policy and supply its canonical notice. This component
does not attest residency, approve international processing or establish consent.

On the first advisory question, the conversation queues the configured notice
and asks the employee to resend the question after delivery. It does not call
`Advisor`, so model inference and query embeddings behind that port cannot run.
The existing inbound/turn/delivery transaction atomically records the effective
revision/text hash and exact notice intent ID. Duplicate callbacks reuse that
intent; a failed transaction rolls it all back. Case submission, redacted status
and human routing stay available while the notice is pending or unavailable.

Later advisory questions require `delivery.ReadDeliveryStatus` to confirm that
**the current notice intent** reached `delivered`, with a provider identity and matching
tenant, account, sender and current employee. Queued, accepted, sent, unknown and
failed states do not qualify. A delivered minimal SMS fallback does not contain
the notice and cannot qualify. The gate never treats a waiting-message delivery
as notice delivery. It never auto-retries an unknown or failed notice to unlock
assistance.

The employee can explicitly send `SEND PROCESSING NOTICE` to request a fresh
notice. This uses the same validated trusted revision/text and formatter, but
binds the new intent to that genuine inbound message ID. Earlier delivery records,
including unknown outcomes, remain unchanged. The new exact notice must have its
own authenticated `delivered` receipt before a later question can reach the
advisor; earlier receipts cannot unlock it. Duplicate inbound callbacks reuse the
recorded response and intent. The command does not invoke AI, change configuration,
retry an earlier delivery or erase a draft narrative, pending confirmation or
selected case. It is handled before draft input; missing or malformed notice
configuration rejects the command without persisting a turn or delivery change.

`delivered` comes from the delivery owner's authenticated receipt/reconciliation
path. It is evidence of transport delivery, **not reading, understanding, consent
or legal permission**. Those are separate policy decisions. The additive internal
`DeliveryStatus.EmployeeID` and `Sender` bindings are excluded from JSON status
serialization and must not be exposed by employee-facing status APIs.

A changed notice revision **or text** requires a new notice and delivery proof.
A changed roster employee bound to the sender resets the turn's notice identity.
Current roster eligibility is still checked on every turn and at delivery time;
revoked employees cannot continue. Replays belonging to a prior employee are
refused. Notice state remains in the existing governed conversation JSON, so the
normal subject export/erasure/restore boundaries cover it without a new table.

Persisted fields `processing_disclosure_key` and
`processing_disclosure_intent_id` identify the effective notice and its currently
required intent. Their existence alone never releases advice. A receipt/status
storage failure leaves advice held. Processing notices are queued without a selected
case reference, because a policy notice must not depend on unrelated case access.

Synthetic PostgreSQL tests cover the first question, replay/restart, authentic
local HMAC receipt verification, non-delivery states, minimal fallback, identity
and revision changes, current revocation, exact owner binding, concurrent initial
questions, rollback/retry, explicit fresh requests after unknown delivery,
duplicate requests, forged receipts and preserved draft/case workflows. These are
local synthetic transport observations, not live employee delivery evidence.
