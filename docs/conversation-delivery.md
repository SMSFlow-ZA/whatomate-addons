# Conversation and delivery

`conversation.Service.Receive(ctx, Inbound)` is a trusted port for a host adapter
that has already verified the provider webhook signature and resolved tenant and
account. `Inbound` contains tenant, account, canonical sender, provider message
ID and text. There is deliberately no public phone impersonation webhook.

`conversation.NewHTTPHandler(db, HTTPConfig)` exposes POST
`/api/extensions/conversation/simulate` and GET `.../logs?sender=...` only when
`EnabledSyntheticDevelopment` is true and the current trusted MFA operator actor
has `conversation:simulate`. The rehearsal uses the fixed `synthetic` account.
Its body accepts `sender`, `text`, `provider_message_id`; identity, department,
zone, grants and roles are rejected. Constructors do not migrate: composition
must call `conversation.Migrate(ctx,db)` and cases, directory and AccessStore
migrations explicitly.

Each turn, replay and delivery rechecks current roster freshness, active status,
revocation and phone registration. Employee identity, unit (roster zone) and
classification come from that roster. `HTTPConfig.Aliases` must call the trusted
`api.AccessStore.EmployeeAliases`; employee principals have only exact current
unit `cases:read.own` and `cases:submit.own`. Linked operator identity is included
only as a denial/conflict identity, never as an operator grant. The cases owner
performs current subject and complainant authorization for status and delivery.

Send LOG CASE, a narrative of at least 12 characters, then CONFIRM or CANCEL.
Confirmation calls the actual transactional `cases.Service.Intake`; no local or
fake case reference is generated. The request key and immutable classification
are saved with the confirmation dialogue. Current unit changes deny confirmation.
A process crash after case commit is recovered by replaying the unchanged inbound;
the case owner's intake idempotency returns the original committed reference.
Unknown commits do not claim submission success. STATUS requires an explicit
reference if the dialogue contains several cases. Employee status contains the
reference and state only. HUMAN/HANDOFF explains the confirmed intake path.

SOP answers use `HTTPConfig.Advisor(ctx, employeeActor, question)`; the host must
connect its governed AI/knowledge boundary with current employee audience access.
The employee actor carries no operator MFA, session or operator grants. Only a
supported answer with citations is returned; absent/failed/missing-source answers
instruct the employee to request human help. No model may create case IDs, grants
or assignment candidates.

Inbound keys are unique by tenant/account/provider ID. Advisory locks serialize
provider keys and each sender dialogue across processes; changed replay payloads
conflict. The inbound response, dialogue and conversation reply enqueue commit
in one PostgreSQL transaction. Narrative is stored only in private turn state;
HTTP uses an explicit response projection. Tenant/account/sender and roster
employee identity bind the dialogue. Case acknowledgement delivery belongs solely
to the cases owner's atomic intake outbox: confirmation does not also enqueue a
conversation acknowledgement. The HTTP rehearsal shows the confirmed reference.

`delivery.Worker{DB,Tenant,Clock,LeaseDuration,Authorize,Send}` owns conversation
reply leases, with `Authorize=conversation.Service.AuthorizeDelivery`. `Send`
receives the stable message ID for provider idempotency. Current employee and
case authorization are checked immediately before dispatch. Missing callbacks
hold delivery. Verified failure, success and unknown outcomes are distinct;
expired in-flight leases become `held_unknown` and are never blindly resent.
No fallback transport or chargeable provider is configured by these packages.
Any future SMS fallback needs its own explicit provider permission/configuration,
minimal notification content and verified failure policy; unknown outcomes cannot
trigger fallback. Root composition owns case-outbox delivery via a cases-owned
lease port; this package never accesses case tables.

Confirmed intake creates a durable timer independent of dialogue state.
`Service.EscalateDue` loads a currently authorized case via its owner and calls
`ResolveCandidates` for fresh trusted triage principal and candidate grants.
`cases.Service.Escalate` enforces conflicts, subject aliases and optimistic
version. A successful due timer records `handed_off`; it is an audited routing
handoff with an atomic `Escalated` lifecycle transition. The timer is marked
`held_unknown` before crossing the case transaction boundary, so crash/unknown
outcomes cannot repeat handoff. Conflicts can retry; known failures hold for
operator intervention. A host worker and audited unknown-outcome reconciliation
remain required; no scheduler is installed by the module constructor.

