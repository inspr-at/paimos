# PAIMOS — Release & Trust Evidence

This document describes what every PAIMOS tag publishes, where the
artefacts live, and how an operator can verify them before deploying.

## What a tag publishes

When CI runs against a `v*` tag, **two tag workflows** fire in parallel:
[`ci-v2.yml`](../.github/workflows/ci-v2.yml)
produces the container image and supply-chain evidence,
[`release-v2.yml`](../.github/workflows/release-v2.yml)
(PAI-99) produces the signed CLI binaries. They execute independently; both
must succeed for a release to be fully published. Before creating the tag, the
release script requires a successful [`backend-full.yml`](../.github/workflows/backend-full.yml)
run for the exact protected-main merge. For main pushes changing only
`frontend/src/`, `frontend/public/`, `README.md`, `VERSION`, `docs/CHANGELOG.md`
or `docs/INSTALL.md`, it may reuse an ancestral successful hosted run whose
serial/platform and all three broad-race jobs actually executed successfully.
The entire diff is checked; skipped/reused suites cannot form evidence chains.
The source commit and run are recorded in the workflow summary. Unknown paths,
backend/build/dependency/test-policy changes or missing evidence run the full
suites. Nightly, manual and explicit PR runs always execute them. This keeps
frontend iterations short without changing publication smoke, frontend checks,
signatures or deployment controls. The first policy change itself requires a
new full baseline; it does not shortcut a release already in progress.
The short `TestAgentIntercom` documentation contracts still run at the current
head because those backend tests read the allowlisted README/INSTALL files.

The full serial/platform and broad-race jobs run in parallel and are not duplicated on the
identical tag commit. Applying the explicit `backend-full-evidence` label is the
supported way to obtain hosted exhaustive evidence for an exact PR head; normal
PR events and unrelated labels do not authorize those jobs.

### Container image (`ci-v2.yml`)

1. **Image** — `ghcr.io/inspr-at/paimos:<release-version>` (immutable per
   tag). Legacy SemVer releases also publish `:<x>.<y>` and `:<x>` moving
   aliases; calendar releases (v1 and v2) publish no mutable numeric aliases. The digest is
   also tagged `sha-<short>` for SHA-pinned deploys.
2. **CycloneDX SBOMs** (PAI-121) — uploaded as a release artifact
   named `sbom-v<release-version>` containing `backend.sbom.json` and
   `frontend.sbom.json`. These describe every Go module and every npm
   package that ended up in the image, including transitive
   dependencies and resolved licenses.
3. **Sigstore signatures + SBOM attestations** (PAI-121) — `cosign
   sign` binds the image manifest digest to a keyless signature backed
   by GitHub's OIDC token; `cosign attest` attaches each SBOM as a
   verifiable attestation against the same digest. No long-lived
   signing key is stored anywhere — the workflow's OIDC token is the
   only thing that can produce a signature for that digest.

### CLI binaries (`release-v2.yml` — PAI-99)

The `paimos` CLI, `paimos-mcp` MCP server, and operator-local
`paimos-agentd` process supervisor are built for three platforms and attached
to the GitHub Release as tarballs:

| Artifact (versioned) | Alias (unversioned) | Signed? |
|---|---|---|
| `paimos_<release-version>_darwin_universal.tar.gz` | `paimos_darwin_universal.tar.gz` | ✅ Developer ID + notarized |
| `paimos_<release-version>_linux_amd64.tar.gz` | `paimos_linux_amd64.tar.gz` | — |
| `paimos_<release-version>_linux_arm64.tar.gz` | `paimos_linux_arm64.tar.gz` | — |
| `paimos-mcp_<release-version>_darwin_universal.tar.gz` | `paimos-mcp_darwin_universal.tar.gz` | ✅ |
| `paimos-mcp_<release-version>_linux_amd64.tar.gz` | `paimos-mcp_linux_amd64.tar.gz` | — |
| `paimos-mcp_<release-version>_linux_arm64.tar.gz` | `paimos-mcp_linux_arm64.tar.gz` | — |
| `paimos-agentd_<release-version>_darwin_universal.tar.gz` | `paimos-agentd_darwin_universal.tar.gz` | ✅ |
| `paimos-agentd_<release-version>_linux_amd64.tar.gz` | `paimos-agentd_linux_amd64.tar.gz` | — |
| `paimos-agentd_<release-version>_linux_arm64.tar.gz` | `paimos-agentd_linux_arm64.tar.gz` | — |
| `sha256sums.txt` — versioned filenames only | — | — |

