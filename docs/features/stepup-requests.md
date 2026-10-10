# Step-up requests

An agent with `approvals.request` and target read access can request one
protected change. A person holding the target permission approves with an
existing passkey or a fresh OIDC sign-in. Requests expire after 15 minutes.
Decline needs no additional authentication; only the requesting agent can
withdraw. The first outcome is final.

The server adds a native `stepup` source to the Decision Desk projection.
Pending requests hold work and sort with held approvals by expiry. The
request API supplies before/after snapshots and method-bearing decided history.
Desk visibility checks only registered target permissions, retaining workspace
and project coverage within the existing 1000-project limit.

Opted-in phone notifications reuse the existing push channel. A step-up goes
to every currently authorized holder with an active registered passkey and
subscription, subject to quiet hours and delivery bounds; it does not wait for
the ordinary approval escalation interval. Push contains only the authenticated
review URL. The phone card shows the exact before/after snapshot and opens the
corresponding Decision Desk item. Approve reuses the native digest/revision-bound
passkey or fresh sign-in path; Decline needs no extra authentication. Its scrolling
body keeps the action footer in place, including safe-area spacing.

Each terminal outcome atomically records one result message for the requesting
agent: applied with the verified method/person, declined, expired, withdrawn,
changed meanwhile, or failed to apply. A session-bound request records the same
line in that exact session's project chat, including workspace-level requests;
no arbitrary successor is selected. Requests without project/session context
leave the result in the principal inbox. A result persistence failure rolls back
the target change and outcome together. Audit and message events carry the result
line without assertions or tokens. Platform passkeys are called device passkeys;
the authenticator does not reliably distinguish Face ID from Touch ID.

## On the Decision Desk (AEON-1048)

A step-up request is the desk kind "Step-up approval", in the same memo,
queue, keys and Decided view as approvals and key trims. The memo adds two
things only: a Before → After block in the answer column (for a feature
override: feature, scope and setting, with the changed setting tinted), and an
Approve in the Decide slot (Enter) that names its method, "Passkey" when the
person has a registered passkey, otherwise "Sign in again". The server makes
the actual choice. Decline sits in the Decline slot and needs no step-up. The
stamp is always Once.

Approve sends the request digest and revision to `.../options`. With a
passkey the browser prompt runs and the assertion goes to `.../approve`; a
cancelled prompt changes nothing. Without one the page leaves for the
`authorize_url` (https only, or http on an http origin) and the OIDC callback
returns to `/decision-desk?needs=s:<id>`, which opens that decided memo. A
response settled by someone else, by expiry or by withdrawal is shown as
that outcome ("Decided by Anna first: …"), never as this person's decision.
When a decision call answers 409 because the request already ended, the desk
reads the request again and shows that settled outcome and decider instead of
keeping the pending memo. Closing the memo, or a change of signed-in person,
aborts the flow: a late answer starts no sign-in, passkey prompt or approval.

The Decided answer names who and how: "Approved · Markus · device passkey",
"Approved · Markus · fresh sign-in at 11:21", "Declined · Markus",
"Withdrawn by the agent", "Expired without a decision", "Not applied ·
changed meanwhile". The desk reads at most 100 pending and 100 decided
requests and says so when more exist. The existing approval memo is
pixel-identical before and after this change (Playwright capture, 390 light,
1440 light and dark).

The first typed mutation is a shipped feature override, requiring
`settings.manage`. Its payload is `{ "kind": "feature", "key":
"workspace-summary", "project_id": null, "enabled": true,
"expected_revision": 0 }`. The before snapshot is `{ "key":
"workspace-summary", "project_id": null, "override": null, "revision": 0 }`
for an untouched workspace override. `before_hash` is SHA-256 of canonical
JSON (sorted object keys, compact separators). Explicit `enabled: null` means
inherit. Additional protected operations need code-owned typed adapters that
retain the existing operation's validation and transaction locks; arbitrary
routes, SQL, secrets, key changes and scope grants are unsupported.

`POST /api/stepup-requests` creates a request; list/get return only owned or
currently authorized requests. Decisions submit `request_digest` and
`revision`. `POST .../{id}/options` uses an existing passkey when available,
otherwise returns an OIDC `authorize_url` using the existing client/callback,
`prompt=login`, `max_age=0`, nonce, PKCE and signed state. The callback requires
the same browser person and OIDC subject, with verified `auth_time` after the
Approve action. Both methods bind the proof to person, request and decision,
with one use and a two-minute expiry. Provider tokens and assertions never
enter request records or audit events.

Approval acquires tenant, tree, request and target fences, rechecks the person's
current target permission, then verifies the proof and before/after hashes.
The target mutation and `applied` outcome commit together as that person.
Changed targets become `stale`; rolled-back apply failures become `failed`.
No waiting/approving intermediate state is persisted, avoiding orphaned work
after interruption. Terminal outcomes are `applied`, `declined`, `expired`,
`stale`, `withdrawn` and `failed`. Audit records contain the requester, decider,
verified method/authentication time, snapshots, digest, expiry and outcome.
Expiry is materialized on authorized get/decision or decided-history reads;
the pending desk immediately excludes expired requests.

List endpoints use bounded keysets (at most 100 requests and 1000 projects).
Migration 1307 creates the RLS-protected native table and widens only the two
1092 phone-kind checks to admit `stepup`, preserving all older writer values.
The exact-byte classifier exception covers those CHECK replacements and
requires the coordinator's normal review and release gates.

