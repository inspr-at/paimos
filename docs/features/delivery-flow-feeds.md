<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Delivery Flow from checks and harness deliveries

Delivery › Flow has three feeds: GitHub App observations, PAIMOS deliveries,
and OPS rollout records. An empty project names all three missing feeds in
English and German. This changes wording within the existing layout only;
no new design or controls are introduced (needs Opus design for layout work).

GitHub App check suites now fill linked PR change items when that PR head has
no recorded CI workflow run. Their single observed span is labelled
**Checks (derived from check suite)**, uses source `github_app` and a stable
`check_suite/<suite-id>` key. It never claims workflow attempts, a runner wait,
queue wait, or a flaky rerun. Actual workflow runs on the same head take
precedence when selecting incoming observations. No GitHub App scopes change.

PAIMOS projects ticket-bound `ship` harness sessions into build spans,
authenticated review-run verdicts into review spans, executed shipping claims
into action observations, and observed merges into the existing change item.
A stopped session without the server's `finished` fact carries no successful
outcome. A proposal or shipping claim does not assert that GitHub merged it.
The projector joins each snapshot to the current tenant/project/ticket/PR
link; unlinked sessions and mismatched projects are ignored. It uses the
existing partial queue index and type/cursor index in separate bounded reads.
Replaying the same events emits no changes.

`POST /api/projects/{projectId}/delivery/flow/rollout` additionally accepts the
explicit `delivery.rollout.write` permission. It is project/workspace grantable
to an agent through a custom role and scoped key, and excluded from built-in
agent grants. It authorizes only the existing, strictly validated
`aeon.rollout.v1` report. Permission is checked again under the tenant fence in
the final transaction; changed rows are audited. `delivery.manage` and all
other delivery routes retain their existing authority. No shared key is edited.

Release identity remains `(tenant, project, "release", release label)`.
Version never participates. Repeated reports retain omitted step and incident
keys. This backfill never posts a release report or modifies a release row,
including OPS-379's releases 131–136 and the degraded incident on 135.

## OPS backfill plan for the 87 linked PRs

The ticket's read-only inventory reports 87 linked PRs. No production backfill
has been run by this change. OPS first generates the concrete current plan with
its existing database transport; database credentials are supplied by its
existing environment, never written into the plan or this repository:

```sh
go run ./scripts/delivery-flow-backfill --dry-run \
  --tenant TENANT_UUID --project PROJECT_UUID --actor PERSON_PRINCIPAL_UUID \
  --repository inspr-at/paimos --expect-linked-prs 87 > flow-plan.json
```

`AEON_FLOW_DATABASE_URL` is the operator's existing connection. Planning uses a
Postgres **READ ONLY** snapshot, rather than tentative writes and rollback.
The plan lists exact linked PR numbers, candidate change IDs and step counts,
and relevant source event IDs. `changed: null` distinguishes candidates from
applied changes. The actor needs current project `delivery.read`; the tool
requires an existing active person principal, and never changes roles or keys.

The tool refuses a changed PR count, over 100 PRs, more than 4,000 stored runs,
source events or candidate steps, and has a two-minute operation deadline.
OPS must inspect any refused or changed inventory; do not silently adjust 87.
There are no GitHub calls or metric/cursor resets. Existing release rows and
incidents remain outside its write set.

After LEAD approves the generated plan, OPS may run the same command with
`--apply` in place of `--dry-run`. This explicitly rechecks `delivery.manage`
inside the final tenant-locked transaction and projects only linked changes.
Keep the JSON result and run again to verify `changed: 0` and no new Flow
events. Compare release rows/steps/incidents before and after with OPS-379's
owned snapshot; do not repost releases 131–136. Production execution remains
an OPS action after LEAD's go, outside this worker's authorization.

The alternative `live-verification` GitHub environment was checked read-only
on 2026-10-17: the repository is accessible with administrative metadata rights,
but the environment endpoint returns 404. That alternative is not configured;
no workflow was dispatched and no environment or credentials were changed.

## Evidence

ESSENTIAL Go cases in `internal/delivery/flow_feeds_test.go` cover suite-only
ingress without Actions reads, head-specific workflow precedence, harness and
backfill replay, preservation of all release 131–136 rows and incident 135,
scoped rollout validation, and a read-only plan plus two applications for 87
fixture PRs. The existing Flow browser spec asserts the new English wording,
keeps mode controls stable, and captures the German empty state at
390/1024/1440 pixels in both themes.
