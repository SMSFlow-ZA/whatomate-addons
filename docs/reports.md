# Reporting domain

The package accepts event-time facts rather than joining current employee
attributes. The source adapter must reconstruct the requested snapshot (including
reopenings) and capture zone/department/type at creation. That durable projection
is still a host integration task.

`Operational` requires `reports:operational` and a current per-case policy.
Counts include only authorized cases; messages/enquiries without an authorized
case reference are excluded. Pre-case enquiry reporting needs a separately
protected source adapter. `Board` requires `reports:aggregate`, independently of
administration. There are no narratives, case IDs, people or drilldown links in
its result. Minimum disclosure size is explicit; a default is never inferred.

If any group or nonzero headline metric is too small, all marginal tables are
suppressed and all headline totals are omitted. This intentionally conservative complementary
suppression protects a single snapshot. Overlapping cohorts or repeated/time
exports still require disclosure review and a controlled release policy.

Tests use 16 synthetic cases: 12 current, four prior, three current resolved,
nine backlog, zones four each and departments six each. Partial periods are
flagged. Message/enquiry IDs deduplicate independently; neither is a case count.
No adoption percentage is invented without a complete eligible-population source.

Still required: durable projections, current case-policy adapter, export receipts,
scheduled delivery authorization, PDF/CSV rendering, time-zone approval, case SLA,
human AI-correction metrics and disclosure review across multiple releases.
