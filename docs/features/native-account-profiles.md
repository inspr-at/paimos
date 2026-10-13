# Native account profiles

AEON-1272 / P4a provides the local `nativecontract.ProfileResolver` implementation
in `internal/nativeprofiles` and the CLI's `resolveNativeAccount` helper. These are
building blocks for P4c admission and I2 runtime wiring; no production launch path,
login, account selector, confined environment or automatic rotation is changed.
This groundwork stays hidden from release notes until the integration is enabled.

The owning daemon constructs a resolver from trusted snapshots of **existing**
enrollments and approved workspaces. Each enrollment names one tenant, person,
trust context, opaque local account key, account UUID, revision, private physical
config folder and pinned official binary/SDK. No public request supplies a folder
or environment. The resolver copies this metadata, accepts an exact C0
`ProfileRequest`, and returns a local-only value snapshot for a single child.
Unknown accounts, another owner/context, stale revisions and changed directory
identities refuse resolution. There is no fallback or rebind method.

For the existing default Claude folder, exactly `real HOME/.claude`, the child
gets real `HOME` and **no** `CLAUDE_CONFIG_DIR`, preserving the current keychain
selection. Dedicated native profiles also keep real `HOME`, while their own
vendor selector points at the enrolled folder. Native, real-HOME, independent
login and vendor-history evidence must match the actual OS and binary/SDK pins;
dedicated folders without independent-login evidence remain unavailable.
Claude, Codex, Pi, Cursor and Grok have explicit selectors. Providers requiring
replacement `HOME` remain unsupported pending separate qualification.

Child environments are new arrays containing only `HOME`, the selected vendor
folder when needed, supplied `USER`/`LOGNAME`/`TMPDIR`, a binary-directory plus
system `PATH`, and fixed locale. Ambient daemon/vendor credentials, provider
overrides, other account selectors and loader flags are excluded. Neither
`os.Setenv` nor shell exports are used. The CLI helper requires the authorized
actor's exact grant/revision and the approved physical workspace root, and returns
a copied environment; the existing `use` command retains its old meaning.

Only directory metadata is inspected. Folder paths and their ancestors must be
physical, the final directory must belong to the OS user, and account folders
must have no group/other permissions. Account/history folders may neither overlap
nor alias each other, even across tenants or owners. Workspaces cannot contain
account state. Metadata is checked again at resolution and replacement of a bound
folder is rejected. No directory is created by resolution, and no credential
file is opened, read, copied or symlinked.

There is **no automatic settings or skills sharing**. Any desired nonsecret
settings/skills must first be explicitly approved by the owner inside the same
tenant/person/trust context and prepared by the operator. Credential links,
login-state copying, shared history across contexts and global `cswap` are not
part of this path. The resolver cannot inspect files to determine whether an
operator-prepared setting is nonsecret; it grants no new tool/settings authority.
I2 must retain the immutable tool/settings policy and suppress obsolete binding
hooks through its approved overlay, with no edits to global personal settings.

Official sign-in and enrollment remain person/OPS-led. A private folder does not
prove macOS keychain namespace isolation or native-history portability. The
coordinator/OPS/person-owned **G_ACCOUNTS** gate must prove the allowed profiles
retain their identities concurrently on the actual OS, using current pins and
approved vendor/account limits. Fake tests provide no such approval. Rebinding
requires an authorized stopped successor with confirmed old-writer exit, fresh
account/context admission and explicit conversation portability qualification;
quota evasion and silent account rotation remain unavailable.

P4c/I2 must revalidate current login identity, permissions, account revision, pins
and shared-ledger occupancy under the final spawn/write fence. The resolver's
metadata check alone cannot lock out an operator changing a login between resolve
and spawn. An idle or taken process still owns its reservation; uncertain exit
never permits a successor.

The named risk is macOS keychain namespace and vendor-history portability.
`TestProfileIsolationPreservesCredentialsAndParentEnvironment` uses retained fake
credential state and two explicit metadata barriers to prove overlapping child
profiles are distinct, default Claude selection is preserved, input snapshots
and the parent environment stay unchanged, and no credential IO occurs.
`TestProfileResolutionRejectsForeignStaleAndUnqualifiedAccounts` covers account,
scope, revision, pin, OS, history qualification and injected-clock policy expiry.
`TestProfileDirectoriesRejectAliasesAndUnsafeHomes` covers alias/symlink/privacy
and changed-directory refusals, including a temporary real filesystem fixture.
`TestNativeAccountStartBindsAuthorizedProfile` verifies exact CLI grant/account
binding, refusal propagation without fallback and copied child environment.
There are no browser tests or model/subscription calls for this slice.

