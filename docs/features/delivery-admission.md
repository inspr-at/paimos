# Delivery admission model

AEON-1392 adds PAIMOS-owned admission facts and a pure decision in delivery.
This is groundwork for AEON-1393: it does not enqueue, validate merge groups,
publish statuses or change GitHub protection. No UI is added.

`GET /api/delivery/{itemId}/admission` returns the current binding revision,
review and visual approval provenance, visible hold reasons and decision.
Admission requires an open PR targeting `main`, green configured CI, an
independent authenticated review and visual approval or proven exemption,
all for exactly the current repository, PR, target, head, merge-base range,
ticket and binding revision, without a hold. `aeon/admission` is excluded
from its own CI prerequisites; an empty prerequisite list fails closed.

Only P1's authenticated `SubmitRunTx` records approvals. The approval records
reference immutable review runs and expose their actual model, harness,
reasoning effort and author/reviewer families. Same-family review is denied
even with generic review settings off. Visual runs retain P1's separate
`reviews.visual_approve` check inside the final submission transaction.
Pre-expansion runs without an admission revision never satisfy admission.

Delivery observations advance the binding revision after any head, target,
scope, ticket or project change, including A → B → A. Old approvals and pending
runs remain invalid. Ordinary CI updates and hold changes preserve approvals.
Existing delivery holds also block admission.

People with `delivery.manage` can PUT the displayed control revision and exact
binding to the item endpoint. This replaces its release-cut, wave and manual
holds and explicitly designates a fix for the nightly exception. Holds survive
new heads; a fix designation resets on a binding change. Project-wide holds
and nightly policy use GET/PUT
`/api/projects/{projectId}/delivery-admission`. All mutations recheck live
permissions under tenant/tree fences and append audit events after writes.
Agents cannot write these policy controls or record approvals through them.

The nightly policy defaults enabled with `unknown` evidence. P2 lets the policy
owner explicitly record `green`, `red` or `unknown`, or disable the policy.
Automatic nightly observation belongs to the next integration. A red nightly
admits only an explicitly designated fix; unknown evidence blocks all PRs
while enabled. A fix never bypasses CI, reviews, visual review or explicit holds.

Visual review requires both reviewer `visual_impact=no` with a reason and a
complete proven diff to exempt a change. The classifier allows docs, tests,
CI, named test harness tools, and an explicit list of database pool/timeout/
metrics source files. It does not exempt arbitrary Go/backend changes, web
source/assets or OpenAPI changes. Both sides of renames are classified. The
read-only GitHub compare supplies the actual merge-base; reaching its 300-file
boundary marks the diff incomplete and requires visual approval. Missing
merge-base evidence refuses admission even when visual approval exists.
Browser-test selection and visual-review scope remain separate decisions.

Rule → test mapping:

| Rule / risk | Test |
| --- | --- |
| Each prerequisite; exact repository/PR/head/target/scope/ticket/revision; independent review; nightly policy and fix exception; hold precedence; visual verdict | `TestAdmissionRules` |
| Proven visual classes; unknown/backend/web/API changes; invalid or incomplete diff; reviewer yes/uncertain | `TestVisualAdmissionClassifier` |
| Actual merge-base; renamed presentation file; truncated file list; failed read | `TestAdmissionDiffReadsMergeBaseAndRenameSides` |
| Admission cannot depend on or bootstrap itself | `TestAdmissionCIExcludesOwnContext` |
| Real P1 run submission → stored approvals → admission; route/family provenance; visual grant; replacement with changes; all hold types on/off; nightly/fix precedence; new head, retarget and scope A → B → A; stale controls; permission revocation; projection replay; tenants and agent policy refusal | `TestAdmissionStoreAuthenticatedApprovalsAndInvalidation` |
| Capability authentication, coordinator/worker forgery, same-family review with policy off, visual-grant revocation | Existing `internal/auth:TestAuthenticatedReviewerRuns` |
