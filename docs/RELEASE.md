# Release

Aeon uses INSPR Calendar Versioning, currently INSPR-CalVer3 (`inspr-calver-3`). The coordinate is `YYMMDDhhmmss.0.0`: two-digit year, month, day, hour, minute, and second in UTC, then `.0.0`. It is SemVer-shaped and fixed width. `version.json` is the only source of that coordinate. The fields that matter for a release are `version_scheme`, `version`, `release_channel`, and `release_sequence`.

### From CalVer2 to CalVer3

Releases up to `260929113854.0.0` (release 10, sequence 105) were reserved under `inspr-calendar-v2` (INSPR-CalVer2). Both schemes share one coordinate, so versions keep sorting as time and every earlier tag stays exactly as published. From release 11 on, a reservation writes `"version_scheme": "inspr-calver-3"` in `version.json` with a coordinate later than `260929113854.0.0`; `scripts/verify-release.mjs` rejects a new reservation that still declares `inspr-calendar-v2` (`LAST_CALVER2` in that script, `releasehistory.LastCalVer2` in Go). Only the reservation changes; `release_sequence` continues.

The version pill uses the shared INSPR renderer: six segments (`YY·MM·DD hh:mm`, seconds on hover, focus or tap), never the `v` or `.0.0`. Copying always yields the exact canonical version, `.0.0` included. The label table (`schemes.json`) names the schemes INSPR-CalVer3, INSPR-CalVer2 and INSPR-CalVer1.

The git tag is `v` plus the `version` field, for example `v260926064658.0.0`. Create an annotated tag with a message: `git tag -a "$tag" -m "Release $tag"`. `scripts/release-tag.mjs` checks that a pushed tag has that shape; the release workflow rejects it unless `git cat-file -t "refs/tags/$tag"` returns `tag`. `scripts/verify-release.mjs` checks that `version.json` matches the scheme and that the vendored calendar presentation bundle under `web/src/vendor/calendar-version-display` matches `scripts/calendar-version-bundle-pin.json`. `just release-check` runs the verifier. A production web build runs the same check before it emits assets.

Development builds leave the linker version at `dev`. A release build sets:

```
-X github.com/inspr-at/paimos/internal/version.Version=<version>
```

with `-trimpath`. The server image, `aeon-cli`, and Linux `paimos-agentd` use `CGO_ENABLED=0` (`Dockerfile` for the image). Darwin `paimos-agentd` is built on macOS with `CGO_ENABLED=1` and links LocalAuthentication. `scripts/build-release-binaries.sh` is the build used by `.github/workflows/release.yml`.

## Workflow

### Test runner routing (AEON-438)

CI's hosted `runner-route` job calls `test-runner-route.yml`; routed test jobs
consume its JSON `runs_on` output behind independent event, ref and rerun-attempt
guards.
Only `push` and `workflow_dispatch` on `refs/heads/main` may select `[self-hosted,
Linux, ARM64, mbp2606]`. The reviewed workflows route PRs to `ubuntu-latest`;
a PR can modify those workflows or the guard, so runner-side admission is the
enforcement boundary. The manual `Test runner smoke` workflow exercises the same
router and small Go/Node checks. Normal PR and main CI retain the full Go suite
on seven hosted shards; Mac shard integration remains pending below.

**Active and required admission contract: mode B (Free plan), decided by Markus
on 2026-09-30 and recorded on NIX-600.** Publishing
`AEON_MBP2606_AVAILABILITY` requires all three controls below to be implemented
and verified; workflow guards and matching labels cannot bind a JIT runner to
the job the controller checked. GitHub can assign another queued job carrying
those labels, including a fork job racing the verified job.

1. **Verified JIT minting:** no idle pre-registered runners. NIX-600 mints only
   for an API-verified queued job whose event is `push` or `workflow_dispatch`,
   head repository is `inspr-at/paimos`, workflow is
   `inspr-at/paimos/.github/workflows/ci.yml@refs/heads/main` or
   `inspr-at/paimos/.github/workflows/test-runner-smoke.yml@refs/heads/main`, and
   head SHA is reachable from `main`. Missing or unverifiable metadata rejects
   admission. Record the verified job ID, run ID/attempt and unique runner name.
   Before **every mint**, cancel the runs for every queued mbp2606-labelled job
   the controller has not verified, or refuse to mint while any such job remains.
   This covers any event or ref, including directly edited `runs-on`, PRs,
   work-branch pushes, non-main dispatches, `merge_group`, `workflow_run` and
   `schedule`; leave no spare registrations. App **5134402** requires
   `actions:write` on paimos to cancel those runs.