R0's `internal/native*/**` glob in audit slice **S2 agent-runtime** now covers
the profile package through PR **#623**, included on this branch. The two CLI
files are covered by the existing **S3 cli-inbox** `internal/cli/**` slice. The remaining R0 step
in this PR before merge is to register all four named Go tests above as
**ESSENTIAL** in `scripts/ci/go-test-tiers.json` for `internal/nativeprofiles`
(three tests) and `internal/cli` (one test). This builder does not own the shared
metadata files. No web shard, route, permission, migration, DSAR or shared-fence
rows are needed: this slice adds none of those surfaces.

The exact additions to the Go manifest's `tests` array are:

```json
[
  {"kind":"go","package":"internal/cli","name":"TestNativeAccountStartBindsAuthorizedProfile","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativeprofiles","name":"TestProfileDirectoriesRejectAliasesAndUnsafeHomes","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativeprofiles","name":"TestProfileIsolationPreservesCredentialsAndParentEnvironment","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativeprofiles","name":"TestProfileResolutionRejectsForeignStaleAndUnqualifiedAccounts","tier":"ESSENTIAL"}
]
```

Required validation is the guarded remote Go run for
`./internal/nativeprofiles/... ./internal/cli/...`, guarded
`ci-static-locked.sh --merge-main`, and
`node scripts/test-tiers/affected.mjs --base origin/main`. A busy/held/incomplete
exit 3 is an explicit incomplete handoff, never a passing result; PR CI and the
coordinator's cross-family release gates remain authoritative.

Initial builder handoff (2026-10-13): the affected-set command exited 0 and selected
**FULL** (`Go reverse dependencies exceed 300 extra cases (3219)`). Formatting,
Git whitespace and exclusive-write-set checks passed. The required guarded Go
run was attempted three times and guarded static twice; all returned **exit 3**
because the global two remote slots were occupied (2/2). Neither Go execution nor
static verification completed, and no local fallback or negative-control run was
performed. R0 must register the source and the four tests in this same PR; then
the coordinator retries the guarded checks and requires authoritative PR CI.
G_ACCOUNTS and P4c/I2 integration remain separate coordinator-owned work. No
origin push, deployment, model CLI or cross-family gate was performed.

Fix-round 2 (2026-10-13): PR #615 `go-static` job **115092406004** rejects
exactly the four unclassified Go tests listed above. `release-check-run` job
**115092406143** rejects the five files in `internal/nativeprofiles` as
unassigned; both reported audit test failures have this same cause. The local
audit suite reproduces those two failures (31 of 33 tests pass). A read-only,
in-memory ownership check passes for the entire current snapshot after adding
`internal/nativeprofiles/**` to S1. The exact R0 change remains that one glob and
the four **ESSENTIAL** test rows described above. P4a has no authority to edit
these coordinator-owned inventories under `plan.json` / `plan.md`; they must
be applied by R0 in this PR before the CI failures can be closed.

The guarded remote Go run of baseline `1907d7e0` also exposed a fixture setup
failure under the runner's umask 022: `t.TempDir()` was not private. Commit
`5452d0d4a` explicitly sets the physical fixture directory to mode 0700 before
the existing acceptance assertion. The later mode-0755 refusal assertion is
unchanged, as is production code. This baseline failure and the successful
guarded rerun provide the negative control; both `./internal/nativeprofiles/...`
and `./internal/cli/...` now pass through the guarded remote runner. The affected selector exits 0 and
selects **FULL** (3225 reverse-dependency cases). No web unit or browser spec is
affected by this non-browser slice. P4a acceptance checks pass for exact enrolled
account binding, default/dedicated child environments, exclusion of credential
sharing, refusal of unqualified accounts without rotation, and retained fake
credentials with distinct overlapping children and an unchanged parent
environment. Live account qualification remains G_ACCOUNTS.

Guarded `ci-static-locked.sh --merge-main` completed remotely with **exit 1**:
`go-tiers`, `audit-tests` and `audit-coverage` fail solely on those missing R0
rows. The other **39 checks pass**, with **zero optional skips**, including Go
vet, test-tier selector regressions, web shard checks, web typecheck/lint and
OpenAPI/migration checks. This is a failed CI handoff, not a green result. Only
the test fixture and this feature document changed in this round; the worktree
has no shared-metadata edits, origin push, deployment, model invocation or
review-gate action.

