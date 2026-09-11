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

# INSPR calendar v2 (PAI-979): UTC YYMMDDhhmmss.0.0, SemVer-syntactic, fixed width.
expect_supported 260909113550.0.0
expect_supported v260909113550.0.0
expect_supported 261231235959.0.0
expect_supported 280229120000.0.0
expect_rejected 260229120000.0.0
expect_rejected 260431120000.0.0
expect_rejected 260909240000.0.0
expect_rejected 260909116000.0.0
expect_rejected 260909113560.0.0
expect_rejected 2609091135.0.0
expect_rejected 20260909113550.0.0
expect_rejected 260909113550
expect_rejected 260909113550.0
expect_rejected 260909113550.0.1
expect_rejected 260909113550.1.0
expect_rejected 260909113550.0.0-rc1
expect_rejected 260909113550.0.0+g39d0b59
expect_rejected 090909113550.0.0
expect_rejected 260909113550.00.0

[[ "$(release_version::kind 5.21.1)" == semver ]] || fail "old-line SemVer kind drifted"
[[ "$(release_version::kind 26.08.31)" == calendar ]] || fail "calendar kind drifted"
[[ "$(release_version::kind 260909113550.0.0)" == calendar-v2 ]] || fail "calendar v2 kind drifted"
release_version::is_any_calendar 260909113550.0.0 || fail "v2 is not classified as a calendar era"
release_version::is_any_calendar 26.08.31 || fail "v1 is not classified as a calendar era"
! release_version::is_any_calendar 5.21.1 || fail "legacy SemVer classified as calendar"
[[ "$(release_version::calendar_v2_iso_date 260909113550.0.0)" == 2026-09-09 ]] || fail "v2 iso date drifted"
[[ "$(release_version::calendar_v2_day v261231235959.0.0)" == 261231 ]] || fail "v2 day extraction drifted"

# v2 reservation policy: strictly later than every published v2 coordinate,
# today's UTC day, never in the future; an exact published tag is a resume.
utc_now=$(release_version::utc_coordinate)
utc_day=$(release_version::utc_date_compact)
release_version::calendar_v2_reservation_policy "$utc_now" $'v5.21.1\nv26.09.09' || fail "first v2 reservation rejected"
release_version::calendar_v2_reservation_policy "$utc_now" $'v26.09.09\nv'"$utc_now" || fail "exact published v2 resume rejected"
! release_version::calendar_v2_reservation_policy "$utc_now" $'v26.09.09\nv991231235959.0.0' || fail "v2 coordinate accepted behind a later published coordinate"
! release_version::calendar_v2_reservation_policy 991231235959.0.0 $'v26.09.09' || fail "future v2 coordinate accepted"
! release_version::calendar_v2_reservation_policy "${utc_day}000000.0.0" $'v26.09.09\nv'"${utc_day}000001.0.0" || fail "earlier same-day v2 coordinate accepted"
release_version::calendar_v2_reservation_policy 100101000000.0.0 $'v5.21.1\nv100101000000.0.0' || fail "published historic v2 resume rejected"
release_version::has_calendar_v2_tag $'v5.21.1\nv26.09.09\nv260909113550.0.0' || fail "v2 tag not detected"
! release_version::has_calendar_v2_tag $'v5.21.1\nv26.09.09' || fail "v2 tag detected where none exists"

today=$(release_version::vienna_date)
release_version::calendar_recut_policy "$today" $'v5.21.1' || fail "first calendar cut rejected"
release_version::calendar_recut_policy "$today" $'v5.21.1\nv'"$today" || fail "exact unsuffixed resume rejected"
release_version::calendar_recut_policy "$today.14.05" $'v5.21.1\nv'"$today" || fail "same-day recut rejected"
! release_version::calendar_recut_policy "$today.14.05" $'v5.21.1' || fail "suffix accepted without prior same-day cut"
! release_version::calendar_recut_policy 99.01.01 $'v5.21.1' || fail "non-Vienna-day calendar cut accepted"
release_version::calendar_recut_policy 99.01.01 $'v5.21.1\nv99.01.01' || fail "published unsuffixed resume rejected after cut day"

filtered=$(printf '%s\n' v5.21.1 v26.08.31 v26.08.31.14.05 v260909113550.0.0 v6.0.0 5.21.2 bad | release_version::tag_filter)
[[ "$filtered" == $'v5.21.1\nv26.08.31\nv26.08.31.14.05\nv260909113550.0.0' ]] || fail "tag filter drifted: $filtered"

workflow="$ROOT/.github/workflows/ci-v2.yml"
grep -qF 'type=raw,value=${{ steps.release-version.outputs.version }},enable=${{ steps.release-version.outputs.calendar }}' "$workflow" ||
  fail "workflow lacks exact raw calendar Docker tag"
grep -qF '[[ "$kind" = calendar || "$kind" = calendar-v2 ]]' "$workflow" ||
  fail "workflow does not publish the exact raw tag for calendar v2 releases"
[[ $(grep -cF 'type=semver,pattern=' "$workflow") -eq 3 ]] || fail "SemVer alias set drifted"
[[ $(grep -cF 'enable=${{ steps.release-version.outputs.semver }}' "$workflow") -eq 3 ]] ||
  fail "calendar tags can still derive mutable SemVer aliases"
[[ $(grep -cF 'release_version::is_supported "$version"' "$ROOT/.github/workflows/release-v2.yml") -eq 2 ]] ||
  fail "CLI release workflow does not reject unsupported release tags on every build platform"
grep -qF '(.metadata.container.tags | all(test("^sha-[0-9a-f]+$")))' "$ROOT/scripts/ghcr-prune.sh" ||
  fail "GHCR prune no longer limits tagged deletion candidates to sha-only images"

echo "test-release-version: ok"