The unversioned aliases let `releases/latest/download/<name>` work in
the install one-liner without a "look up the latest tag first"
round-trip. Bytes are identical to the versioned form, so the sums
file lists only the versioned names.

Each `paimos-agentd` tarball contains exactly one bare binary. It includes the
AGPL PAIMOS bridge code, but no Anthropic SDK, Anthropic license copy, runtime
manifest, Node.js, or Claude CLI. Claude owned sessions therefore require three
operator-provided local dependencies: Node.js 18+, Claude CLI 2.1.251+ with an
existing operator login, and exact Agent SDK 0.3.251 configured by absolute
`sdk.mjs` path. Startup validates the SDK version and SHA-256 and never fetches
packages, credentials, or private daemon state.

**macOS signing** uses a Developer ID Application certificate held in
a personal Apple Developer account ("Developer ID Application: Markus
Barta (P66J39QV6V)"). Codesign sets the hardened runtime + a secure
timestamp; `xcrun notarytool submit --wait` ships each binary to Apple
for notarization. The ticket lives on Apple's servers (stapler can't
bind to bare Mach-O executables) — Gatekeeper fetches it on first run.

**Pre-release tags** (anything containing a hyphen, e.g. `v3.2.4-rc1`)
are auto-marked `prerelease: true` and don't take over
`/releases/latest/`.

`main` builds keep the previous behaviour: container image + `latest`
tag, no SBOM, no signature, no CLI binaries.

## How to verify a release

The short path is:

    just verify-release v<release-version>

That wraps [`scripts/verify-release.sh`](../scripts/verify-release.sh) and
checks the image signature, SBOM attestations, GitHub provenance
attestation, and claim matrix. It requires `cosign`, `gh`, and `jq`
locally. The manual commands below are the same evidence surface broken out
for inspection.

### Container image

