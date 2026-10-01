#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Select files before Node runs; validate against runner-sealed inputs.
set -euo pipefail

readonly repo="$(git rev-parse --show-toplevel)"
readonly snapshot="$RUNNER_TEMP/aeon-web-selection"
cd "$repo"

hash() { shasum -a 256 | cut -d ' ' -f 1; }

if [ "${1:-}" = snapshot ]; then
  mkdir -p "$snapshot"
  base_sha=
  paths_hash=
  require_tests=1
  if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
    # Only the immutable event base is accepted, including PRs into other branches.
    [[ "${PR_BASE_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid PR base SHA' >&2; exit 1; }
    test "$(git rev-parse --verify "$PR_BASE_SHA^{commit}")" = "$PR_BASE_SHA"
    base_sha="$PR_BASE_SHA"
    git diff --no-renames --name-only -z "$PR_BASE_SHA" -- > "$snapshot/paths"
    paths_hash="$(hash < "$snapshot/paths")"
    require_tests=0
    while IFS= read -r -d '' path; do
      case "$path" in
        web/*|*.spec.ts|*.spec.js|*.spec.mts|*.spec.mjs|scripts/ci-web-*|.github/workflows/ci.yml) require_tests=1 ;;
      esac
    done < "$snapshot/paths"
  fi
  # Never let Playwright query a diff after loading repository JavaScript.
  # Spec-only PRs select those files; shared UI inputs conservatively select all.
  selected_specs="$(python3 -I - "$snapshot/paths" "$GITHUB_EVENT_NAME" <<'PY'
import json, subprocess, sys
from pathlib import Path
tracked = subprocess.check_output(['git', 'ls-files', '-z']).decode().split('\0')
specs = sorted(p for p in tracked if p.startswith('web/tests/') and p.endswith('.spec.ts'))
if sys.argv[2] == 'pull_request':
    changed = Path(sys.argv[1]).read_bytes().decode().split('\0')
    relevant = [p for p in changed if p.startswith('web/') or '.spec.' in p
                or p.startswith('scripts/ci-web-') or p == '.github/workflows/ci.yml']
    # Deleting a spec also runs all survivors, so an empty selection fails closed.
    if all(p in specs for p in relevant):
        specs = sorted(set(relevant))
print(json.dumps(specs, ensure_ascii=True, separators=(',', ':')))
PY
)"
  if [ "$selected_specs" != '[]' ]; then require_tests=1; fi
  # The runner seals these step outputs before any repository Node code runs.
  # The scratch directory is never a source of trusted selection inputs in run.
  {
    printf 'event=%s\n' "$GITHUB_EVENT_NAME"
    printf 'base_sha=%s\n' "$base_sha"
    printf 'refs_hash=%s\n' "$(git show-ref --head | hash)"
    printf 'paths_hash=%s\n' "$paths_hash"
    printf 'require_tests=%s\n' "$require_tests"
    printf 'selected_specs=%s\n' "$selected_specs"
    printf 'script_hash=%s\n' "$(hash < scripts/ci-web-tests.sh)"
    printf 'guard_hash=%s\n' "$(hash < scripts/ci-web-exit-guard.cjs)"
  } >> "$GITHUB_OUTPUT"
  exit 0
fi
test "${1:-}" = run
test "${SNAPSHOT_EVENT:-}" = "$GITHUB_EVENT_NAME"
[[ "${SNAPSHOT_REFS_HASH:-}" =~ ^[0-9a-f]{64}$ ]] || { echo 'Invalid saved Git refs hash' >&2; exit 1; }
[[ "${SNAPSHOT_REQUIRE_TESTS:-}" =~ ^[01]$ ]] || { echo 'Invalid saved coverage requirement' >&2; exit 1; }
readonly original_refs_hash="$SNAPSHOT_REFS_HASH"
readonly require_tests="$SNAPSHOT_REQUIRE_TESTS"
readonly original_script_hash="${SNAPSHOT_SCRIPT_HASH:-}"
readonly original_guard_hash="${SNAPSHOT_GUARD_HASH:-}"
for digest in "$original_script_hash" "$original_guard_hash"; do
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || { echo 'Invalid saved CI script hash' >&2; exit 1; }
done
test "$(hash < scripts/ci-web-tests.sh)" = "$original_script_hash" || { echo 'CI test script changed after snapshot' >&2; exit 1; }
test "$(hash < scripts/ci-web-exit-guard.cjs)" = "$original_guard_hash" || { echo 'CI exit guard changed after snapshot' >&2; exit 1; }
args=(-c playwright.ui.config.ts --workers=2 --retries=0)
if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
  readonly base_sha="${SNAPSHOT_BASE_SHA:-}"
  [[ "$base_sha" =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid saved PR base SHA' >&2; exit 1; }
  [[ "${SNAPSHOT_PATHS_HASH:-}" =~ ^[0-9a-f]{64}$ ]] || { echo 'Invalid saved changed paths hash' >&2; exit 1; }
  readonly original_paths_hash="$SNAPSHOT_PATHS_HASH"
else
  test "$require_tests" = 1
fi

check_inputs() {
  test "$(git show-ref --head | hash)" = "$original_refs_hash" || { echo 'Git refs changed during UI checks' >&2; return 1; }
  if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
    test "$(git rev-parse --verify "$base_sha^{commit}")" = "$base_sha"
    test "$(git diff --no-renames --name-only -z "$base_sha" -- | hash)" = "$original_paths_hash" || {
      echo 'Changed paths changed during UI checks' >&2; return 1;
    }
  fi
}
check_inputs
mkdir -p "$snapshot"
# JSON is a one-line sealed output; turn each literal path into an anchored
# Playwright file regex, preserving spaces, metacharacters and embedded newlines.
python3 -I - "$repo" "${SNAPSHOT_SELECTED_SPECS:-}" > "$snapshot/args" <<'PY'
import json, re, sys
paths = json.loads(sys.argv[2])
if not isinstance(paths, list) or any(not isinstance(p, str) or
        not p.startswith('web/tests/') or not p.endswith('.spec.ts') or
        any(part in ('.', '..') for part in p.split('/')) or '\0' in p for p in paths):
    raise ValueError('Invalid saved spec paths')
for path in paths:
    sys.stdout.buffer.write(('^' + re.escape(sys.argv[1] + '/' + path) + '$').encode() + b'\0')
PY
if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
  while IFS= read -r -d '' path; do args+=("$path"); done < "$snapshot/args"
fi
if [ "$GITHUB_EVENT_NAME" = pull_request ] && [ "${#args[@]}" -eq 4 ]; then
  if [ "$require_tests" -eq 1 ]; then
    echo 'UI/spec changes or full-suite events require a non-empty test selection' >&2
    exit 1
  fi
  echo 'No UI/spec paths changed; empty selection accepted.' >> "$GITHUB_STEP_SUMMARY"
  exit 0
fi
# Verify the original and the exact bytes required by each Node invocation.
check_guard() {
  test "$(hash < "$repo/scripts/ci-web-exit-guard.cjs")" = "$original_guard_hash" &&
    test "$(hash < "$snapshot/guard.cjs")" = "$original_guard_hash" || {
      echo 'CI exit guard changed after snapshot' >&2; return 1;
    }
}
cp "$repo/scripts/ci-web-exit-guard.cjs" "$snapshot/guard.cjs"
cd "$repo/web"
readonly node_options="--require=\"$snapshot/guard.cjs\""
# Non-PR events, especially merge_group, snapshot every UI spec.
list="$snapshot/list"
check_guard
if AEON_CI_REPO_ROOT="$repo" NODE_OPTIONS="$node_options" npx playwright test "${args[@]}" --list --reporter=list > "$list" 2>&1; then
  check_inputs
  check_guard
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
check_guard
if AEON_CI_REPO_ROOT="$repo" NODE_OPTIONS="$node_options" npx playwright test "${args[@]}" --reporter=json > "$report"; then
  check_inputs
  check_guard
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
