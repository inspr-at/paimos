#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck disable=SC1091
source "$ROOT/scripts/release-version.sh"

fail() {
  echo "test-release-version: $*" >&2
  exit 1
}

expect_supported() {
  release_version::is_supported "$1" || fail "expected supported version: $1"
}

expect_rejected() {
  ! release_version::is_supported "$1" || fail "expected rejected version: $1"
}

expect_supported 5.21.1
expect_supported 26.08.31
expect_supported v26.08.31.23.59
expect_rejected 6.0.0
expect_rejected 26.8.31
expect_rejected 26.08.32
expect_rejected 26.02.29
expect_rejected 26.08.31.24.00
expect_rejected 26.08.31.12.60
expect_rejected 26.08.31.12
expect_rejected 26.08.31-dev

[[ "$(release_version::kind 5.21.1)" == semver ]] || fail "old-line SemVer kind drifted"
[[ "$(release_version::kind 26.08.31)" == calendar ]] || fail "calendar kind drifted"

today=$(release_version::vienna_date)
release_version::calendar_recut_policy "$today" $'v5.21.1' || fail "first calendar cut rejected"
! release_version::calendar_recut_policy "$today" $'v5.21.1\nv'"$today" || fail "duplicate unsuffixed cut accepted"
release_version::calendar_recut_policy "$today.14.05" $'v5.21.1\nv'"$today" || fail "same-day recut rejected"
! release_version::calendar_recut_policy "$today.14.05" $'v5.21.1' || fail "suffix accepted without prior same-day cut"
! release_version::calendar_recut_policy 99.01.01 $'v5.21.1' || fail "non-Vienna-day calendar cut accepted"

filtered=$(printf '%s\n' v5.21.1 v26.08.31 v26.08.31.14.05 v6.0.0 5.21.2 bad | release_version::tag_filter)
[[ "$filtered" == $'v5.21.1\nv26.08.31\nv26.08.31.14.05' ]] || fail "tag filter drifted: $filtered"

workflow="$ROOT/.github/workflows/ci-v2.yml"
grep -qF 'type=raw,value=${{ steps.release-version.outputs.version }},enable=${{ steps.release-version.outputs.calendar }}' "$workflow" ||
  fail "workflow lacks exact raw calendar Docker tag"
[[ $(grep -cF 'type=semver,pattern=' "$workflow") -eq 3 ]] || fail "SemVer alias set drifted"
[[ $(grep -cF 'enable=${{ steps.release-version.outputs.semver }}' "$workflow") -eq 3 ]] ||
  fail "calendar tags can still derive mutable SemVer aliases"
[[ $(grep -cF 'release_version::is_supported "$version"' "$ROOT/.github/workflows/release-v2.yml") -eq 2 ]] ||
  fail "CLI release workflow does not reject unsupported release tags on every build platform"
grep -qF '(.metadata.container.tags | all(test("^sha-[0-9a-f]+$")))' "$ROOT/scripts/ghcr-prune.sh" ||
  fail "GHCR prune no longer limits tagged deletion candidates to sha-only images"

echo "test-release-version: ok"