Validation on the assigned worker branch covered the full affected Go suites
(step-up, authentication, phone approvals, Decision Desk, authorization,
agent pairing, DSAR and reporter contract). Final targeted regressions also
covered minimal custom roles and request ownership across project sessions.
Migration tests proved transactional rollback, forced RLS and acceptance of
both legacy and new phone kinds. `ci-static.mjs --merge-main` passed all 41
checks with no skips on the final code commit.

The checksum-verified published native server from `v261009095632.0.0` passed
the standard migration compatibility HTTP probe before and after candidate
migrations against an isolated PostgreSQL 18 fixture. Docker was unavailable
on the test host, so the image-based compatibility gate remains for
coordinator CI. Cross-family review, merge and release remain coordinator
responsibilities; the worker did not push or deploy.

Fix round 2 adds a regression at 1000 projects covering owners, minimal
workspace target-permission holders and denied project-bound readers. It
fails against the original `debe5427f` production code: the owner's desk query
carried 162 permission entries and 6,282,560 bytes. Registered target coverage
now carries only `settings.manage` and stays below 64 KiB for that fixture.
The affected remote Go suites, reporter contract and migration rollback/legacy
kind regression passed on `3c1ab48f2`; the locked static check passed all 41
checks with no skips. The remote runner used the host's installed Go 1.26.3
after the Nix shell hit a missing derivation, and generated the OpenAPI output
before contract tests. The non-blocking review findings remain follow-up work.

AEON-1049 validation covered the remote phone-approval, native step-up, inbox,
authorization, authentication and Decision Desk suites. The result regression
checks exact session/project binding, all terminal lines, first-decision
idempotence and rollback when recording the result fails. A first-use System
sender fixture checks PostgreSQL's actual locks before the inbox insert, so
SQL functions cannot take the event counter early. The reporter contract passed
after regenerating the ignored OpenAPI output.

The phone component regressions cover passkey cancellation, digest/revision
binding, all recorded outcomes, and cancellation when leaving the record.
The control stability guard passed at 390, 1024 and 1440 pixels in light and
dark themes with long German context. Screenshots are local Playwright artifacts
under `web/test-results/phone-approvals-step-up-ph-1449f-footer-still-in-both-themes/aeon-1049-stepupphone/`
(`card-390-light.png`, `card-390-dark.png`, and corresponding 1024/1440 files).
Ownership, test-tier/shard checks, typecheck, lint and push pointer validation
passed. The locked static gate passed all 41 checks without skips. No origin
push, deployment or worker-run model review was performed; the coordinator
retains those gates and the separate desk UI package's integration.

The command-package suite also passed after OpenAPI generation in its temporary
remote checkout. The final static run on `34d537a7e` passed all 41 checks with
no skips (exit 0); the final handover commit adds only this validation record.

AEON-1049 fix round 2 settles a bounded expiry page in two phases inside its
existing transaction. Every request update, exact session lookup, result message
and receipt write finishes before the first event takes the event counter lock.
The System sender is resolved once for the batch; result events and request
audits follow the row writes. A failed later result rolls back the entire page.

The registered two-agent/session regression failed remotely on unchanged
`d1ec5f8d` production code, with the corrected fixture at `7ca1497c6`, because
PostgreSQL's lock guard detected the event counter before the second result row.
It passes with the batch fix, and also checks exact agent/session/project
destinations, first-use System creation, expiry timestamps, repeat-list
idempotence and rollback when the second message insert is rejected.

The affected remote step-up, inbox, authentication, Decision Desk, phone
approval, command and reporter-contract suites passed on `354bef5a9`. The
command and contract rerun first regenerated OpenAPI in this task's temporary
remote checkout. Both push pointer tests and the three targeted phone spec
cases passed; the latter used one local worker and covered three widths in both
themes. The remote browser launcher refused twice pending OPS-247, so
CI-equivalent Linux Chromium remains for coordinator CI.

The final locked remote static gate passed all 41 checks with no skips (exit 0)
after restoring its missing Apple SDK dependency. The tier manifest change is
one additive regression entry. Non-blocking review findings remain follow-up
work; no origin push, deployment or worker-run model review was performed.

AEON-1049 fix round 3 cancels the phone review's outstanding proof when route
leave starts, before navigation waits for session refresh or the next page's
bundle. A late options response cannot start a passkey ceremony, and a late
passkey cannot submit approval after Back has started navigation.

The existing cancellation regression now holds navigation open with a session
read barrier and checks both proof stages, including a passkey stub that returns
despite cancellation. It fails against unchanged `e1264d5b` production code
because the options signal is still live during navigation. With `cf06b2d7b`,
both stages pass and record no approval. The three targeted phone cases passed
locally with one worker, and 24 targeted push/identity-scope unit tests passed
both locally and remotely. Existing control stability
and screenshots cover 390, 1024 and 1440 pixels in both themes; artifact names
remain as documented above. The locked remote static gate passed all 41 checks
without skips. The remote browser launcher still refuses pending OPS-247, so
CI-equivalent Linux Chromium validation remains for coordinator CI.

The affected remote phone-approval, native step-up, inbox, authentication,
Decision Desk, command and reporter-contract suites passed on `cf06b2d7b`.
The command and contract rerun generated OpenAPI in this task's temporary test
checkout first. The existing test name and registrations are retained; no
assertion was weakened. Non-blocking follow-ups remain untouched. Only local
branch commits were made; no origin push, deployment or worker review gate ran.
