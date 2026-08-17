# PAIMOS — Release & Trust Evidence

This document describes what every PAIMOS tag publishes, where the
artefacts live, and how an operator can verify them before deploying.

## What a tag publishes

When CI runs against a `v*` tag, **two workflows** fire in parallel:
[`ci-v2.yml`](../.github/workflows/ci-v2.yml)
produces the container image and supply-chain evidence,
[`release-v2.yml`](../.github/workflows/release-v2.yml)
(PAI-99) produces the signed CLI binaries. Either can fail without
blocking the other; both must succeed for a release to be considered
fully published.

### Container image (`ci-v2.yml`)

1. **Image** — `ghcr.io/inspr-at/paimos:<x.y.z>` (immutable per
   tag) plus `:<x>.<y>` and `:<x>` moving aliases. The same digest is
   also tagged `sha-<short>` for SHA-pinned deploys.
2. **CycloneDX SBOMs** (PAI-121) — uploaded as a release artifact
   named `sbom-v<x.y.z>` containing `backend.sbom.json` and
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

The `paimos` CLI and the `paimos-mcp` MCP server are built for three
platforms and attached to the GitHub Release as tarballs:

| Artifact (versioned) | Alias (unversioned) | Signed? |
|---|---|---|
| `paimos_<x.y.z>_darwin_universal.tar.gz` | `paimos_darwin_universal.tar.gz` | ✅ Developer ID + notarized |
| `paimos_<x.y.z>_linux_amd64.tar.gz` | `paimos_linux_amd64.tar.gz` | — |
| `paimos_<x.y.z>_linux_arm64.tar.gz` | `paimos_linux_arm64.tar.gz` | — |
| `paimos-mcp_<x.y.z>_darwin_universal.tar.gz` | `paimos-mcp_darwin_universal.tar.gz` | ✅ |
| `paimos-mcp_<x.y.z>_linux_amd64.tar.gz` | `paimos-mcp_linux_amd64.tar.gz` | — |
| `paimos-mcp_<x.y.z>_linux_arm64.tar.gz` | `paimos-mcp_linux_arm64.tar.gz` | — |
| `sha256sums.txt` — versioned filenames only | — | — |

The unversioned aliases let `releases/latest/download/<name>` work in
the install one-liner without a "look up the latest tag first"
round-trip. Bytes are identical to the versioned form, so the sums
file lists only the versioned names.

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

    just verify-release v<x.y.z>

That wraps [`scripts/verify-release.sh`](../scripts/verify-release.sh) and
checks the image signature, SBOM attestations, GitHub provenance
attestation, and claim matrix. It requires `cosign`, `gh`, and `jq`
locally. The manual commands below are the same evidence surface broken out
for inspection.

### Container image

Verify the signature (replace `<x.y.z>` with the tag you're pulling):

    cosign verify ghcr.io/inspr-at/paimos:<x.y.z> \
      --certificate-identity-regexp '^https://github.com/inspr-at/paimos/.+' \
      --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

Pull the SBOM attestation:

    cosign download attestation \
      --predicate-type 'https://cyclonedx.org/bom' \
      ghcr.io/inspr-at/paimos:<x.y.z> | \
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

    curl -fLO https://github.com/inspr-at/paimos/releases/download/v<x.y.z>/sha256sums.txt
    shasum -a 256 -c sha256sums.txt --ignore-missing

## Generating SBOMs locally

`just sbom` (or `scripts/sbom.sh`) regenerates both SBOMs into
`dist/sbom/`. Useful when reviewing dependency exposure before cutting,
or when a downstream auditor asks for a snapshot.

## Cutting a release

Pick patch / minor / major; the script handles the VERSION bump, README
badge, CHANGELOG date, release commit with DCO sign-off, protected PR, auto-merge,
exact merge-commit tag, and the wait for `ghcr.io/.../<ver>` to appear:

    just release patch
    just release minor
    just release <x.y.z>      # explicit override (e.g., for post-rc cuts)

The script never pushes `main` or uses a ruleset bypass. It creates or reuses
`release/v<x.y.z>`, opens one PR against `main`, enables protected squash
auto-merge, and tags the merge commit returned for that PR. If another change
lands on `main` later, it is not accidentally included in the release tag.

For agent / non-TTY runs, the reviewed CHANGELOG entry for the new version
must already exist (the script refuses to commit its generated TODO stub).
Starting from clean, current `main`, add only the `## [<x.y.z>]` section to
[`docs/CHANGELOG.md`](CHANGELOG.md), leave that one file uncommitted, then run:

    ./scripts/release.sh patch --no-edit
    # or the explicit form, e.g. when the latest tag is an -rc pre-release:
    ./scripts/release.sh <x.y.z> --no-edit

That reviewed working-tree change moves onto the release branch before the
other deterministic release files are updated. Interactive runs start clean,
create the release branch first, and open `$EDITOR` on the generated draft.

Rerunning the same explicit version is safe: a matching open PR, a merged but
untagged PR, and an already-correct tag resume from their last checkpoint.
Branch/file/PR/tag drift fails closed. If `main` advances while checks run,
the script merges it into the release branch with a DCO sign-off and lets the
required checks rerun; it does not ask GitHub to synthesize an unsigned update.
The accepted PR-head OID is pinned throughout that wait, its four file changes
are checked against the deterministic transformations, and the final squash
tree must match it exactly. Local commit/tag signing configuration is disabled
for these DCO commits and the annotated tag; CI signs the published artifacts.

After the tag is pushed, both workflows run in parallel — total
wall-clock is typically 8–15 minutes (Apple's notarytool dominates the
darwin job). `scripts/release.sh` waits for both tag workflows to succeed
before it prints deploy commands. If you need to resume that wait manually:

    just wait-release-ci v<x.y.z>

## Background

PAI-121 closed the audit's call for "SBOM · CycloneDX manifest of every
dependency, published with each release", and the trust posture for the
"Self-hostable" / "Open API" claims. PAI-124 follows on with the rest of
the evidence-and-repeatability layer (provenance, regression gates,
incident-response drills). PAI-99 (v3.2.4) added the signed CLI release
pipeline so external users have a one-liner install path on macOS
without the Gatekeeper-quarantine dance.
