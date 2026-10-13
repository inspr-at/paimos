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

R0 must apply these registrations in the same PR before merge (this builder does
not own the shared metadata files): add `internal/nativeprofiles/**` to audit
slice **S1 security-core**; the two CLI files are already covered by the existing
`internal/cli/**` slice. Register all four named Go tests above as **ESSENTIAL**
in `scripts/ci/go-test-tiers.json` for `internal/nativeprofiles` (three tests) and
`internal/cli` (one test). No web shard, route, permission, migration, DSAR or
shared-fence rows are needed: this slice adds none of those surfaces.

Required validation is the guarded remote Go run for
`./internal/nativeprofiles/... ./internal/cli/...`, guarded
`ci-static-locked.sh --merge-main`, and
`node scripts/test-tiers/affected.mjs --base origin/main`. A busy/held/incomplete
exit 3 is an explicit incomplete handoff, never a passing result; PR CI and the
coordinator's cross-family release gates remain authoritative.