Verify the signature (replace `<release-version>` with the tag you're pulling):

    cosign verify ghcr.io/inspr-at/paimos:<release-version> \
      --certificate-identity-regexp '^https://github.com/inspr-at/paimos/.+' \
      --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

Pull the SBOM attestation:

    cosign download attestation \
      --predicate-type 'https://cyclonedx.org/bom' \
      ghcr.io/inspr-at/paimos:<release-version> | \
      jq -r '.payload | @base64d | fromjson | .predicate'

The decoded predicate is the same CycloneDX JSON that lives next to
the GitHub release artifact, so an operator who pulls only by digest
gets the bill of materials directly off the registry.

### CLI binary (macOS)

After downloading the darwin universal tarball, confirm the signature
chain and the notarization ticket:

    codesign --display --verbose=2 paimos        # shows the cert chain
    codesign --test-requirement="=notarized" \
             --verify --verbose=2 paimos         # explicit requirement satisfied → notarized

The expected `Authority` line is `Developer ID Application: Markus
Barta (P66J39QV6V)` followed by Apple's intermediate and root CAs.

Verify the SHA-256 against the published sums file:

    curl -fLO https://github.com/inspr-at/paimos/releases/download/v<release-version>/sha256sums.txt
    shasum -a 256 -c sha256sums.txt --ignore-missing

## Generating SBOMs locally

`just sbom` (or `scripts/sbom.sh`) regenerates both SBOMs into
`dist/sbom/`. Useful when reviewing dependency exposure before cutting,
or when a downstream auditor asks for a snapshot.

## Cutting a release

A product cut reserves an INSPR calendar v2 coordinate (PAI-979 / INSPR-395):
`YYMMDDhhmmss.0.0`, the current UTC second as the SemVer MAJOR segment with
MINOR and PATCH fixed at `0.0`. `now` reserves it at the moment the script
starts; an explicit coordinate is accepted when it was reserved earlier the
same UTC day (for example by an external-stage publication that must pin the
next release tag), is later than every published v2 coordinate, and is not in
the future. Legacy `patch|minor|major` and `yy.mm.dd[.hh.mm]` cuts are closed
once the first v2 coordinate exists; the migration anchor lives in
[`scripts/release/version-scheme.json`](../scripts/release/version-scheme.json).
The script handles the VERSION update, README badge, CHANGELOG date, release
commit with DCO sign-off, protected PR, auto-merge, exact merge-commit tag, and
the wait for `ghcr.io/.../<ver>` to appear:

    just release now
    just release <YYMMDDhhmmss.0.0>  # coordinate reserved earlier today (UTC)

The script never pushes `main` or uses a ruleset bypass. It creates or reuses
`release/v<release-version>`, opens one PR against `main`, enables protected squash
auto-merge, and tags the merge commit returned for that PR. If another change
lands on `main` later, it is not accidentally included in the release tag.

For agent / non-TTY runs, the reviewed CHANGELOG content must already exist
(the script refuses to commit its generated TODO stub). When current `main`
starts with exactly one canonical `## [Unreleased]` section, the script
consumes that section in place as `## [<release-version>]` and preserves every older
release byte-for-byte. Otherwise, starting from clean, current `main`, add only
the `## [<release-version>]` section to [`docs/CHANGELOG.md`](CHANGELOG.md), leave that
one file uncommitted, then run:

    ./scripts/release.sh now --no-edit
    # or the explicit, already-reserved coordinate:
    ./scripts/release.sh <YYMMDDhhmmss.0.0> --no-edit

That reviewed working-tree change moves onto the release branch before the
other deterministic release files are updated. Interactive runs start clean,
create the release branch first, and open `$EDITOR` on the generated draft.
The canonical leading `Unreleased` path is already reviewed and is consumed
deterministically without reopening the editor. A duplicate active section or
a versioned entry that leaves the leading `Unreleased` section behind fails
before a release PR can be created; historical changelog bytes are preserved.

Rerunning the same explicit version is safe: a matching open PR, a merged but
untagged PR, and an already-correct tag resume from their last checkpoint.
Branch/file/PR/tag drift fails closed. If `main` advances while checks run,
the script merges it into the release branch with a DCO sign-off and lets the
required checks rerun; it does not ask GitHub to synthesize an unsigned update.
The accepted PR-head OID is pinned throughout that wait, its four file changes
are checked against the deterministic transformations, and the final squash
tree must match it exactly. Local commit/tag signing configuration is disabled
for these DCO commits and the annotated tag; CI signs the published artifacts.

### Audited missing-provenance recovery

The normal path still requires GitHub's protected squash auto-merge receipt. A
merged release PR whose `autoMergeRequest` is missing remains blocked unless a
separate reviewed change has committed an exact, value-free receipt at
`scripts/release/recovery/v<release-version>.json` on current `origin/main`. The receipt
pins the version, PR number, approved head, squash merge, and incident reason.

Recovery is deliberately one-shot and fail-closed. `release.sh` accepts the
receipt only when its exact schema and values match live GitHub PR JSON, the
merge is a one-parent ancestor of current main with the approved head's exact
tree, every required check on that head is successful or explicitly skipped,
and the tag is still absent. An untracked, dirty, stale, or mismatched receipt
has no authority. If normal auto-merge provenance exists, this exceptional path
is not consulted.

After the tag is pushed, both artifact workflows run in parallel and typically
take 8–15 minutes (Apple's notarytool dominates the darwin job).
`scripts/release.sh` waits for both before it prints deploy commands. The tag's
`ci` workflow reuses the exhaustive result required for the identical
protected-main head before tag creation; no new tag copy of the serial suite is
started. If you need to resume the release-evidence wait manually:

    just wait-release-ci v<release-version>

## Background

PAI-121 closed the audit's call for "SBOM · CycloneDX manifest of every
dependency, published with each release", and the trust posture for the
"Self-hostable" / "Open API" claims. PAI-124 follows on with the rest of
the evidence-and-repeatability layer (provenance, regression gates,
incident-response drills). PAI-99 (v3.2.4) added the signed CLI release
pipeline so external users have a one-liner install path on macOS
without the Gatekeeper-quarantine dance.
