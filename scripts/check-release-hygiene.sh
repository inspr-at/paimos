#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
# shellcheck disable=SC1091
source "$ROOT/scripts/release-version.sh"

fail=0

version=$(tr -d '[:space:]' < VERSION)
if ! release_version::is_supported "$version"; then
  echo "release hygiene: VERSION is not supported SemVer or yy.mm.dd[.hh.mm]: $version" >&2
  fail=1
fi

if ! grep -qF "<code>v$version</code>" README.md; then
  echo "release hygiene: README badge does not match VERSION ($version)" >&2
  fail=1
fi

if grep -Eq '"version": "[0-9]+\.[0-9]+\.[0-9]+"' README.md; then
  echo "release hygiene: README health example contains a concrete version; use <VERSION>" >&2
  fail=1
fi

if grep -qF "TODO fill in before committing" docs/CHANGELOG.md; then
  echo "release hygiene: CHANGELOG contains the release TODO stub" >&2
  fail=1
fi

if ! "$ROOT/scripts/check-knowledge-freshness.sh"; then
  fail=1
fi

if ! "$ROOT/scripts/check-branding-hygiene.sh"; then
  fail=1
fi

if grep -qF "paimos-site" scripts/release-doc-sync.sh docs/DEPLOY.md Justfile; then
  echo "release hygiene: doc-sync still points at the retired paimos-site repo" >&2
  fail=1
fi

if ! grep -qF ')/inspr-at"' scripts/release-doc-sync.sh; then
  echo "release hygiene: doc-sync does not inspect the canonical inspr-site checkout" >&2
  fail=1
fi

if ! grep -qF 'just marketing-captures' scripts/release-doc-sync.sh || \
   ! grep -qF 'marketing-captures site=' Justfile; then
  echo "release hygiene: doc-sync does not expose the one-command capture refresh" >&2
  fail=1
fi

if grep -R -qF '/Users/' scripts/marketing; then
  echo "release hygiene: marketing capture tooling contains a workstation-specific path" >&2
  fail=1
fi

if grep -qF 'Claim (paimos.com' docs/claim-matrix.md; then
  echo "release hygiene: claim matrix still names the retired public-site surface" >&2
  fail=1
fi

# Every release-v2 binary phase must use the same explicit inventory. This
# catches a daemon added to one platform but omitted from signing/notarization.
release_binary_loop='for binary in paimos paimos-mcp paimos-agentd; do'
if [[ $(grep -cF "$release_binary_loop" .github/workflows/release-v2.yml) -ne 3 ]]; then
  echo "release hygiene: release-v2 must build/sign exactly paimos, paimos-mcp, and paimos-agentd in all three binary loops" >&2
  fail=1
fi
for artifact in paimos-agentd_darwin_universal.tar.gz paimos-agentd_linux_amd64.tar.gz paimos-agentd_linux_arm64.tar.gz; do
  if ! grep -qF "$artifact" docs/RELEASE.md; then
    echo "release hygiene: docs/RELEASE.md omits $artifact" >&2
    fail=1
  fi
done

# The release workflow archives one bare binary. Proprietary Claude SDK bytes,
# license copies, and generated manifests must never enter this AGPL artifact.
for forbidden in backend/agentd/claudeassets/sdk.mjs backend/agentd/claudeassets/LICENSE.txt backend/agentd/claudeassets/manifest.json; do
  if [[ -e "$forbidden" ]]; then
    echo "release hygiene: forbidden Claude vendor asset is present: $forbidden" >&2
    fail=1
  fi
done
if grep -Eq 'Claude Agent SDK.*(embedded|bundled)|SDK JavaScript and license are embedded' docs/INSTALL.md docs/RELEASE.md docs/AGENT_INTEGRATION.md; then
  echo "release hygiene: documentation falsely claims the Claude Agent SDK ships in paimos-agentd" >&2
  fail=1
fi
if [[ $(grep -c 'tar czf' .github/workflows/release-v2.yml) -ne 2 ]] ||
   [[ $(grep -cE -- '-C .*?("\$\{binary\}"|"\$binary")$' .github/workflows/release-v2.yml) -ne 2 ]]; then
  echo "release hygiene: release tar commands must archive exactly the selected bare binary" >&2
  fail=1
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi

echo "release hygiene: ok"
