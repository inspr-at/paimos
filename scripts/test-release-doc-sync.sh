#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/paimos-doc-sync-test.XXXXXX")
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  echo "test-release-doc-sync: $*" >&2
  exit 1
}

new_fixture() {
  local name="$1" fixture
  fixture="$TMP_ROOT/$name"
  mkdir -p "$fixture/repo/scripts" "$fixture/bin" "$fixture/state"
  cp "$ROOT/scripts/release-doc-sync.sh" "$fixture/repo/scripts/"
  cp "$ROOT/scripts/release-version.sh" "$fixture/repo/scripts/"

  cat > "$fixture/bin/git" <<'GIT'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  tag)
    cat "${DOC_SYNC_TEST_STATE:?}/tags"
    ;;
  rev-parse)
    grep -Fxq "${2:-}" "$DOC_SYNC_TEST_STATE/tags"
    ;;
  diff)
    printf '%s\n' backend/example.go docs/example.md
    ;;
  log)
    printf '%s\n' '0000000 test release change'
    ;;
  *)
    echo "unexpected git invocation: $*" >&2
    exit 97
    ;;
esac
GIT

  cat > "$fixture/bin/paimos" <<'PAIMOS'
#!/usr/bin/env bash
set -euo pipefail

state=${DOC_SYNC_TEST_STATE:?}
if [[ "${1:-}" == "--json" ]]; then
  shift
fi
[[ "${1:-}" == "issue" ]] || exit 98
action=${2:-}
shift 2

case "$action" in
  list)
    if [[ -f "$state/existing" ]]; then
      printf '%s\n' '{"issues":[{"title":"Doc/site sync follow-up (rolling)","issue_key":"PAI-777"}]}'
    else
      printf '%s\n' '{"issues":[]}'
    fi
    ;;
  get)
    [[ "${1:-}" == "PAI-777" && -f "$state/existing" ]] || exit 1
    jq -n \
      --rawfile description "$state/description" \
      --rawfile title "$state/title" \
      '{description: $description, title: ($title | rtrimstr("\n"))}'
    ;;
  update)
    [[ "${1:-}" == "PAI-777" ]] || exit 99
    shift
    title=""
    description_file=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --title) title=$2; shift 2 ;;
        --description-file) description_file=$2; shift 2 ;;
        *) echo "unexpected update argument: $1" >&2; exit 99 ;;
      esac
    done
    [[ -n "$title" && -n "$description_file" ]]
    printf '%s\n' "$title" > "$state/title"
    cp "$description_file" "$state/description"
    count=0
    [[ ! -f "$state/update-count" ]] || count=$(<"$state/update-count")
    printf '%s\n' "$((count + 1))" > "$state/update-count"
    ;;
  create)
    description_file=""
    title=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --description-file) description_file=$2; shift 2 ;;
        --title) title=$2; shift 2 ;;
        -p|--type|--priority) shift 2 ;;
        *) echo "unexpected create argument: $1" >&2; exit 99 ;;
      esac
    done
    [[ -n "$description_file" && -n "$title" ]]
    cp "$description_file" "$state/created-description"
    cp "$description_file" "$state/description"
    printf '%s\n' "$title" > "$state/title"
    touch "$state/existing"
    count=0
    [[ ! -f "$state/create-count" ]] || count=$(<"$state/create-count")
    printf '%s\n' "$((count + 1))" > "$state/create-count"
    printf '%s\n' '{"issue_key":"PAI-999"}'
    ;;
  *)
    echo "unexpected paimos action: $action" >&2
    exit 98
    ;;
esac
PAIMOS

  chmod +x "$fixture/bin/git" "$fixture/bin/paimos" "$fixture/repo/scripts/"*.sh
  printf '%s\n' "$fixture"
}

run_sync() {
  local fixture="$1"
  shift
  DOC_SYNC_TEST_STATE="$fixture/state" \
    PATH="$fixture/bin:$PATH" \
    "$fixture/repo/scripts/release-doc-sync.sh" "$@"
}

assert_contains() {
  local haystack="$1" needle="$2"
  [[ "$haystack" == *"$needle"* ]] || fail "missing expected output: $needle"
}

test_calendar_previous_tag_does_not_sigpipe() {
  local fixture output i
  fixture=$(new_fixture calendar-sigpipe)
  {
    printf '%s\n' v26.09.02 v26.09.01.17.31
    # Keep the producer writing well past the pipe buffer after awk finds the
    # previous tag. The old `awk ... exit` lookup deterministically returns
    # SIGPIPE/141 under the script's `set -o pipefail`.
    for ((i = 50000; i >= 1; i--)); do
      printf 'v1.0.%d\n' "$i"
    done
  } > "$fixture/state/tags"

  output=$(run_sync "$fixture" --dry-run v26.09.02) ||
    fail "calendar doc-sync exited before drafting"
  assert_contains "$output" "Diff range: v26.09.01.17.31..v26.09.02"
  assert_contains "$output" "## Doc/site sync follow-up — v26.09.02"
}

