#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Snapshot before Node runs; validate Playwright selection against those inputs.
set -euo pipefail

readonly repo="$(git rev-parse --show-toplevel)"
readonly snapshot="$RUNNER_TEMP/aeon-web-selection"
cd "$repo"

if [ "${1:-}" = snapshot ]; then
  mkdir -p "$snapshot"
  printf '%s\n' "$GITHUB_EVENT_NAME" > "$snapshot/event"
  git show-ref --head > "$snapshot/refs"
  if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
    # Only the immutable event base is accepted, including PRs into other branches.
    [[ "${PR_BASE_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid PR base SHA' >&2; exit 1; }
    test "$(git rev-parse --verify "$PR_BASE_SHA^{commit}")" = "$PR_BASE_SHA"
    printf '%s\n' "$PR_BASE_SHA" > "$snapshot/base"
    git diff --no-renames --name-only -z "$PR_BASE_SHA" -- > "$snapshot/paths"
  fi
  exit 0
fi
test "${1:-}" = run
test "$(cat "$snapshot/event")" = "$GITHUB_EVENT_NAME"
readonly original_refs="$(cat "$snapshot/refs")"
args=(-c playwright.ui.config.ts --workers=2 --retries=0)
require_tests=1
if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
  readonly base_sha="$(cat "$snapshot/base")"
  [[ "$base_sha" =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid saved PR base SHA' >&2; exit 1; }
  args+=("--only-changed=$base_sha")
  require_tests=0
  while IFS= read -r -d '' path; do
    case "$path" in
      web/*|*.spec.ts|*.spec.js|*.spec.mts|*.spec.mjs) require_tests=1 ;;
    esac
  done < "$snapshot/paths"
fi
readonly require_tests

check_inputs() {
  test "$(git show-ref --head)" = "$original_refs" || { echo 'Git refs changed during UI checks' >&2; return 1; }
  if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
    test "$(git rev-parse --verify "$base_sha^{commit}")" = "$base_sha"
    cmp "$snapshot/paths" <(git diff --no-renames --name-only -z "$base_sha" --) || {
      echo 'Changed paths changed during UI checks' >&2; return 1;
    }
  fi
}
check_inputs
cd "$repo/web"
# Non-PR events, especially merge_group, run every UI spec without a path filter.
list="$snapshot/list"
if npx playwright test "${args[@]}" --list --reporter=list > "$list" 2>&1; then
  check_inputs
else
  cat "$list" >&2
  exit 1
fi
{
  echo '### UI specs selected for this change'
  echo
  echo '```text'
  cat "$list"
  echo '```'
} >> "$GITHUB_STEP_SUMMARY"
cat "$list"
total="$(sed -nE 's/^Total: ([0-9]+) tests? in [0-9]+ files?$/\1/p' "$list")"
[[ "$total" =~ ^[0-9]+$ ]] || { echo 'Missing or invalid Playwright list total' >&2; exit 1; }
if [ "$require_tests" -eq 1 ] && [ "$total" -eq 0 ]; then
  echo 'UI/spec changes or full-suite events require a non-empty test selection' >&2
  exit 1
fi
if [ "$total" -eq 0 ]; then
  echo 'No UI/spec paths changed; empty selection accepted.' >> "$GITHUB_STEP_SUMMARY"
  exit 0
fi
report="$snapshot/result.json"
if npx playwright test "${args[@]}" --reporter=json > "$report"; then
  check_inputs
else
  cat "$report" >&2
  exit 1
fi
# An exit-0 during test loading must not masquerade as a completed browser run.
node - "$report" "$total" <<'JS'
const fs = require('node:fs');
const result = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const total = Number(process.argv[3]);
const stats = result.stats;
if (!stats || !Number.isInteger(stats.expected) || !Number.isInteger(stats.skipped) ||
    stats.expected <= 0 || stats.skipped < 0 || stats.expected + stats.skipped !== total ||
    stats.unexpected !== 0 || stats.flaky !== 0 || !Array.isArray(result.errors) || result.errors.length !== 0) {
  throw new Error('Playwright did not complete the selected tests successfully');
}
console.log(`Completed UI tests: ${stats.expected} passed, ${stats.skipped} skipped; selected ${total}`);
JS