The compiled host `ConversationPage.vue` uses these APIs for synthetic employee
chat, actual references and current authorized durable delivery logs. It preserves
unchanged inbound identity across uncertain requests. Production provider
signature adapter, approved audience mapping, real sender, case-outbox worker,
worker scheduling and unknown-outcome reconciliation require host composition.
No carrier calls, deployment or provider costs are part of local verification.

Verification: real PostgreSQL tests use `CONVERSATION_DATABASE_URL` and
`go test -race -tags integration ./conversation ./delivery`, covering duplicate
confirmation, crash recovery after case commit, mismatched replay, revocation
after enqueue, lease crash holding and current delivery authorization. HTTP tests
prove MFA/development permission gates; the existing cases suite owns subject
alias routing and transactional case/outbox guarantees.

`Service.CaseAcknowledgement(ctx, cases.OutboxMessage, configuredAccount)` is the
case-owned outbox adapter: root claims with
`SQLRepository.ClaimOutbox(ctx,tenant,worker,now,leaseUntil)`, resolves this notice
from the current roster and case policy immediately before sending, then calls
`FinishOutbox`. The notice's provider idempotency key is the case outbox ID.
Missing approved sender holds that claim; unknown sending remains held_unknown.
It does not copy the case acknowledgement into a competing delivery table.

`conversation.ActivityFacts(ctx,db,actor)` exports immutable content-free usage:
ID, organization, creation unit/zone/department/type, opaque optional case key,
kind (inbound, enquiry, outbound, handoff), outcome and occurrence time. Replay
cannot add events. Outbound `queued` is explicitly not carrier delivery. Current
exact reports scopes are required, with linked-case conflicts/authorization
rechecked through cases reporting ports. Pre-case enquiries require their own
creation-unit reporting scope. No phone, employee ID, narrative or text is
included. `go test -tags integration` verifies duplicate counting, scope removal
and current subject exclusions.

Timers require `HTTPConfig.LoadEscalationCase(ctx,tenant,unit,caseID)` to read via
an independently authorized, current worker principal through the cases owner.
Revoking employee chat eligibility does not suspend a submitted case's timer.
`ActivityFactsFor(ctx,db,actor,aggregate)` selects an explicit reporting mode;
callers with both permissions must use that method to avoid mixing board scopes.


Host composition now supplies the synthetic audience/roster/model adapter and an explicit current authorized worker resolver. A synthetic-only five-second scheduler processes timers and holds durable conversation/case acknowledgement intents because no approved carrier sender is installed. It creates no generic transcript copies. Production activation and live carrier verification remain separate gates.

Optional `delivery.Worker.SMSFallback` queues exactly one fixed minimal SMS notice only after a verified primary failure. Its configured SMS account is explicit; dispatch rechecks eligibility/case access. Unknown, expired and failed SMS outcomes never trigger blind replay or another fallback. Local database tests exercise this path; no live SMS route is claimed.



Verified receipt reconciliation is now an explicit owner port. The shared
`api.ReceiptVerifier` is mandatory and authenticates tenant, approved account,
intent ID, provider ID, stable receipt ID, evidence reference, observation time
and terminal outcome before persistence. No receipt HTTP endpoint is installed.
`SQLRepository.BindOutboxRoute` persists the account before any case outbox send;
accepted outcomes require that binding. `FinishOutbox` records acceptance or
transport failure/held states; delivered status requires `ReconcileOutbox` with
verified evidence. `OutboxDeliveryStatus` is a trusted owner read port.

Conversation transport adapters may use `Worker.SendObserved` to return accepted
plus a provider ID. The legacy `VerifiedSent`/`sent` label means transport
acceptance, never proven carrier delivery. `delivery.Reconciler.Reconcile`
settles sending, accepted, legacy sent or held_unknown without resending.
Terminal delivery/failure cannot be changed by a conflicting receipt. Repeated
identical receipts are idempotent; an altered stable receipt ID is rejected.
Unknown-to-verified-failed reconciliation may create one explicitly configured
minimal SMS fallback after current eligibility authorization; SMS dispatch still
reauthorizes eligibility. Unknown outcomes alone never create fallback.

Both owners commit status and a durable receipt audit together. Journals retain
only tenant/account, receipt/intent/provider IDs, canonical evidence fingerprint,
outcome and observed/recorded timestamps; evidence bodies and signatures are not
stored. These minimal control journals require an explicit retention policy.
Tests use a clearly labelled local HMAC synthetic verifier solely to exercise the
transport boundary. No live carrier verifier, callback route or real-cost sender
is configured or claimed by these tests.
