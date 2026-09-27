# Release

Aeon uses INSPR Calendar Versioning v2 (`inspr-calendar-v2`). The coordinate is `YYMMDDhhmmss.0.0`: two-digit year, month, day, hour, minute, and second in UTC, then `.0.0`. It is SemVer-shaped and fixed width. `version.json` is the only source of that coordinate. The fields that matter for a release are `version_scheme`, `version`, `release_channel`, and `release_sequence`.

The git tag is `v` plus the `version` field, for example `v260926064658.0.0`. `scripts/release-tag.mjs` checks that a pushed tag has that shape. `scripts/verify-release.mjs` checks that `version.json` matches the scheme and that the vendored calendar presentation bundle under `web/src/vendor/calendar-version-display` matches `scripts/calendar-version-bundle-pin.json`. `just release-check` runs the verifier. A production web build runs the same check before it emits assets.

Development builds leave the linker version at `dev`. A release build sets:

```
-X github.com/inspr-at/paimos/internal/version.Version=<version>
```

with `CGO_ENABLED=0` and `-trimpath`. The server image uses the same linker setting (`Dockerfile`).

## Workflow

A push of a `v*` tag runs `.github/workflows/release.yml`.

1. Check out the repository with tags, so release history can see earlier coordinates.
2. Validate the tag and run `scripts/verify-release.mjs --release`. Fail if `version.json` disagrees with the tag.
3. Refuse a coordinate whose GitHub release already exists. Build `paimos-agentd` and `aeon-cli` for the platforms below.
4. Generate the release-history manifest embedded in the server image. That file is produced at release time. It is not committed.
5. Run the image smoke gate. Publishing waits for it.
6. Refuse a coordinate whose GHCR image tag already exists, including a tag left by a partial earlier run. Push the image to `ghcr.io/inspr-at/aeon:<version>`. There is no `latest` tag.
7. Create the GitHub release once with the CLI, `paimos-agentd`, and `SHA256SUMS`. Existing releases are never uploaded to or overwritten. The notes name the image and its digest.

## Image smoke gate

`scripts/smoke-image.sh` builds the release image and exercises it before anything is published. It checks the pinned Chromium and tini packages, their licenses, and `NOTICE`. It then starts a disposable Postgres and the server with mounted secret files, and checks startup, the database role, UID 65532, health, and headers. The script's dev mode is only for authenticated upload and quote calls. Live OIDC is not part of the gate, because the database is disposable and has no identity provider.

The gate needs Docker. It is a release check, not the day-to-day `just test` run.

## Release assets

| Asset | Where |
| --- | --- |
| Server image | `ghcr.io/inspr-at/aeon:<version>`, linux/amd64, provenance enabled |
| `aeon-cli-darwin-arm64`, `aeon-cli-darwin-amd64`, `aeon-cli-linux-amd64`, `aeon-cli-linux-arm64` | GitHub release for the `v` tag. Install the file as `aeon`; a symlink named `paimos` selects paimos mode. |
| `paimos-agentd-darwin-arm64`, `paimos-agentd-darwin-amd64`, `paimos-agentd-linux-arm64`, `paimos-agentd-linux-amd64` | Same GitHub release. The current Nix package is named `aeon-agentd` and builds `bin/aeon-agentd`. Both names come from `cmd/aeon-agentd`. |
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