Fix-round 3 (2026-10-13): `go-static` job **115099172008** rejected two
operator-host references in this document. They now use neutral wording.
Baseline `6082e94e` fails the existing public-name guard at those two lines;
the guard passes after commit `c636e6ac4`. No production behavior or test
assertion changed. Both affected Go packages pass through the guarded remote
runner. No web unit or browser spec is affected by this documentation change.

Guarded `ci-static-locked.sh --merge-main` returns **exit 1** with **39 checks
passing and zero optional skips**, including the public-name guard, Go vet,
test-tier regressions, web shard regressions, web typecheck/lint and contract
checks. The three failing checks are `go-tiers`, `audit-tests` and
`audit-coverage`: R0 still must add the S1 `internal/nativeprofiles/**` glob
and the four **ESSENTIAL** Go test rows specified above. The reported
`release-check-run` job **115099172009** has the same missing ownership cause.
A read-only in-memory application of that glob clears all five ownership
errors. `plan.json` and `plan.md` explicitly reserve both inventories for R0;
P4a's exclusive write set remains unchanged. This handoff is **not CI green**.

Fix-round 4 (2026-10-13): baseline `02e5ef46` reproduces the same R0 omissions.
`go-static` job **115106235132** rejects exactly the four manifest rows above;
`release-check-run` job **115106235117** rejects exactly the five unassigned
profile files. The aggregate Go job reports successful Go tests and failed static
checks. Read-only proposals pass both full-tree ownership and strict tier
validation. Feeding only the proposed S1 glob to the delta CLI through stdin
changes its exit from 1 to 0 with zero changed files and no ownership errors;
neither shared inventory was edited. The exact Go rows are now provided above
for R0 to apply together with that glob.

Both affected Go packages pass through the guarded remote runner on `02e5ef46`.
The affected selector exits 0 and selects **FULL** (3225 reverse-dependency
cases). Guarded merged-main static returns **exit 1**: **39 checks pass, zero
optional skips**, and only `go-tiers`, `audit-tests` and `audit-coverage` fail on
the R0 omissions. The updated document passes the public-name guard, its four
rows match the existing tests, and Git whitespace checks pass. Web unit/browser
tests remain inapplicable to this slice. The policy preview separately awaits
the coordinator's trusted review verdict. This round changes only this owned
document; CI remains blocked by R0's exclusive metadata ownership and the
coordinator's review. No origin push, deployment, model call or review gate was
performed.

Fix-round 5 (2026-10-13): the complete logs for `go-static` job
**115114897307** reject exactly the four **ESSENTIAL** rows above;
`release-check-run` job **115114897389** reports the same two audit failures
and five unassigned profile files. Baseline `d47f561b` reproduces the full-tree
ownership errors. Its delta CLI exits 1 even with zero changed files, because
ownership must cover the entire tree. Supplying the proposed S1 glob through
stdin makes that CLI exit 0 with no errors; the same read-only proposal passes
full-tree ownership. Neither shared inventory was edited.

At validation start, the fetched `origin/main` at `828bf8697` was already an
ancestor of this branch. A final fetch advances main to `06929d1a4`; the read-only
merge-tree check exits 0 with no conflicts, so this round requires no main merge.
The central R0 ownership fix is still in open PR **#623**, which changes only
`scripts/audit/slices.json`. After that fix reaches main, R0 must also apply the
four test-tier rows above in this PR. Both inventories remain outside P4a's
exclusive write set under `plan.json` and `plan.md`.

The guarded remote run passes `internal/nativeprofiles` on `d47f561b`. The first
full CLI run exited 1; its full-package JSON diagnostic rerun passes without
source or assertion changes. The affected selector exits 0 and selects **FULL**
(3225 reverse-dependency cases). Web unit/browser tests remain inapplicable to
this non-browser slice. This round changes only this owned document and does
not change production behavior; the existing four behavior tests remain intact.

Guarded `ci-static-locked.sh --merge-main` on `d47f561b` returns **exit 1**:
**39 checks pass with zero optional skips**. Only `go-tiers`, `audit-tests` and
`audit-coverage` fail, on the exact missing R0 registrations above. This includes
passing test-tier/web-shard regressions, web typecheck/lint, Go vet, public-name,
contract and migration checks. The updated document also passes the public-name
and Git whitespace guards. CI remains **not green**; R0 must finish the same-PR
metadata work before the coordinator reruns CI and its release gates. No origin
push, deployment, model call or review gate was performed.

