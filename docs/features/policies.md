# Policies

Settings → Policies (`/settings/policies`) says who may do what. Which model runs
is decided on Models, so the page names no model. It is one line and three cards,
for everyone who is signed in; nothing on it writes except the cross-family rule.
There are no tabs and no dialog (AEON-1165, approved design AEON-1143/1144).

**Cross-family review** (`#cross-family-review`) is the existing review-policy card:
mode, families, project scope and "What the pull request shows" are unchanged.
People without Manage review policy read the permission note instead of Save and
Discard. Below the rule, "How reviewers are chosen" turns the read-only fields of
`GET /api/settings/review-policy` into four sentences: the reviewer family order
(`family_order`), the review floor (`floor`: strong or frontier at the highest
native level, xhigh or max), the security exclusion (`security_excludes`), and a
link to Models › Reviews for the model in use today. These rules are workspace-wide;
they stay on screen while a project's rule loads and clear when the person changes.
Models' "Rule in Policies" opens `/settings/policies#cross-family-review`; the
card is ringed and takes focus, so the next Tab continues inside it.

**What agents must ask you for** lists every permission the registry marks as never
grantable to an agent key, in plain words, grouped as High, Medium and Low risk.
Each group is folded behind a one-line preview and grows below its head; the
controls above it never move. Every row says "Asks you"; the card lead says once
that the agent asks and the person approves on the Decision Desk, one action at a
time. A footer names the other person-only writes checked in code (release planning
and membership, locked doctrine rules). The list is static per release:
`web/tests/policies.test.ts` holds it equal to the `AgentGrantable: false` entries
of `internal/authz` (and their risk), so a new exclusion fails the build until it is
named here. The page no longer reads `GET /api/authz/permissions`.

**Rules that live elsewhere** has twelve rows: what, owner, Enforced or Advisory, and
one link. A link shows only when every permission its destination needs is held;
otherwise the owner is named without a link. Fix rounds and lane pause, and GitHub's
merge queue and code owners, have no screen here and never link.

The review ladders, their editor and the "About these rules" sheet are gone: role
orders moved into Models chains and holds into model pauses (AEON-1158). Browser
coverage, including the approved design's layout, target-size and edge-accent
probes at 1440 and 390 px in light and dark, lives in
`web/tests/policies-rules.spec.ts`; screenshot evidence is written to the test
output directory and is not committed.

Queue routes finish buffering request bodies before opening their transaction
or retaining tenant/tree/pairing locks. Input is capped at 1 MiB and shares a
30-second request deadline with the transaction; interrupted input returns 408,
and oversized input returns 413 before queue work starts.
