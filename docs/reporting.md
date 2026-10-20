# Reporting integration

The module owns `addon_report_events` and `addon_report_activity` only. It consumes
case-owner `ReportFacts` / `AggregateFacts` and an optional owner-supplied immutable
activity port. It never joins the live roster or reads another module's private
SQL tables. Current report grants and current case/subject policy govern every
refresh and export. An old projection is not an access grant.

## Host ports

Call `reports.Store{DB: db}.Migrate(ctx)` before registering:

```go
reports.NewHTTPHandler(db, reports.HTTPConfig{
    Enabled: true,
    MinGroup: explicitlyApprovedThreshold,
    RefreshActor: refreshCurrentVerifiedSession,
    Activities: currentAuthorizedOwnerActivity, // optional, never guessed
})
```

`RefreshActor(context.Context, api.Actor) (api.Actor,error)` is mandatory. It must
recheck the original live session, fresh MFA, active identity and exact current
permission/scope grants. It runs before calculation and again before delivery.
Changes in actor grants or the current authorized case/activity source cause a
denied/conflict response without report content. Source callbacks must be
server-owned and must never be constructed from request bodies.

`Activities(context.Context,api.Actor,board bool)([]reports.ActivityFact,error)`
should adapt `conversation.ActivityFactsFor(ctx,db,actor,board)` field-for-field.
The explicit mode matters for an actor holding both operational and aggregate
permissions. No operator, sender, phone, narrative or content belongs in an
activity fact. CaseKey is opaque; linked-case and subject-conflict checks remain
with the conversation/case owners. Pre-case events need an exact current unit
grant. Inbound received, outbound queued, enquiries and handoffs are distinct;
queued is never represented as delivered. A nil port is explicitly unavailable.

## HTTP

Authenticated MFA routes:

- `GET /api/extensions/reports/operational?start=YYYY-MM-DD&end=YYYY-MM-DD`
- `GET /api/extensions/reports/board?start=YYYY-MM-DD&end=YYYY-MM-DD`
- `GET /api/extensions/reports/export?mode=operational|board&start=...&end=...`

Operational reports allow optional exact `zone`, `department`, `case_type` and
RFC3339 `as_of`. Windows are UTC and half-open for creation/activity counts;
maximum width is 366 days. A current-period snapshot is marked partial. Drafts
are excluded until first submission. Reopening clears resolution at the selected
snapshot. Creation dimensions remain immutable even if the employee moves or an
operator corrects the current classification. Both occurred and recorded times
must be at/before the snapshot, preserving what was known then.

Leadership reports require a **single completed UTC calendar month**. Custom
cohort filters and arbitrary snapshots are denied to reduce subtraction attacks.
This deliberate release restriction means current-month leadership reporting is
not available. Small nonzero cells or scalar metrics suppress **all related case
and service totals and margins**; zero is distinct from suppressed. The host
must explicitly set a threshold; 5 in a synthetic profile is a demonstration
choice, not an approved customer policy. A full disclosure review for releases,
repeated audience-specific exports and retention remains a deployment gate.

Exports require `reports:export` plus the report permission, each with explicit
matching unit grants. They contain aggregate counts only, no raw event/case IDs,
people or narratives. CSV formula-leading labels are escaped. Responses and
exports use no-store. Report source owner ports are re-read before delivery.
The frontend consumes the actual API, clears prior reports on filter/error or
unmount, and uses the same controlled export endpoint.

## Durability and checks

Projection keys are organisation/mode/case/version or organisation/mode/event ID.
Identical replay is idempotent; changed payload under a stable identity is rejected
atomically. Current source allowlists gate historical reconstruction, so revoked
scope does not read older projected facts. Projections require complete contiguous
case events from version 1 and reject changed creation snapshots. Projection data
must be included in privacy/restore inventory and migration order after cases.

Run in disposable PostgreSQL using `API_DATABASE_URL`:

```sh
go test -count=1 -race -tags integration ./reports
go vet -tags integration ./reports
```

The tests cover history/reopening, drafts, replay, changed history, scope removal,
MFA/refresher requirements, disclosure suppression, CSV escaping, export grants,
activity de-duplication and access changes during response. Frontend tests cover
actual route/parameter/controlled download behavior; host typechecking covers the
compiled page. These are component/adapter checks, not a recorded production or
carrier demonstration. Actual integrated browser acceptance remains separate.

## Operational monthly trends

`GET /api/extensions/reports/trend?end_month=2026-10&months=6&dimension=zone`
returns a real, chronological period series. `months` is required and bounded
1–12. `end_month` is an inclusive UTC month no later than the current month;
`dimension` is exactly `zone`, `department` or `case_type`. Optional exact
`zone`, `department` and `case_type` filters define the same cohort in every
period. Empty months remain present with zero new cases.

Every period reconstructs the case-owner's frozen versioned events as known at
that month's end (or now for a partial current month). New submitted cases,
resolved cases from that creation cohort, historical backlog and creation-time
dimension group counts reconcile to the single-period report. Drafts, future
knowledge and later roster/category changes do not rewrite earlier periods.
Facts are persisted/validated through the existing projection store. No mutable
roster lookup or private case-table query is added.

Trends require exact current `reports:operational` unit grants and the existing
current case/subject access policy. Leadership-only actors cannot query them.
The session/grants and current source are rechecked after building the series;
changes return 403/409 without the result. Export uses
`GET /api/extensions/reports/export?mode=trend&end_month=...&months=...&dimension=...`
and additionally requires matching `reports:export` unit grants. CSV labels use
the same formula escaping as ordinary reports.

The real frontend API supplies a default six-month series, selectable 3/6/12
months, dimension selector, accessible SVG bar chart and exact monthly/cohort
table. It clears old series on filter changes, failed access refresh and unmount.
The existing leadership route remains a suppressed monthly summary, with no
operational trend request. Case trends do not infer message/service counts.
Tests cover reconciliation, historical dimensions, limits, scoped filtering,
controlled exports, mid-request source/grant changes and rendered table/chart
cleanup. Integrated runtime capture is a separate orchestrator acceptance step.
