<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Run content capabilities

An ordinary paired computer's API credential is transport authority. Existing
person-approved workstation designations retain their separate live-key, signer
and confirmation checks. Its run poll, run
list and run detail expose an explicit metadata projection: identifiers,
execution mode, model/account selection, lifecycle, revisions and accounting.
They do not select or return trace, recovery handover, display labels or
free-text outcome detail. Content readers and launch claims require a separate
server-issued credential.

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
criteria only during Prepare. Tools capture their run binding; session calls
retain that same proof. Credentials stay in daemon memory, outside journals,
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

The exact owned stop/yield/confirm-exit/control-completion safety routes retain
their private worker-lease checks without a content capability. The server also
rechecks a claimed managed run or an active daemon attachment; a lease on a
person-created direct registration does not establish daemon ownership. Bare paired
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
