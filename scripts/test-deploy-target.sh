#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck disable=SC1091
source "$ROOT/scripts/deploy-target.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/_deploy-lib.sh"

TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/paimos-deploy-target.XXXXXX")
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  echo "test-deploy-target: $*" >&2
  exit 1
}

setup_repo() {
  local name="$1"
  local base="$TMP_ROOT/$name"
  local origin="$base/origin.git"
  local repo="$base/repo"

  mkdir -p "$base"
  git init -q --bare "$origin"
  git init -q "$repo"
  (
    cd "$repo"
    git config user.email "ci@example.invalid"
    git config user.name "CI"
    printf 'one\n' > app.txt
    git add app.txt
    git commit -q -m "initial"
    git tag -a v1.0.0 -m "v1.0.0"
    git remote add origin "$origin"
    git push -q origin HEAD:main --tags
  )
  printf '%s\n' "$repo"
}

test_default_release_at_tagged_head() {
  local repo
  repo=$(setup_repo tagged-head)
  (
    cd "$repo"
    deploy_target::resolve ""
    [[ "$DEPLOY_TARGET_TAG" == "1.0.0" ]] || fail "expected 1.0.0, got $DEPLOY_TARGET_TAG"
    [[ "$DEPLOY_TARGET_REASON" == "default-release" ]] || fail "expected default-release, got $DEPLOY_TARGET_REASON"
    [[ "$DEPLOY_TARGET_AHEAD_COUNT" == "0" ]] || fail "expected ahead=0, got $DEPLOY_TARGET_AHEAD_COUNT"
  )
}

test_refuses_omitted_target_when_head_is_ahead() {
  local repo err
  repo=$(setup_repo ahead-head)
  err="$TMP_ROOT/ahead.err"
  (
    cd "$repo"
    printf 'two\n' >> app.txt
    git add app.txt
    git commit -q -m "main change"
    if deploy_target::resolve "" 2>"$err"; then
      fail "expected omitted target to fail when HEAD is ahead"
    fi
  )
  grep -q "deploy target omitted" "$err" || fail "missing omitted-target error"
  grep -q "latest release tag v1.0.0" "$err" || fail "missing latest-tag context"
  grep -q "scripts/deploy.sh <instance> current" "$err" || fail "missing current shortcut"
}

test_explicit_targets_are_preserved() {
  local repo
  repo=$(setup_repo explicit)
  (
    cd "$repo"
    deploy_target::resolve "v1.0.0"
    [[ "$DEPLOY_TARGET_TAG" == "1.0.0" ]] || fail "expected v-prefix stripped, got $DEPLOY_TARGET_TAG"
    [[ "$DEPLOY_TARGET_REASON" == "explicit" ]] || fail "expected explicit, got $DEPLOY_TARGET_REASON"

    deploy_target::resolve "sha-deadbee"
    [[ "$DEPLOY_TARGET_TAG" == "sha-deadbee" ]] || fail "expected sha-deadbee, got $DEPLOY_TARGET_TAG"
    [[ "$DEPLOY_TARGET_REASON" == "explicit" ]] || fail "expected explicit sha, got $DEPLOY_TARGET_REASON"
  )
}

test_current_resolves_to_head_sha_tag() {
  local repo expected
  repo=$(setup_repo current)
  (
    cd "$repo"
    expected="sha-$(git rev-parse --short HEAD)"
    deploy_target::resolve "current"
    [[ "$DEPLOY_TARGET_TAG" == "$expected" ]] || fail "expected $expected, got $DEPLOY_TARGET_TAG"
    [[ "$DEPLOY_TARGET_REASON" == "current-head" ]] || fail "expected current-head, got $DEPLOY_TARGET_REASON"
  )
}

test_calendar_release_targets_are_exact() {
  local repo
  repo=$(setup_repo calendar)
  (
    cd "$repo"
    git tag -d v1.0.0 >/dev/null
    git push -q origin :refs/tags/v1.0.0
    git tag -a v26.08.31 -m v26.08.31
    git push -q origin v26.08.31
    deploy_target::resolve ""
    [[ "$DEPLOY_TARGET_TAG" == "26.08.31" ]] || fail "calendar default lost leading zeroes"
    deploy_target::resolve v26.08.31
    [[ "$DEPLOY_TARGET_TAG" == "26.08.31" ]] || fail "calendar explicit target drifted"
    deploy::health_version_matches_target 26.08.31 26.08.31 || fail "calendar health match rejected"
    ! deploy::health_version_matches_target 26.08.31 26.8.31 || fail "calendar health mismatch accepted"
    if deploy_target::resolve 6.0.0 >/dev/null 2>&1; then
      fail "prohibited 6.0.0 deploy target accepted"
    fi
  )
}

test_default_release_at_tagged_head
test_refuses_omitted_target_when_head_is_ahead
test_explicit_targets_are_preserved
test_current_resolves_to_head_sha_tag
test_calendar_release_targets_are_exact

echo "test-deploy-target: ok"
