# Optional host routing policy

`HTTPConfig.RoutingPolicyTx(ctx, tx, actor, operation, caseID)` runs after the existing current actor fence and before source writes. Return a trusted `RoutingBinding` to persist the applied policy on new events, or an error to deny the mutation. A nil hook preserves generic legacy behavior. Request JSON never supplies this binding. Intake replay does not rewrite historical receipts.

The standalone host uses the hook to restrict recipient-changing assignments/escalations to exact dispatch grants when its current tenant policy requires that. Existing case authorization and conflict checks still apply. A trusted worker may pass `SQLRepository.RoutingBinding` only while retaining its own authorization and policy locks through the nested source commit.

`GET /dispatch` is metadata only: exact assignment grant, same organization/unit, and no complainant/subject alias conflict. It does not expand `CanRead`. `ReadRouting` is a trusted server conflict-checking port; serialize `DispatchView`, never its internal case value. Assignment responses for dispatchers use this narrow DTO. Candidate resolution and current candidate transaction validation remain mandatory host responsibilities.