Fix-round 6 (2026-10-13): complete CI logs for baseline `7dc61d60` confirm
that `go-static` job **115122536493** rejects exactly the four existing Go
tests listed above; `release-check-run` job **115122536485** fails its two
audit assertions solely on the five unassigned profile files. The aggregate
failures follow those jobs. The non-required policy preview separately lacks
the coordinator's trusted review verdict; this worker does not run that gate.

A read-only baseline diagnostic reproduces all five ownership errors. Adding
only the proposed S1 glob in memory clears full-tree ownership and changes the
delta CLI exit from 1 to 0, including its zero-changed-files case. Each proposed
**ESSENTIAL** row matches an existing test on the baseline and is absent from
its manifest. Neither shared inventory was changed. No behavior finding was
supplied, and source code and test assertions remain unchanged.

Fetched `origin/main` at `06929d1a4` is already an ancestor of the branch, and
PR #615 is mergeable. Central R0 audit PR **#623** remains open and changes
only `scripts/audit/slices.json`. Both that file and the Go tier manifest are
explicitly reserved for R0 by the normative plan. R0 must apply the S1 glob
and four **ESSENTIAL** rows before this PR can pass CI.

Both affected Go packages pass through the guarded remote runner on
`7dc61d60`. The affected selector exits 0 and selects **FULL** (3233
reverse-dependency cases). Web unit and browser tests remain inapplicable to
this non-browser slice. Fake-test acceptance remains **PASS** for exact account
binding, **PASS** for default/dedicated child environments, **PASS** for exclusion
of credential/settings sharing, **PASS** for refusal without account rotation,
and **PASS** for retained credentials, unchanged parent environment and distinct
overlapping child profiles. Live account qualification remains G_ACCOUNTS.

Guarded `ci-static-remote.sh` runs the required merged-main static check and
returns **exit 1**: **39 checks pass with zero optional skips**. Only `go-tiers`,
`audit-tests` and `audit-coverage` fail, on the same missing R0 registrations.
The test-tier and web-shard self-checks, Go vet, web typecheck/lint, contract and
migration checks pass. The updated document passes the public-name and Git
whitespace guards. This is **not a green CI handoff**: both failing inventories
are outside P4a's exclusive write set. Only this owned feature document changed;
no origin push, deployment, model call or review gate was performed.

Fix-round 7 (2026-10-13): `go-static` job **115132254013** on baseline
`5885d384` fails only the four missing **ESSENTIAL** test registrations listed
above. All seven Go test shards, web unit/browser jobs, release checks and
migration compatibility pass in that CI run. R0 audit PR **#623** is merged;
the source ownership failure is resolved by its S2 `internal/native*/**` glob.
Fetched `origin/main` at `3507e3319` is already included, and PR #615 is mergeable.
The non-required policy preview separately awaits the coordinator's trusted
review verdict; this worker does not run that gate.

Each rejected test exists on `5885d384` and is absent from its Go tier manifest.
A read-only diagnostic using the manifest plus those source-verified tests
reproduces exactly the four strict-validation errors. Adding only the proposed
rows in memory passes strict validation; this diagnostic does not replace native
collection. The manifest remains unchanged. Both normative plan files reserve
it for R0, so those same-PR registrations remain outside P4a's write set.

Both affected Go packages pass through the guarded remote runner on `5885d384`.
The affected selector exits 0 and selects **FULL** (3233 reverse-dependency
cases). No web unit or browser spec is affected by this documentation change.
Fake-test acceptance remains **PASS** for exact account binding, **PASS** for
default/dedicated child environments, **PASS** for exclusion of credential and
settings sharing, **PASS** for refusal without account rotation, and **PASS**
for retained credentials, unchanged parent environment and distinct overlapping
children. Live account qualification remains G_ACCOUNTS. No behavior fix or
regression test is needed for this metadata-only CI finding; source and test
assertions remain unchanged.

Guarded `ci-static-remote.sh` runs the required merged-main static check and
returns **exit 1**: **41 checks pass with zero optional skips**. Only `go-tiers`
fails, on the four missing R0 rows. Both `audit-tests` and `audit-coverage` now
pass, along with test-tier/web-shard self-checks, Go vet, web typecheck/lint,
contract and migration checks. The updated document passes the public-name,
Git whitespace and write-scope checks. This is **not a green CI handoff**: R0
must complete the Go manifest registration in this PR before merge. Only this
owned feature document changed; no origin push, deployment, model call or review
gate was performed.
