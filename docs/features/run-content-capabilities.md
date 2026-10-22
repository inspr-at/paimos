<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Run content capabilities

An ordinary paired computer's API credential is transport authority. Its run
poll, run list and run detail expose an explicit metadata projection: identifiers,
execution mode, model/account selection, lifecycle, revisions and accounting.
They do not select or return trace, recovery handover, display labels or
free-text outcome detail. Content readers and launch claims require a separate
server-issued credential.

Existing person-approved workstation designations retain their separate
live-key, signer and confirmation checks.

During agentd's Prepare stage, `POST /api/runs/{runId}/capability` negotiates
`X-Aeon-Run-Capability-Policy: run_content_v1` and supplies the daemon identity
and generation. The server checks the run's existing person, current project
access and computer-start grant, the paired computer and its enrolled account's
generation. Missing people are refused; issuer, pairing approver, preferences
and caller-selected headers never supply a person. Unbound historical runs
remain metadata-only through this transport.

The response contains a random 256-bit credential with a 24-hour maximum
lifetime. Only its digest and binding metadata are stored. Reissuing it replaces
the preceding credential for that run. The computer-wide generation epoch
changes on an accepted health probe with a different generation, invalidating
capabilities across sibling accounts. Replaying a previous generation string
does not restore old capabilities.

Content calls carry `X-Aeon-Run-Capability` alongside the transport credential.
Authentication resolves the credential's run/person rather than accepting a
run ID as proof. Each database transaction rechecks the binding and live access
under the tenant/tree/pairing fence before resource work. Run and work-order
targets, node lookups and harness sessions must match the bound run; a content
credential cannot select another person's run on the same computer.

Agentd acquires the credential and fetches the run context, work-order node and
criteria only during Prepare. Tools capture their run binding; session calls,
chat callbacks and notification streams retain that same proof, including with
injected HTTP transports. Credentials stay in daemon memory, outside journals,
prompts and harness child settings. Inherited child environments scrub daemon
credential variables and aliased Aeon credential values; provider account
selection keeps its existing policy.

Older daemons can still read metadata. Responses advertise
`Aeon-Run-Content-Policy: run_content_v1`; unsupported callers receive
`Aeon-Upgrade-Reason: run_capability_upgrade_required`, including an empty poll,
and corresponding per-run upgrade reasons. They cannot fetch bodies or claim
runs through the paired transport. A no-launch verification refusal remains a
metadata operation and does not require content authority. Pairing and API-key person migration remain
separate rollout concerns.

Paired attached inbox hooks have no run and retain an exact session path for
heartbeat, drain, delivery completion and content-free notification hints. The
server checks an unmanaged null-run session, its authenticated agent, exact
worker lease and S1-03 generation ownership, the connected pairing and the
key's existing live person/creator ceilings under the shared fence in every
transaction. A person-created registration that merely names the paired agent
does not qualify. The first heartbeat must attest the attached process identity;
delivery and notifications require that verified binding. Attached hooks drain
and receive message hints only for their exact session. These calls do not
require an agentd upgrade or grant access to run, node or session content reads.

The exact owned stop/yield/confirm-exit/control-completion safety routes retain
their private worker-lease checks without a content capability. Person session
controls (interrupt and stop) are not content or launch: a bare paired
credential meets the computer pairing ceiling on those routes, and an upgrade
reason does not replace that refusal. A content credential cannot select them. The server also
rechecks the exact lease and S1-03 ownership: an assigned run, a generation
started by the paired principal, or its current native session binding. A lease
on a person-created direct registration does not establish daemon ownership. Bare paired
transport on those routes always uses the metadata-only projection. Revoked
person access or an obsolete content capability cannot prevent the owning
daemon from requesting or confirming a stop; handovers, steer text and other
content remain unavailable on this safety path. Native session actor grants
retain their existing independent operation/generation broker checks.

Regression coverage includes server credential binding/reissue, wrong run,
person, computer and generation, historical unbound runs, metadata-only legacy
reads, Prepare request order, per-run tools, inherited environment scrubbing and
paired stop/yield after person revocation. Migration 1414 is an additive nullable
expansion; capability digests are classified as secrets in the DSAR inventory.

## Validation handoff

The focused capability, Prepare/tools/chat/SSE, child environment and paired
revocation safety regressions pass. The real HTTP supervisor verification test
also passes with the authoritative unsettled-accounting field returned during
Prepare. Twelve full affected packages passed, including authentication,
authorization, runs/accounts, harnesses, database/DSAR and reporter contracts.
The redirect regression fails when capability redirect protection is removed
and passes with it restored.

Full daemon, pairing and daemon-command suites on the secondary Linux runner
still encounter the AEON-1627 shell/executable fixture limitations, including
missing zsh, `/bin/bash` and `/bin/true`. The final static pre-filter passed
42 checks; `test-tiers` and `web-shard-spec-tests` timed out. An earlier snapshot
passed all 44 checks. Final hosted validation remains pending; these failures
were not skipped or reported as green.

Fix round 4 restores paired attached-hook coverage through the real bearer
middleware. The two restored recovery/delivery and ownership-audit regressions
fail with `run_capability_upgrade_required` in the tests-only snapshot
`84e0c6274`, whose production code is unchanged from `6edee174`. All four
focused attached-hook regressions pass after the fix, including exact
session/lease and S1-03 ownership, person revocation after HTTP admission, and
content-free SSE closure after revocation. The full harness and authentication
suites pass; authorization, database, runs and reporter contracts also passed
in the affected run. The explicit pairing fence caller inventory, primitive
order, auth lock inventory and negative controls also pass remotely.

The final pairing suite reports only
`TestPathProofCommandsRunInShells: shell_test.go:25: zsh is not installed` on
the secondary Linux runner. The primary Mac test lane is RED. The required
static runner was attempted three times and returned exit 3 each time because
the night protocol paused checks on the runner shared with the CI pool. Its
response directed validation to the PR's hosted CI static job. Full pairing
validation on a runner with its required shells and `node scripts/ci-static.mjs
--merge-main` remain pending. No test or assertion was skipped or loosened.
