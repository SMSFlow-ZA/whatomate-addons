# Optional registry-bound category confirmation

`HTTPConfig.ValidateCategoryTx(ctx, tx, actor, label, CategoryBinding)` runs after
the current actor transaction fence and before case source locks. Hosts can
require an explicit registry audience and optimistic registry version, lock the
registry for the duration of confirmation, and reject removed labels. The
browser sends `category_binding: {audience, version}`; the callback must validate
both fields against trusted storage. No audience/unit mapping is inferred.

Case edit permissions, conflicts, expected case version and human reason remain
mandatory. Accepted registry metadata is saved on the current case and in the
immutable category correction event. Later registry edits do not rewrite those
events or the original intake classification. Employee DTOs remain redacted.

Nil callback preserves legacy integration behavior. The standalone host must
configure the callback; AI suggestions are never automatically applied. Choosing
a manual category need not invoke AI or consume inference quota.
