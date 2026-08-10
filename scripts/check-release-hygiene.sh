#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

fail=0

version=$(tr -d '[:space:]' < VERSION)
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "release hygiene: VERSION is not semver x.y.z: $version" >&2
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

if grep -qF "paimos-site" scripts/release-doc-sync.sh docs/DEPLOY.md Justfile; then
  echo "release hygiene: doc-sync still points at the retired paimos-site repo" >&2
  fail=1
fi

if ! grep -qF ')/inspr-at"' scripts/release-doc-sync.sh; then
  echo "release hygiene: doc-sync does not inspect the canonical inspr-site checkout" >&2
  fail=1
fi

if grep -qF 'Claim (paimos.com' docs/claim-matrix.md; then
  echo "release hygiene: claim matrix still names the retired public-site surface" >&2
  fail=1
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi

echo "release hygiene: ok"