2. **Job-started hook:** bake an executable hook into the sealed VM image,
   outside the checkout and actions-runner directory. Set
   `ACTIONS_RUNNER_HOOK_JOB_STARTED` to its absolute path in the image's runner
   startup configuration; never load the hook from the repository. Before any
   workflow step, require `GITHUB_REPOSITORY` = `inspr-at/paimos`,
   `GITHUB_EVENT_NAME` in exactly `{push, workflow_dispatch}`, and
   `GITHUB_WORKFLOW_REF` equal to one of the two fully qualified workflow refs
   above. Read and validate the payload from `GITHUB_EVENT_PATH`.
   **PR-shaped events or payloads are always denied**, including same-repository
   PRs; a matching `pull_request.head.repo.full_name` never admits them.
   On missing, malformed or mismatched metadata, the hook must kill both
   `Runner.Listener` and `Runner.Worker` and power the VM off (`poweroff -ff`)
   **before it returns**; the controller then discards the VM. A non-zero exit
   alone is insufficient. Keep the slot cache disk **LUKS2-locked at boot**;
   the controller unlocks and mounts it only after API attribution of this
   `runner_name` and hook admission. NIX-600's smoke acceptance must prove
   that a rejected job containing an `if: always()` step and an action with a
   `pre:` step produces **no workflow-step output and no cache write**.

   GitHub's [job hook documentation](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts)
   says "the job will not run"; this contract explicitly **does not rely on
   that claim**. In actions/runner at `ca43437862b6d6be24e6de73dff3971c99140c9a`,
   the hook is an ordinary `always()` pre-job step
   ([JobExtension.cs:302–310](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/JobExtension.cs#L302-L310)).
   A failed step only updates the job result
   ([StepsRunner.cs:274–278](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/StepsRunner.cs#L274-L278));
   later step conditions are still evaluated
   ([StepsRunner.cs:203–241](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/StepsRunner.cs#L203-L241)),
   and action `pre-if` defaults to `always()`
   ([ActionManifestManager.cs:458](https://github.com/actions/runner/blob/ca43437862b6d6be24e6de73dff3971c99140c9a/src/Runner.Worker/ActionManifestManager.cs#L458)).
3. **Controller post-job check:** query the actual completed job via the
   [workflow-jobs API](https://docs.github.com/en/rest/actions/workflow-jobs#get-a-job-for-a-workflow-run).
   Match its `runner_name` to the minted runner and confirm its job ID and run
   ID/attempt are exactly those verified before minting, including assignments
   from unexpected runs rather than checking only the expected run. A mismatch
   alerts the operator and pauses mode B by clearing/refusing availability; missing or
   unverifiable evidence also fails closed. This check runs outside the VM;
   cleanup and verification must not depend on a job-controlled completion hook.

**Deny recording:** write a deny line in the GitHub job log. The VM cannot reach
the host to report a deny, so the controller treats **any VM power-off during a
job** as a deny (including pause or cancellation), pauses mode B and taints the
slot. Attribute the actual job/run/attempt and `runner_name` through the API.

The controller, hook and `ci.yml` router use the same smaller allowlist:
**`push`, `workflow_dispatch` at `refs/heads/main` only**. `schedule` and
`merge_group` are excluded; no tags are routed. Expanding events or refs requires
a reviewed change to all three controls and the workflow guard.

**Preconditions for availability and every mint:** paimos main ruleset
**24240960** (AEON-411 part 1) must exist, have `enforcement: active`, and match
the reviewed ruleset baseline, including rules, parameters, ref conditions and
bypass actors. Missing, disabled, weakened, changed or unverifiable protection
refuses minting and clears/refuses `AEON_MBP2606_AVAILABILITY` (mode B off).
The NIX-600 app `inspr-mbp2606-runner` (**5134402**) requires `actions:write`
for queue cancellation and has `administration:write` on paimos and can edit
rulesets, so the controller verifies the ID, active enforcement and unchanged
rules through the API before **every** mint; the app's permissions are not proof
that protection remains intact.

**Mode-B runtime:** every job gets one fresh Linux ARM64 Lima VM cloned from a
sealed base image containing rootful Docker, actions-runner and the baked hook,
with **no host mounts**. The JIT runner executes inside that VM. The job has
root inside its VM, so the VM is the isolation boundary; the controller deletes
it after completion or rejection. Persistent caches use one **LUKS2-locked disk
per slot**, unlocked and mounted by the controller only after API attribution
and hook admission, read-write **only for verified main pushes**. Lima 2.2
`format:true` repartitions on every boot: attach slot disks with `format:false`
and require an explicit `--init` on first use. After **any deny, pause or
mismatch**, restore the tainted slot disk from its last known-good APFS
clone, captured after the previous verified main push. Promote a new known-good
clone only after the external post-job check passes for a main push. Dispatches
get a throwaway clone of known-good, with disposable scratch/overlays. A cache
tarball over the controller's SSH is the fallback after admission; write-back
is allowed only for verified main pushes.

**Network precondition:** a host `pf` anchor for user `ci` blocks private ranges
and host loopback, except the Lima SSH loopback ports **60019–60023**. This
stateless, public-key-only exception, with per-instance keys and **no private
key in any guest**, is an **accepted residual risk**. User `ci` has **no port-53
egress at all**. VM DNS resolves through the Lima hostagent → `mDNSResponder`;
router **TCP 53/80/443 and UDP 53** are blocked from the VM. Direct resolver
fallback and port 53 to arbitrary LAN hosts are not allowed in this contract.
The controller verifies that the host anchor is installed and active before
publishing availability and before every mint; a missing, inactive or
unverifiable anchor keeps mode B off. An in-VM firewall does not satisfy this
boundary, because the job has root.

Org state verified on 2026-09-30: only the **Default** runner group, public
repositories not allowed, **0 runners**; Blacksmith is removed.

Mode A is only a possible future Team-plan upgrade: a group restricted to paimos
and the two selected main workflow refs, recorded and verified before adoption,
retaining the baked job-started hook as defence in depth.

Fork-PR approval is `all_external_contributors` (set by the lead, 2026-09-30).
That is defence in depth, not the runner admission boundary. A `merge_group` run
executes PR code, so queueing a PR is a decision to run it on the Mac if routing
is ever enabled for that event. **It is excluded from the mbp2606 allowlist in
mode B today**: GitHub documents exact pinned workflow refs; matching
`gh-readonly-queue/…` refs to the selected `main` workflows is unverified.
Merge-queue CI continues on hosted runners. See GitHub's
[runner-group workflow restrictions](https://docs.github.com/en/enterprise-cloud%40latest/actions/how-tos/manage-runners/self-hosted-runners/manage-access).

Routing is disabled until NIX-600's controller publishes the repository variable
`AEON_MBP2606_AVAILABILITY` on `inspr-at/paimos` with this value-free shape:

```json
{"schema":1,"repository":"inspr-at/paimos","os":"linux","arch":"arm64","online":true,"busy":false,"observed_at":"2026-09-30T10:00:00Z","idle_runners":4}
```

The schema remains **version 1**; mode and rerun-attempt metadata require no new
availability fields. In mode B, `idle_runners` counts available VM execution
slots, not idle registered runners. The controller observes live capacity,
publishes only when online and idle, refreshes at least every 10 seconds, and
clears the variable before draining/stopping the pool. Records expire after
**30 seconds**. An absent, invalid, expired, future-dated, offline or busy record
selects hosted immediately;
there is no network wait and no runner administration credential in CI. GitHub's
[runner-list API](https://docs.github.com/en/rest/actions/self-hosted-runners#list-self-hosted-runners-for-a-repository)
requires repository Administration read access; that belongs to the host
controller, not the workflow token. Neither token permissions nor environment
secrets are added here. The Linux ARM64 pool must provide Docker service-container
support, Ubuntu-compatible `apt`/`sudo`, Go 1.26 and the shells used by the tests.
Routable `go` and future routed `e2e` jobs must not assume amd64:
`pgvector/pgvector:pg18` is multi-arch, and setup-go/setup-node select ARM64 on
this runner. The guard rejects routed jobs referencing `amd64`, `x86_64`,
`x86-64`, `i[3-6]86` or the token `x64` in artifacts, including action inputs,
services, matrices and inherited environment/default settings. `release.yml`
and `pairing-platform.yml` are **never routed**; their multi-platform artifacts
and evidence remain hosted.

**Off/drain:** after clearing availability, NIX-600 keeps minting JIT runners for
API-verified queued jobs that still carry the mbp2606 label, until that queue
empties, and lets running jobs finish, with every mode-B control and precondition
still enforced. A ruleset/network failure or post-job mismatch stops minting and
cancels affected queued runs rather than draining through a failed boundary.
**Hard stop:** cancel those queued and running runs before stopping capacity;
do not leave them stranded waiting for a runner. A lease is an admission check,
not an atomic reservation: concurrent
admissions or host failure after selection remain a queue risk. Never publish
a simple persistent `on` flag. Offline/busy smoke and full-suite timing must be
recorded when the host becomes available.

For routed workflows, use **Re-run all jobs** (`gh run rerun RUN_ID` without
`--failed`) so the hosted router refreshes the lease. It emits `run_attempt`;
consumers compare it with `github.run_attempt` and select hosted if a failed-job
or individual-job rerun retains an older output. This rejects stale attempts,
not a lease that expires after initial job scheduling; the controller's draining
duties cover already queued jobs. See GitHub's
[rerun behavior](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs).

**TODO (AEON-408):** main now includes the seven-shard Go layout; this branch
keeps those shards and the static/aggregate gates hosted. On Mac integration,
use **4 Go shards on mbp2606, 7 on hosted**, driven by the router's runner class;
require 4 idle slots for the mbp2606 batch. Retain
the shard commands and hosted aggregate/static gates, add `runner-route` to
`needs`, and copy the Go job's guarded `runs-on` and actual runner-class evidence.
The shard inventory must include `scripts/ci-runner-guard`. Insufficient capacity
sends the entire batch to hosted; broader routed fan-outs must request their
whole simultaneous capacity through `required-idle-runners`.

Every evidence-producing Go test on mbp2606 uses **`go test -count=1`** to bypass
cached test results. Routed action caches and the controller's persistent Go,
npm and Playwright cache volumes are written **only by pushes to main**.
Dispatch jobs may read trusted caches but write only disposable per-job scratch
or overlays; NIX-600 must enforce that isolation. The routed workflows disable
`setup-go` caching outside main pushes. A test cache hit is not fresh evidence.

`go run ./scripts/ci-runner-guard` scans **all** workflow YAML, including `.yml`
and `.yaml` in any letter case. Hosted labels are exactly `ubuntu-latest`,
`ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15` and `macos-15-intel`; the controller
must not assign these labels to self-hosted runners. Its fixture tests reject
direct labels, hosted-looking impostors, unsafe expressions, matrix labels,
unguarded router outputs, routed jobs with secrets/environments/write permissions,
amd64 artifact references, and routed release/pairing/image/attestation/pin jobs.
Unknown dynamic expressions fail closed.
The hosted `release-check` runs both guard and router tests. Release workflows,
image build/relink, attestation and pin gates always stay hosted; attestation
verification must retain `--deny-self-hosted-runners` in its owning gate.

The lead accepted mbp2606 green evidence for tree-keyed reuse of **tests/evals**
only (AEON-438, 2026-09-30). Successful routed jobs record their actual
`runner_class=hosted|mbp2606`, source commit and event in the job summary. Any
future reuse record must preserve that class. Image provenance, attestations and
pin gates may never reuse that evidence. This change adds no tree-skip mechanism.

A push of a `v*` tag runs `.github/workflows/release.yml`.

1. Check out the repository with tags, so release history can see earlier coordinates.
2. Validate the tag and run `scripts/verify-release.mjs --release`. Fail if `version.json` disagrees with the tag.
3. Refuse a coordinate whose GitHub release already exists, including drafts (the authenticated, paginated release list includes them). macOS runners build darwin `paimos-agentd` with CGO enabled, then sign it with Developer ID (team P66J39QV6V, hardened runtime) and notarize it in the `release-signing` environment before upload (docs/AGENT_INTEGRATION.md, Signed release daemon). The Ubuntu job builds Linux `paimos-agentd` statically and all `aeon-cli` targets with CGO off, then checks the darwin binaries.
4. Generate the release-history manifest embedded in the server image. That file is produced at release time. It is not committed.
5. Run the image smoke gate. Publishing waits for it.
6. Refuse a coordinate whose GHCR image tag already exists, including a tag left by a partial earlier run. Push the image to `ghcr.io/inspr-at/aeon:<version>`. There is no `latest` tag.
7. Create the GitHub release once as a **draft** with the CLI, signed/notarized darwin `paimos-agentd`, Linux `paimos-agentd`, and `SHA256SUMS`. Existing releases are never uploaded to or overwritten. The notes name the image and its digest. A successful tag build ends here; it does not publish the draft or open a Homebrew PR.
8. The release coordinator deploys that exact image digest through the normal deployment gates and verifies the live server's version and health. Only then publish the existing draft as described below. Failed or incomplete verification leaves it a draft.
9. Publication triggers `.github/workflows/homebrew-tap.yml` (`release: published`). Its `homebrew-tap` job validates the exact event tag, rejects drafts and prereleases, and reads public release metadata before downloading `SHA256SUMS`. It renders `Formula/aeon-agentd.rb` from that release's darwin checksums and, when `HOMEBREW_TAP_APP_ID` and `HOMEBREW_TAP_APP_KEY` are present in the `homebrew-tap` environment, opens a pull request on `inspr-at/homebrew-tap`. The formula installs the signed, notarized darwin bytes with `bin.install` and does not rebuild or re-sign them. If either secret is absent the job logs `homebrew tap bump skipped: app secrets absent` and succeeds. The stable 105 sample is [docs/homebrew/aeon-agentd.rb](homebrew/aeon-agentd.rb).

### Publish after live verification (AEON-356)

Run this explicit step from the release coordinator's checked-out release commit, only after recording the successful live verification against the image digest in the draft notes. Confirm the tag, draft state and complete nine-asset set (eight binaries plus `SHA256SUMS`); do not publish a draft from a failed or partial tag workflow.

```sh
tag="v$(node -p 'require("./version.json").version')"
gh release view "$tag" --repo inspr-at/paimos --json tagName,isDraft,assets,body
# After live verification and inspection above:
gh release edit "$tag" --repo inspr-at/paimos --draft=false
gh release view "$tag" --repo inspr-at/paimos --json tagName,isDraft,publishedAt,url
```

Use the coordinator's approved GitHub CLI identity (or an approved GitHub App identity) with release write access. Do not publish using a workflow's `GITHUB_TOKEN`: GitHub suppresses downstream release-event workflows for that token. Publishing via this CLI step emits `release.published`, which starts the Homebrew workflow at the release tag. The new workflow must be included in the tagged commit. See GitHub's [release event reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release) and [workflow token restrictions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow#triggering-a-workflow-from-a-workflow).

Confirm `isDraft: false`, public asset availability, and the Homebrew workflow result/PR before considering distribution complete. If the tap job fails, fix its cause and rerun that job; do not rerun the tag build, toggle publication to retrigger it, replace assets, or reuse the coordinate. The tap PR still follows its own checks and merge approval. Missing app secrets mean no automatic PR; record that result for the coordinator to resolve before claiming Homebrew is updated.

Drafts are excluded from public release discovery and GitHub's `latest` endpoint. The tap script uses unauthenticated exact-tag metadata and asset requests, with an explicit published-state check before any tap mutation. The server's installation guide already pins the running server version and uses unauthenticated exact asset URLs; it never enumerates authenticated drafts or substitutes `latest`. Between server deployment and publication those downloads fail closed, and Homebrew still offers its previously merged release. Publication makes the same pinned URLs available without a server rebuild. Never give these consumers credentials to read drafts. See GitHub's [release API visibility rules](https://docs.github.com/en/rest/releases/releases#list-releases).

## Image smoke gate

`scripts/smoke-image.sh` builds the release image and exercises it before anything is published. It checks the pinned Chromium and tini packages, their licenses, and `NOTICE`. It then starts a disposable Postgres and the server with mounted secret files, and checks startup, the database role, UID 65532, health, and headers. The script's dev mode is only for authenticated upload and quote calls. Live OIDC is not part of the gate, because the database is disposable and has no identity provider.

The gate needs Docker. It is a release check, not the day-to-day `just test` run.

## Release assets

| Asset | Where |
| --- | --- |
| Server image | `ghcr.io/inspr-at/aeon:<version>`, linux/amd64, provenance enabled |
| `aeon-cli-darwin-arm64`, `aeon-cli-darwin-amd64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64` | GitHub release for the `v` tag. Install the file as `aeon`; a symlink named `paimos` selects paimos mode. |
| `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, `paimos-agentd-linux-amd64` | Same GitHub release. Darwin binaries link LocalAuthentication. Linux binaries are static. The current Nix package is named `aeon-agentd` and builds `bin/aeon-agentd`. Both names come from `cmd/aeon-agentd`. |
| `SHA256SUMS` | Same GitHub release, covering the CLI and agentd files above. Check it with `sha256sum -c` or `shasum -a 256 -c` before installing. |
| Flake | `flake.nix` in this repository. `packages.<system>.aeon` is the CLI plus a `paimos` symlink. `packages.<system>.aeon-agentd` is the supervisor. The version is the `version` field of `version.json`. |

Nix install of the CLI:

```
nix profile install github:inspr-at/aeon#aeon
```

The flake reference keeps resolving after the repository is renamed to `inspr-at/paimos`, because GitHub redirects the old name.

The four binaries are cross-built, not a claim that all user-service lifecycles work. `.github/workflows/pairing-platform.yml` runs an isolated fake-executable launchd or systemd-user fixture on `macos-15` (arm64), `macos-15-intel` (amd64), `ubuntu-24.04-arm` (arm64), and `ubuntu-24.04` (amd64). A platform is qualified only after that runner's real service install, status, drain, and removal check passes on the integrated commit. Other macOS releases and Linux distributions have no lifecycle evidence from this matrix.

Published coordinates are immutable. The existing stable86 release `v260927181849.0.0` predates the fourth daemon target: its `paimos-agentd-darwin-arm64` asset answered HTTP 200 and its `paimos-agentd-linux-arm64` asset answered HTTP 404 in read-only HEAD checks on 2026-09-27. A guide serving that version must omit Linux arm64 rather than point at a future asset or rewrite stable86.

Screenshot data for a dev tenant is `aeon demo seed`. See [DEMO.md](DEMO.md). That command is not part of the release tag workflow.

## Ticket benefits and release-note snapshots (AEON-256)

Tickets store `pill_en`, `pill_de`, `benefit_en`, `benefit_de` and
`hide_from_release_notes` in `nodes.fields`. Migration `0896` adds their optional
schema properties for every tenant and replaces `aeon_seed_node_kinds` for new
tenants. It changes no node values, translations, events or release artifacts.
Custom unrelated properties and constraints stay in place. Schema requirements
are deliberately optional so incomplete drafts can be created with warnings.

The generic node API requires both pills (2–4 whitespace-separated words) and
both nonblank benefits when a ticket enters a built-in completed state (`done`,
`accepted` or `delivered`) from outside that set, including creation in any of
those states, direct PATCH, bulk changes, CLI calls and bulk undo. Normal updates
check the final fields and state while holding the node row lock. Bulk skips an
incomplete ticket with a reason; bulk undo rejects the entire invalid reversal.
An already-completed ticket remains editable, including transitions within that
set, without fabricated backfills; reopening and completing it again invokes the
requirement. Hiding a ticket is not an exception. Sentence count, positive plain
language and translation fidelity are
editorial requirements, not claimed as machine-verified. This is an application
transition rule, not a SQL constraint: historical import/migration writers retain
their existing behavior. Cancellation, archival and tenant-defined state names
are not guessed to mean successful completion.

For a new release, read the supported authenticated endpoint
`GET /api/projects/{projectId}/releases/{releaseId}/note-snapshot` using a
project-scoped `releases.read` and `nodes.read` key (or an authorized person). The tenant comes
from authentication, not an input parameter. The export uses one SQL statement:
`journey_tickets.release_node_id` is the sole membership source, joined by tenant
and project; `nodes.fields` supplies exactly the five benefit properties.
Backlog tickets, Git mentions and a project's other releases are not membership.
Deleted/unavailable public members remain explicit gap entries. `captured_at`,
release revision and each member's `updated_at` record the observation. No live API or
classic database is contacted by the history builder.

At reservation, the release coordinator reviews that export and freezes its
public projection in `internal/releasehistory/data/product-notes.json` **before
tagging** (AEON-372):

```sh
go run ./internal/releasehistory/packnotes -repo . -snapshot SNAPSHOT.json -reserve VERSION -tenant TENANT_UUID -project AEON_PROJECT_UUID
```

`VERSION` must match `version.json`. Both UUIDs bind the source to the selected
PPM tenant and AEON project. This explicit reserve step may freeze an unpublished
preview; historical snapshot imports must already be frozen. Historic ticket
exports use the separate explicit workflow below. Use the configured API
client with `releases.read`/`nodes.read` to obtain the export. Keep the raw export
in its authorized local context: it may contain hidden text and tenant IDs and
must not be committed as the public projection. The generated file contains
only public ticket keys, pills, benefits, captured groups and capture provenance.
Review and commit it with the reservation. Identical reruns are idempotent
only with that same export file: a fresh export has a new `captured_at` and
conflicts. Restore the file before re-reserving; do not export the preview again.
Conflicting entries fail instead of rewriting a reserved version.

For historical backfill where snapshots exist, export stored snapshots as `VERSION.json` in
one directory and run the same command with `-snapshots DIRECTORY -tenant
TENANT_UUID -project AEON_PROJECT_UUID`. A run with no exports imports only
authoritative snapshots already in local tags and reports missing versions.
Alternatively, save an authorized PPM `GET /api/releases` response, record that
workspace's `tenant_id` and `project_node_id` on the saved file, and pass
`-history HISTORY.json -tenant TENANT_UUID -project AEON_PROJECT_UUID`. Prefer
`-history` over `-snapshots` for backfill. A snapshot file from before group
storage has no group, and this snapshot import cannot derive one offline; those embedded
items then take a group from the viewing tenant's own tickets, which usually
means Features. `-history` records the group the serving workspace already
derived. The two identifiers must match the flags; a file for another workspace
is rejected. This consumes only `database-snapshot` or immutable tag-snapshot
notes, never `changes.linked_tickets`. It records each ticket's group, including
a group the server derived from the live classification when the snapshot itself
had none. The explicit historic-ticket workflow below is the only import of current
ticket fields for missing snapshots; release PR bodies and pills.tsv are not sources.
Existing tags and artifacts stay unchanged; the new
binary carries the backfill. Migration `0997` captures future groups alongside
the five note fields. Older snapshots have no group. Serving those classifies
every commit ticket from the current classification, not only the tickets the
capture tells: a hidden bug, a ticket with no pill or benefit, and a commit
ticket that was not a release member. A bug is a fix and a visible benefit is a
feature, the same rule as AEON-289. Only those two facts are read. The captured
pill and benefit stay frozen, and live pill or benefit text never enters
`linked_tickets`. The history export above records that group, so other
workspaces see Fixes from the embedded notes.

A release with no capture uses that same classification for `changes[].group`.
It loses live-text Highlights by design: the no-live-text rule keeps pill and
benefit text out of the response, so Highlights has nothing to show until a
capture exists. Compare still follows the classified group.

A capture that already stores a group keeps that group and its frozen text.
Commit tickets the capture does not name — a hidden member, a member with no
pill or benefit, and a ticket that was not a release member — still take
`changes[].group` from those same two live facts. The note on that
classification stays empty, so live pill and benefit text never reaches
`linked_tickets` or Highlights. Tickets the capture already grouped are not
read again. A shared commit still takes the strongest group. Compare follows
that group.

`internal/releasehistory/generate` also reads legacy `release-notes/VERSION.json`
files **from their matching annotated Git tags**, including under `-offline`.
Later ticket edits cannot change those notes. The generated manifest records the exact
file SHA-256, tag/path, capture time, revision, both languages, hidden count and
gaps. Portable notes are the additive `notes.public_items`, with no tenant UUIDs;
the existing `notes.items` schema stays unchanged. Members sort by recorded position, then key and ID. Exact duplicate IDs
collapse; conflicting duplicates, duplicate keys, malformed metadata and a
recorded version that differs from the tag fail the build. If the release had no
assigned version at capture time (candidate registration can happen later), the
file's tagged path is the explicit coordinator-supplied build binding; that
missing recorded version stays a visible provenance gap. It is not inferred
from a ticket, timestamp or Git headline.

Missing snapshot files produce empty notes with a membership/field-data gap.
Incomplete visible tickets produce field-specific gaps; they get no invented
translation or Git-headline benefit. Hidden tickets contribute no text or key to
the public notes; they contribute only to the hidden count, even when benefit
fields are incomplete or the member was unavailable at capture.
Technical Git headlines, legacy top-level `tickets` references and changes remain
evidence; membership claims come only from the snapshot. The release detail shows
each captured ticket as one block under Features or Fixes (pill as heading, key,
benefit sentence, commits folded), exactly as it shows linked tickets of a release
without a snapshot; the tag message appears only under Evidence. Old v1 manifests without the optional
`notes` member and regenerated records with `notes.source = "unavailable"` keep
their ticket references, ticket filter and commits, but their tag message is not
shown as a title; their Git text is not presented as benefit notes or membership.
Available snapshots remain authoritative even when empty or incomplete. This
additive reader boundary does not modify any existing published artifact or
legacy tag.

Integration acceptance still belongs to the coordinator: review migration0896,
select and approve the production release/project mapping, capture/review/commit
an authorized snapshot before the next tag, verify its digest in the resulting
artifact, and live-test both languages. Automatic production snapshot capture,
server-side signed/sealed snapshots, GitHub release-body rendering, translation
backfills and company-rule publication are not implemented here. An omitted
snapshot is intentionally a visible gap, not a successful benefit-note release.
The writing-rule proposal is `docs/proposals/ticket-benefit-writing.json`, using
AR1's draft Rule DTO. It must be imported as a draft at the fetched revision and
published separately by an authorized human; it changes no effective harness
files or company rules.

### Historic product notes without journey membership (AEON-398)

Historic AEON tickets often have no journey release assignment. For published
releases without a capture, use the same Git tag history and union of release
and commit ticket keys as the history builder. This is explicitly later
`release-manifest-tickets` evidence, not original journey membership. No
database write, migration, tag rewrite or deployment is involved.

The history also recognizes historical lightweight release tags when their
committed `version.json` matches the tag and the coordinate is not an unpublished
reservation. Their channel and sequence come from that file, and their release
time comes from the tagged commit's committer date. Keep published tags unchanged;
new releases require annotated tags as described above.

From a full checkout with release tags, use a configured PPM agent client (or
wrapper) with `nodes.read`. It must select the approved PPM tenant; no credential
is passed on the command line. Both source UUIDs are explicit:

```sh
go run ./internal/releasehistory/exporthistoric -repo . -client /path/to/ppm-client -tenant TENANT_UUID -project AEON_PROJECT_UUID -out tmp/historic-notes.json
# Review the ignored local export, then freeze only its public projection:
go run ./internal/releasehistory/packnotes -repo . -historic tmp/historic-notes.json -tenant TENANT_UUID -project AEON_PROJECT_UUID
```

The exporter verifies the authenticated tenant, resolves each node's kind and
project ancestry, and reads the four bilingual note fields, type, tags and hide
flag. The output must be ignored and inside this checkout; an existing file is
never overwritten. Keep it local: hidden text is present for the projection
check and must never be committed. API failures, missing tickets, wrong source
bindings, duplicate keys and invalid hide flags fail the import rather than
publishing a partial history. A later release can repeat these two commands with
a new export filename; already captured versions remain unchanged.

`packnotes -historic` reuses `ParseTicketMeta` and the live linked-note projection
for Features/Fixes, including bug kinds, types and tags. Hidden notes are omitted;
tickets with no note text stay under Other. Missing translations stay empty.
The public bundle contains only ticket keys, existing note text, groups and
capture provenance. Its digest covers the version, manifest membership source,
capture time and selected ticket observations; `written_after_release` is true.
Only published versions without captures are added; reservations and existing
snapshots are skipped. Review and commit `internal/releasehistory/data/product-notes.json`
with the code. Export counts distinguish releases and ticket occurrences across
releases (classified, Other and hidden); one ticket can occur in several releases.

Regression: `TestHistoricNotesNonPPMTenant` builds historic Git membership,
imports the export and serves it without tenant ticket data. The Playwright
`release-historic-tenant.spec.ts` consumes that HTTP output and exercises both
groups, the release filters and Highlights/Details at desktop and phone widths.
`GET /api/releases` and `GET /api/releases/{version}` support agent keys with
`releases.read`; presentation writes remain person-only.

## Historical note backfill (AEON-290)

After migration `0939`, the deployed binary supports an offline maintenance
command against its configured database (no Git checkout, API login or network
history lookup). Both dry-run and apply require an explicit **active person**
with workspace `roles.manage` authority. Agents and inactive people are denied;
there is no implicit operator. Tenant and project visibility come from the
person's live bindings. The command does not run migrations.

```sh
aeon release-notes backfill --tenant inspr --project AEON --actor-principal-id PERSON_UUID --all-missing
aeon release-notes backfill --tenant inspr --project AEON --actor-principal-id PERSON_UUID --release VERSION --apply
```

The container entry point is `/paimos release-notes backfill` with the same
flags. Omit `--apply` for a read-only plan. `--release` and `--all-missing` are
mutually exclusive; omitting both means all missing snapshots in that project.
The JSON report lists version, tickets found, notes count and hidden count for
each planned/inserted capture, plus `excluded_keys` (resolved manifest tickets
that are not completed) and `gap_keys` (captured public tickets with incomplete
fields or unavailable members), plus unchanged and skipped counts. Hidden
tickets never contribute gap keys.

For historical tags, membership is the union of ticket keys in the embedded
manifest's release and listed commits, resolved only within the selected tenant
and project. Only completed tickets (`done`, `accepted`, `delivered`) are
captured; other states are excluded and reported. This is an explicit
approximation from Git evidence, recorded as
`membership_source: release-manifest-tickets`; it is not original journey
membership. Unresolved keys are not treated as tickets. The current five benefit
fields and their update times are captured once, including hidden tickets for
provenance. Public notes contain only the hidden count, never their benefit text
or missing-field warnings; this is decided when reading the frozen snapshot.
`released_at` uses the manifest publication time, or its tag time when publication
time is absent; neither is replaced with the capture time. Captures without an
original release time are skipped. Reservations are never captured.

The version-keyed table is tenant/project protected and insert-only, with the
same immutability trigger as native snapshots. Each insert records the person,
`backfilled: true`, `label: backfilled`, capture time and one audit event. Apply
is atomic across both paths. Reruns leave existing rows unchanged. Native journey
releases still use their own membership and snapshot store, through the same
person/admin authorization and project/version selectors.

`GET /api/releases` and its detail route use stored journey snapshots first,
then explicit version-keyed backfills for the visible AEON project, then the
embedded tag/public product notes. Tenants without that project use the public
product notes too. Empty or hidden-only tenant captures remain authoritative.
Without any capture, the historical evidence remains. The overlay is computed per request and never
changes the shared embedded manifest. A malformed database snapshot is logged
without its payload and falls back for that release alone; other releases remain
available. Releases with no public notes or gaps show a quiet **Internal changes
only** in the detail (or **No notes for this release** without any commit); their
tag message is evidence only. Backfilled notes
show **Notes written after release**. No original tag, artifact, published
timestamp, or existing snapshot is rewritten. The coordinator owns production dry-run review and apply.

## Release presentation (AEON-305)

Every release introduces itself with a short **theme** (the kicker, 1–80
characters, one line), one **headline** sentence (up to 200 characters, one line)
and a short **intro** (two or three sentences, up to 600 characters), each in
English and German. The release detail shows them as a compact header above the
blocks every release shows: Features and Fixes with one block per ticket (the
pill as heading, the key, the benefit sentence and its commits folded), then
Other changes. A captured or backfilled snapshot decides which tickets are told
and their text. Without a capture, Aeon does not fill Highlights from live
ticket text: that is the no-live-text rule. Compare still uses the served change
group. A ticket is a fix when it is a bug (the served change group), else when
its commits are only `fix:`; otherwise a feature. The release list shows version,
date and theme, or the pills. A release without a presentation has no header.
The Git tag message is evidence, never a title; "Notes written after release"
is one muted line.

Presentations live in `release_presentations` (migration `0945`), keyed by
tenant, product project and calendar version, protected by tenant RLS and project
visibility, separate from tags, manifests and note snapshots. The version need not
be in the running build yet, so the presentation can be written before the new
build is live. English theme and headline are required; German fields may be
empty and readers then show English. Writing identical text changes nothing.
Every change records one `release.presentation_set` event (before and after);
removing one records `release.presentation_cleared`.

**The release agent must write the presentation for every new release**, in both
languages, as part of the release, right after the notes snapshot is final and
before announcing the release. It is written against the production database from
the deployed container, like the backfill, and is a dry run until `--apply`:

```sh
/paimos release-notes present --tenant inspr --project AEON --actor-principal-id PERSON_UUID \
  --release VERSION \
  --theme "Releases with a name" --theme-de "Releases mit Namen" \
  --headline "Every release says what it is about." --headline-de "Jedes Release sagt, worum es geht." \
  --intro "Two or three sentences." --intro-de "Zwei oder drei Sätze." \
  --apply
```

or with the six fields as a JSON file (`-` reads stdin; use `docker exec -i`),
which avoids shell quoting:

```sh
/paimos release-notes present --tenant inspr --project AEON --actor-principal-id PERSON_UUID \
  --release VERSION --file - --apply < presentation.json
# {"theme_en":"…","theme_de":"…","headline_en":"…","headline_de":"…","intro_en":"…","intro_de":"…"}
```

`--clear` removes a presentation. Outside the container the binary is `aeon`
with the same arguments. The actor is an active **person** with `releases.deploy`
on the project (workspace admins and owners); offline there is no agent key, so
the release agent names its operator, as for the backfill. The command prints a
JSON report with `applied`, `tenant_id` and `change` (`version`, `changed`,
`before`, `after`). People can also use `PUT` and `DELETE
/api/releases/{version}/presentation` (same authority; optional
`expected_revision` answers 409 when stale).

Write for the reader, not the repository: the theme names what the release is
about in a few words, the headline says what changes for them in one sentence, the
intro adds context in two or three. No ticket keys, package names or commit
jargon; those stay in the rows and the commits.