test_semver_and_default_tag_selection() {
  local fixture explicit latest
  fixture=$(new_fixture semver)
  printf '%s\n' ignored-bookmark v5.21.1 v5.21.0 v4.9.9 > "$fixture/state/tags"

  explicit=$(run_sync "$fixture" --dry-run v5.21.1) || fail "SemVer doc-sync failed"
  assert_contains "$explicit" "Diff range: v5.21.0..v5.21.1"

  latest=$(run_sync "$fixture" --dry-run) || fail "default-tag doc-sync failed"
  assert_contains "$latest" "Using latest release tag: v5.21.1"
  assert_contains "$latest" "Diff range: v5.21.0..v5.21.1"
}

test_exact_tag_validation() {
  local fixture
  fixture=$(new_fixture invalid-tags)
  printf '%s\n' v26.09.02 v26.02.29 v5.21.1 > "$fixture/state/tags"

  if run_sync "$fixture" --dry-run v26.02.29 >/dev/null 2>&1; then
    fail "invalid calendar date was accepted"
  fi
  if run_sync "$fixture" --dry-run v5.21.0 >/dev/null 2>&1; then
    fail "missing exact release tag was accepted"
  fi
}

seed_rolling_ticket() {
  local fixture="$1"
  touch "$fixture/state/existing"
  printf '%s\n' 'Doc/site sync follow-up (rolling — latest v26.09.01)' > "$fixture/state/title"
  printf '%s\n' \
    '## Doc/site sync follow-up — v26.09.01' \
    '' \
    'Existing release notes.' > "$fixture/state/description"
}

test_rolling_reuse_and_idempotency() {
  local fixture first second
  fixture=$(new_fixture rolling)
  printf '%s\n' v26.09.02 v26.09.01 v5.21.1 > "$fixture/state/tags"
  seed_rolling_ticket "$fixture"

  first=$(run_sync "$fixture" --yes --no-edit v26.09.02) || fail "rolling append failed"
  assert_contains "$first" "Appended v26.09.02 to PAI-777"
  [[ "$(<"$fixture/state/update-count")" == 1 ]] || fail "rolling ticket was not updated once"
  grep -Fqx '## Doc/site sync follow-up — v26.09.02' "$fixture/state/description" ||
    fail "new release section was not appended"
  [[ "$(<"$fixture/state/title")" == 'Doc/site sync follow-up (rolling — latest v26.09.02)' ]] ||
    fail "rolling title did not advance to the newer calendar tag"

  second=$(run_sync "$fixture" --yes --no-edit v26.09.02) || fail "idempotent rerun failed"
  assert_contains "$second" "already covers v26.09.02"
  [[ "$(<"$fixture/state/update-count")" == 1 ]] || fail "idempotent rerun wrote again"
  [[ ! -f "$fixture/state/create-count" ]] || fail "rolling reuse created a duplicate ticket"

  run_sync "$fixture" --yes --no-edit v5.21.1 >/dev/null || fail "out-of-order append failed"
  [[ "$(<"$fixture/state/title")" == 'Doc/site sync follow-up (rolling — latest v26.09.02)' ]] ||
    fail "older backfill regressed the rolling latest title"
}

test_create_then_reuse_is_single_ticket() {
  local fixture first second
  fixture=$(new_fixture create-once)
  printf '%s\n' v26.09.02 v26.09.01 > "$fixture/state/tags"

  first=$(run_sync "$fixture" --yes --no-edit v26.09.02) || fail "initial ticket create failed"
  assert_contains "$first" "Filed PAI-999"
  [[ "$(<"$fixture/state/create-count")" == 1 ]] || fail "initial run did not create exactly once"

  second=$(run_sync "$fixture" --yes --no-edit v26.09.02) || fail "created-ticket rerun failed"
  assert_contains "$second" "already covers v26.09.02"
  [[ "$(<"$fixture/state/create-count")" == 1 ]] || fail "rerun created a duplicate ticket"
  [[ ! -f "$fixture/state/update-count" ]] || fail "rerun rewrote an already-covered ticket"
}

test_empty_existing_description_fails_closed() {
  local fixture
  fixture=$(new_fixture empty-existing)
  printf '%s\n' v26.09.02 v26.09.01 > "$fixture/state/tags"
  seed_rolling_ticket "$fixture"
  : > "$fixture/state/description"

  if run_sync "$fixture" --yes --no-edit v26.09.02 >/dev/null 2>&1; then
    fail "empty rolling description was overwritten"
  fi
  [[ ! -f "$fixture/state/update-count" ]] || fail "failed-closed lookup still updated"
  [[ ! -f "$fixture/state/create-count" ]] || fail "failed-closed lookup created a duplicate"
}

test_calendar_previous_tag_does_not_sigpipe
test_semver_and_default_tag_selection
test_exact_tag_validation
test_rolling_reuse_and_idempotency
test_create_then_reuse_is_single_ticket
test_empty_existing_description_fails_closed

echo "test-release-doc-sync: ok"
