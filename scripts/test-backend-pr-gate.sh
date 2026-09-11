#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SELECTOR="$ROOT/scripts/backend-changed-packages.sh"
CI_SELECTOR="$ROOT/scripts/backend-ci-packages.sh"
TEST_RUNNER="$ROOT/scripts/backend-pr-test.sh"
RACE_RUNNER="$ROOT/scripts/backend-pr-race.sh"
FULL_WAITER="$ROOT/scripts/wait-backend-full.sh"
FULL_AUTHORIZER="$ROOT/scripts/backend-full-authorize.sh"
WORKFLOW="$ROOT/.github/workflows/ci-v2.yml"
FULL_WORKFLOW="$ROOT/.github/workflows/backend-full.yml"
RELEASE_DOC="$ROOT/docs/RELEASE.md"
SELECTION_SENTINEL='PAIMOS_BACKEND_SELECTION_OK_V1'
FULL_WAIT_CALL="wait-backend-full.sh --check \"\$GITHUB_SHA\""
FULL_PR_GUARD="github.event_name != 'pull_request' || github.event.label.name == 'backend-full-evidence'"
FULL_AGGREGATE_GUARD="always() && (github.event_name != 'pull_request' || github.event.label.name == 'backend-full-evidence')"
FULL_LABEL_ENV="BACKEND_FULL_LABEL: \${{ github.event.label.name }}"
FULL_AUTH_RESULT="AUTHORIZATION: \${{ needs.backend-full-authorize.result }}"
FULL_SERIAL_RESULT="SERIAL: \${{ needs.backend-full-serial.result }}"
FULL_RACE_RESULT="RACE: \${{ needs.backend-full-race.result }}"
FULL_AUTH_ASSERT="[[ \"\$AUTHORIZATION\" == 'success' ]]"
FULL_SERIAL_ASSERT="[[ \"\$SERIAL\" == 'success' ]]"
FULL_RACE_ASSERT="[[ \"\$RACE\" == 'success' ]]"
GO_COMMAND=${GO_COMMAND:-go}
FIXTURES="$ROOT/scripts/testdata/backend-gate"
TMP_ROOT=$(mktemp -d)
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  echo "test-backend-pr-gate: $*" >&2
  exit 1
}

python3 "$ROOT/scripts/backend-race-exclusions.py" --check "$ROOT/backend"

select_files() {
  local output first packages
  output=$(printf '%s\n' "$@" | "$SELECTOR" --files-from -)
  first=${output%%$'\n'*}
  [[ "$first" == "$SELECTION_SENTINEL" ]] ||
    fail 'changed-package selector omitted its success sentinel'
  if [[ "$output" == *$'\n'* ]]; then
    packages=${output#*$'\n'}
    [[ -z "$packages" ]] || printf '%s\n' "$packages"
  fi
}

check_selection() {
  local expected="$1"
  shift
  local actual
  actual=$(select_files "$@")
  [[ "$actual" == "$expected" ]] ||
    fail "selection for [$*] was [$actual], want [$expected]"
}

check_selection_contains() {
  local files="$1"
  shift
  local actual file_array=()
  read -r -a file_array <<<"$files"
  actual=$(select_files "${file_array[@]}")
  for expected in "$@"; do
    grep -Fxq "$expected" <<<"$actual" ||
      fail "affected selection for [$files] omitted [$expected]: [$actual]"
  done
}

[[ -x "$SELECTOR" ]] || fail "missing executable changed-package selector: $SELECTOR"
[[ -x "$TEST_RUNNER" ]] || fail "missing executable changed-package test runner: $TEST_RUNNER"
[[ -x "$RACE_RUNNER" ]] || fail "missing executable changed-package race runner: $RACE_RUNNER"
[[ -x "$FULL_WAITER" ]] || fail "missing executable exact-head backend-full waiter: $FULL_WAITER"
[[ -x "$FULL_AUTHORIZER" ]] || fail "missing executable backend-full evidence authorizer: $FULL_AUTHORIZER"

GITHUB_EVENT_NAME=pull_request BACKEND_FULL_LABEL=backend-full-evidence "$FULL_AUTHORIZER" ||
  fail 'backend-full evidence authorizer rejected the stable operator label'
if GITHUB_EVENT_NAME=push "$FULL_AUTHORIZER" >/dev/null 2>&1; then
  fail 'backend-full evidence authorizer accepted a push'
fi
GITHUB_EVENT_NAME=schedule GITHUB_REF=refs/heads/main "$FULL_AUTHORIZER" ||
  fail 'backend-full authorizer rejected nightly main execution'
if GITHUB_EVENT_NAME=schedule GITHUB_REF=refs/heads/other "$FULL_AUTHORIZER" >/dev/null 2>&1; then
  fail 'backend-full authorizer accepted a nightly run outside main'
fi
if GITHUB_EVENT_NAME=pull_request BACKEND_FULL_LABEL=unrelated "$FULL_AUTHORIZER" >/dev/null 2>&1; then
  fail 'backend-full evidence authorizer accepted an unrelated PR label'
fi

fixture_head='1111111111111111111111111111111111111111'
FAKE_HEAD_SHA="$fixture_head" FAKE_BACKEND_FULL_MODE=success \
  GH_COMMAND="$FIXTURES/backend-full-gh.sh" "$FULL_WAITER" --check "$fixture_head" >/dev/null ||
  fail 'exact-head backend-full waiter rejected successful exhaustive evidence'
for mode in failed wrong-head skipped; do
  if FAKE_HEAD_SHA="$fixture_head" FAKE_BACKEND_FULL_MODE="$mode" \
    GH_COMMAND="$FIXTURES/backend-full-gh.sh" "$FULL_WAITER" --check "$fixture_head" >/dev/null 2>&1; then
    fail "exact-head backend-full waiter accepted $mode evidence"
  fi
done

if GIT_COMMAND="$FIXTURES/fail-command.sh" "$SELECTOR" \
  ec235d7cd03a13d06a02727cf55bad7c29bd89c7 HEAD >/dev/null 2>&1; then
  fail 'changed-package selector accepted a failed git diff'
fi
if printf '%s\n' backend/db/db.go | env REAL_GO_COMMAND="$GO_COMMAND" \
  GO_COMMAND="$FIXTURES/fail-go-list-test.sh" "$SELECTOR" --files-from - >/dev/null 2>&1; then
  fail 'changed-package selector accepted a failed go list -test reverse-closure query'
fi
[[ -x "$CI_SELECTOR" ]] || fail "missing executable CI selection adapter: $CI_SELECTOR"
if BACKEND_SELECTOR_COMMAND="$FIXTURES/fail-command.sh" "$CI_SELECTOR" HEAD HEAD >/dev/null 2>&1; then
  fail 'CI selection adapter swallowed a selector failure'
fi
if BACKEND_SELECTOR_COMMAND="$FIXTURES/no-sentinel.sh" "$CI_SELECTOR" HEAD HEAD >/dev/null 2>&1; then
  fail 'CI selection adapter accepted selector output without a success sentinel'
fi

# A detected rename reports only the destination to --name-only. Exercise a
# real cross-package move in an isolated repository: selection must treat it as
# an explicit deletion plus addition, then expand both reverse test closures.
rename_repo="$TMP_ROOT/rename-repo"
mkdir -p "$rename_repo/scripts" "$rename_repo/backend/source" "$rename_repo/backend/destination" \
  "$rename_repo/backend/sourceconsumer" "$rename_repo/backend/destinationconsumer"
cp "$SELECTOR" "$rename_repo/scripts/backend-changed-packages.sh"
printf '%s\n' 'module github.com/inspr-at/paimos/backend' '' 'go 1.26.6' >"$rename_repo/backend/go.mod"
printf '%s\n' 'package source' '' 'func Keep() string { return "source" }' >"$rename_repo/backend/source/keep.go"
printf '%s\n' 'package source' '' '// Moved is intentionally long enough for Git rename detection.' \
  '// Its body stays byte-identical across the cross-package move.' \
  'func Moved() string { return "moved-contract-payload" }' >"$rename_repo/backend/source/moved.go"
printf '%s\n' 'package destination' '' 'func Keep() string { return "destination" }' >"$rename_repo/backend/destination/keep.go"
printf '%s\n' 'package sourceconsumer_test' '' 'import (' '  "testing"' \
  '  "github.com/inspr-at/paimos/backend/source"' ')' '' \
  'func TestSourceContract(t *testing.T) { _ = source.Moved() }' >"$rename_repo/backend/sourceconsumer/sourceconsumer_test.go"
printf '%s\n' 'package destinationconsumer_test' '' 'import (' '  "testing"' \
  '  "github.com/inspr-at/paimos/backend/destination"' ')' '' \
  'func TestDestinationContract(t *testing.T) { _ = destination.Keep() }' >"$rename_repo/backend/destinationconsumer/destinationconsumer_test.go"
git -C "$rename_repo" init -q -b main
git -C "$rename_repo" config user.name 'Backend gate fixture'
git -C "$rename_repo" config user.email 'backend-gate@example.test'
git -C "$rename_repo" add .
git -C "$rename_repo" commit -q -m 'fixture base'
rename_base=$(git -C "$rename_repo" rev-parse HEAD)
git -C "$rename_repo" mv backend/source/moved.go backend/destination/moved.go
sed -i.bak 's/package source/package destination/' "$rename_repo/backend/destination/moved.go"
rm "$rename_repo/backend/destination/moved.go.bak"
git -C "$rename_repo" add .
git -C "$rename_repo" commit -q -m 'move contract across packages'
rename_head=$(git -C "$rename_repo" rev-parse HEAD)
rename_selection=$(GO_COMMAND="$GO_COMMAND" "$rename_repo/scripts/backend-changed-packages.sh" \
  "$rename_base" "$rename_head")
for expected in \
  github.com/inspr-at/paimos/backend/source \
  github.com/inspr-at/paimos/backend/destination \
  github.com/inspr-at/paimos/backend/sourceconsumer \
  github.com/inspr-at/paimos/backend/destinationconsumer
do
  grep -Fxq "$expected" <<<"$rename_selection" ||
    fail "cross-package rename selection omitted $expected: [$rename_selection]"
done

check_selection '' docs/INSTALL.md
check_selection_contains 'backend/supervision/service.go backend/supervision/service_integration_test.go' \
  github.com/inspr-at/paimos/backend/supervision \
  github.com/inspr-at/paimos/backend/cmd/paimos
check_selection_contains 'backend/db/db.go' \
  github.com/inspr-at/paimos/backend/db \
  github.com/inspr-at/paimos/backend/auth \
  github.com/inspr-at/paimos/backend/handlers
db_affected=$(select_files backend/db/db.go)
db_expected=$(printf '%s\n' \
  github.com/inspr-at/paimos/backend \
  github.com/inspr-at/paimos/backend/agentmessage \
  github.com/inspr-at/paimos/backend/agentmode \
  github.com/inspr-at/paimos/backend/auth \
  github.com/inspr-at/paimos/backend/baselinebatch \
  github.com/inspr-at/paimos/backend/cmd/dev-fixture-sql \
  github.com/inspr-at/paimos/backend/cmd/paimos \
  github.com/inspr-at/paimos/backend/cmd/paimos-agentd \
  github.com/inspr-at/paimos/backend/cmd/paimos-mcp \
  github.com/inspr-at/paimos/backend/contracts \
  github.com/inspr-at/paimos/backend/db \
  github.com/inspr-at/paimos/backend/delivery \
  github.com/inspr-at/paimos/backend/externalstage \
  github.com/inspr-at/paimos/backend/flowhost \
  github.com/inspr-at/paimos/backend/handlers \
  github.com/inspr-at/paimos/backend/handlers/crm/http \
  github.com/inspr-at/paimos/backend/handlers/crm/hubspot \
  github.com/inspr-at/paimos/backend/handlers/knowledge \
  github.com/inspr-at/paimos/backend/internal/knowledge857 \
  github.com/inspr-at/paimos/backend/internal/testdb \
  github.com/inspr-at/paimos/backend/lifecycleclient \
  github.com/inspr-at/paimos/backend/lifecycleintents \
  github.com/inspr-at/paimos/backend/managedharness \
  github.com/inspr-at/paimos/backend/releaseacceptance \
  github.com/inspr-at/paimos/backend/supervision \
  github.com/inspr-at/paimos/backend/workerfleet)
[[ "$db_affected" == "$db_expected" ]] ||
  fail "db reverse-dependency closure drifted: [$db_affected]"
# Contracts imports agentmessage from its schema tests, not its production
# package. The reverse test closure must retain that real test-only dependency.
check_selection_contains 'backend/agentmessage/bus.go' \
  github.com/inspr-at/paimos/backend/contracts
check_selection_contains 'backend/lifecycleclient/runner_server_test.go' \
  github.com/inspr-at/paimos/backend/lifecycleclient \
  github.com/inspr-at/paimos/backend/cmd/paimos-agentd
! grep -Fxq 'github.com/inspr-at/paimos/backend/pharoslink' <<<"$db_affected" ||
  fail 'db reverse-dependency closure included an unrelated package'
check_selection_contains 'backend/contracts/fixtures/external-stage/dependency-janus-v1.json' \
  github.com/inspr-at/paimos/backend/contracts \
  github.com/inspr-at/paimos/backend/externalstage
direct_db_raw=$(printf '%s\n' backend/db/db.go | "$SELECTOR" --direct --files-from -)
[[ "${direct_db_raw%%$'\n'*}" == "$SELECTION_SENTINEL" ]] || fail 'direct selector omitted its success sentinel'
direct_db=${direct_db_raw#*$'\n'}
[[ "$direct_db" == 'github.com/inspr-at/paimos/backend/db' ]] ||
  fail "direct changed-package selection unexpectedly expanded: [$direct_db]"
check_selection './...' backend/go.mod
check_selection './...' backend/removed-package/deleted.go
check_selection './...' backend/contracts/fixtures/deleted.go

discover_test_names() {
  local package="$1" match="$2" listed name names=()
  listed=$(cd "$ROOT/backend" && "$GO_COMMAND" test -list "$match" "$package")
  while IFS= read -r name; do
    case "$name" in
      Test*|Fuzz*)
        [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] ||
          fail "unsafe discovered test name in $package: $name"
        names+=("$name")
        ;;
    esac
  done <<<"$listed"
  (( ${#names[@]} > 0 )) || fail "no tests discovered for $package / $match"
  printf '%s\n' "${names[@]}" | LC_ALL=C sort
}

plan_test_names() {
  local plan="$1"
  printf '%s\n' "$plan" | rg -o '(Test|Fuzz)[A-Za-z0-9_]+' | LC_ALL=C sort
}

plan_test_names_ordered() {
  local plan="$1"
  printf '%s\n' "$plan" | rg -o '(Test|Fuzz)[A-Za-z0-9_]+'
}

discover_test_names_ordered() {
  local package="$1" match="$2" listed name names=()
  listed=$(cd "$ROOT/backend" && "$GO_COMMAND" test -list "$match" "$package")
  while IFS= read -r name; do
    case "$name" in
      Test*|Fuzz*)
        [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] ||
          fail "unsafe discovered test name in $package: $name"
        names+=("$name")
        ;;
    esac
  done <<<"$listed"
  (( ${#names[@]} > 0 )) || fail "no tests discovered for $package / $match"
  printf '%s\n' "${names[@]}"
}

assert_plan_covers_discovery_once() {
  local label="$1" plan="$2" package="$3" match="$4" expected actual
  expected=$(discover_test_names "$package" "$match")
  actual=$(plan_test_names "$plan")
  [[ "$actual" == "$expected" ]] ||
    fail "$label plan does not cover every currently discovered safe Test/Fuzz exactly once"
}

if "$TEST_RUNNER" --dry-run --lane=affected \
  github.com/inspr-at/paimos/backend/agentmode \
  github.com/inspr-at/paimos/backend/auth >/dev/null 2>&1; then
  fail 'affected normal lane accepts an unindexed multi-package invocation'
fi
if "$TEST_RUNNER" --dry-run --lane=affected --shard=0/1 \
  github.com/inspr-at/paimos/backend/agentmode >/dev/null 2>&1; then
  fail 'affected normal lane accepts a drifted shard count'
fi
affected_plan=
for shard in 0 1; do
  plan=$("$TEST_RUNNER" --dry-run --lane=affected --shard="$shard/2" \
    github.com/inspr-at/paimos/backend/agentmode \
    github.com/inspr-at/paimos/backend/auth \
    github.com/inspr-at/paimos/backend/db \
    github.com/inspr-at/paimos/backend/handlers)
  [[ -n "$plan" ]] || fail "affected normal shard $shard is empty"
  ! grep -qv '^go test -count=1 -timeout=8m ' <<<"$plan" ||
    fail "affected normal shard $shard lost its eight-minute timeout"
  affected_plan+="$plan"$'\n'
done
[[ "$affected_plan" == *'subscribe\ before\ high-water'* && "$affected_plan" == *'permission\ grant\ and\ revoke'* ]] ||
  fail 'affected normal lane lost non-performance Agent Mode stream contracts'
[[ "$affected_plan" != *'overflow\ lost\ wake\ coalescing\ and\ restart'* && "$affected_plan" != *'./db'* && "$affected_plan" != *'./handlers'* ]] ||
  fail 'affected normal lane duplicates an isolated performance, DB, or handler contract'
[[ "$(printf '%s\n' "$affected_plan" | rg -o 'github.com/inspr-at/paimos/backend/auth' | wc -l | tr -d ' ')" -eq 1 ]] ||
  fail 'affected normal shards omit or duplicate a selected package'

db_plan=$("$TEST_RUNNER" --dry-run --lane=db github.com/inspr-at/paimos/backend/db)
[[ "$(grep -c '^go test -count=1 -timeout=15m ./db -run ' <<<"$db_plan")" -eq 4 ]] ||
  fail 'db package is not split into four normal-test shards'
for shard in 0 1 2 3; do
  shard_line=$(sed -n "$((shard + 1))p" <<<"$db_plan")
  [[ -n "$(plan_test_names "$shard_line")" ]] || fail "DB normal shard $shard is empty"
done
assert_plan_covers_discovery_once 'DB normal' "$db_plan" ./db '^(Test|Fuzz)'

handler_plan=
if "$TEST_RUNNER" --dry-run --lane=handlers github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler normal lane still allows local multi-process execution instead of requiring one matrix shard'
fi
if "$TEST_RUNNER" --dry-run --lane=handlers --shard=0/4 github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler normal lane accepts a drifted shard count'
fi
for shard in 0 1 2 3 4; do
  plan=$("$TEST_RUNNER" --dry-run --lane=handlers --shard="$shard/5" github.com/inspr-at/paimos/backend/handlers)
  [[ "$(grep -c '^go test -count=1 -timeout=15m ./handlers -run ' <<<"$plan")" -eq 1 ]] ||
    fail "handler normal shard $shard does not own exactly one invocation"
  [[ "$(printf '%s\n' "$plan" | rg -o 'Test[A-Za-z0-9_]+' | wc -l | tr -d ' ')" -gt 0 ]] ||
    fail "handler normal shard $shard is empty"
  handler_plan+="$plan"$'\n'
done
assert_plan_covers_discovery_once 'handler normal' "$handler_plan" ./handlers '^(Test|Fuzz)'
if GO_COMMAND="$FIXTURES/unsafe-go-list.sh" "$TEST_RUNNER" --dry-run --lane=handlers \
  --shard=0/5 github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler normal sharder filtered an unsafe discovered test name instead of failing closed'
fi

performance_plan=$("$TEST_RUNNER" --dry-run --lane=performance github.com/inspr-at/paimos/backend/agentmode)
[[ "$performance_plan" == 'go test -count=1 -timeout=8m '* ]] ||
  fail 'performance normal lane lost its eight-minute timeout'
[[ "$performance_plan" == *'overflow\ lost\ wake\ coalescing\ and\ restart'* && "$performance_plan" != *'subscribe\ before\ high-water'* ]] ||
  fail 'isolated normal lane does not exclusively own the unchanged Agent Mode performance contract'

db_race_plan=$("$RACE_RUNNER" --dry-run --lane=db github.com/inspr-at/paimos/backend/db)
[[ "$db_race_plan" == *'./db'* && "$db_race_plan" == *'TestSchemaAgentRunTelemetryTerminalWriteRace'* &&
  "$db_race_plan" == *'TestM147ConcurrentCanonicalCommandsConvergeProductionPool'* &&
  "$db_race_plan" == *'TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwnerProductionPool'* ]] ||
  fail 'db race plan lost its package-local concurrency proof'
[[ "$(grep -c '^go test -race .* ./db -run ' <<<"$db_race_plan")" -eq 3 ]] ||
  fail 'db race plan does not isolate the two production-pool 32-writer arbitration proofs'
[[ "$db_race_plan" != *'./...'* && "$db_race_plan" != *'./handlers'* ]] ||
  fail 'db race plan escaped the changed package'
handler_race_plan=
if "$RACE_RUNNER" --dry-run --lane=handlers github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler race lane still allows local multi-process execution instead of requiring one matrix shard'
fi
if "$RACE_RUNNER" --dry-run --lane=handlers --shard=0/4 github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler race lane accepts a drifted shard count'
fi
HANDLER_RACE_MATCH='^Test.*(Concurrent|Concurrency|Race|Atomic|BatchesReleaseWriter|RacedPoke).*$'
HANDLER_RACE_GROUP_SIZE=4
handler_listed_names=()
while IFS= read -r name; do
  handler_listed_names+=("$name")
done < <(discover_test_names_ordered ./handlers "$HANDLER_RACE_MATCH")
handler_expected_order=
handler_expected_groups=0
for shard in 0 1 2 3 4; do
  shard_names=()
  for ((index = shard; index < ${#handler_listed_names[@]}; index += 5)); do
    shard_names+=("${handler_listed_names[$index]}")
  done
  (( ${#shard_names[@]} > 0 )) || fail "handler race shard $shard is empty"
  shard_groups=$(( (${#shard_names[@]} + HANDLER_RACE_GROUP_SIZE - 1) / HANDLER_RACE_GROUP_SIZE ))
  handler_expected_groups=$((handler_expected_groups + shard_groups))
  plan=$("$RACE_RUNNER" --dry-run --lane=handlers --shard="$shard/5" github.com/inspr-at/paimos/backend/handlers)
  [[ "$(grep -c '^go test -race -count=1 -timeout=8m ./handlers -run ' <<<"$plan")" -eq "$shard_groups" ]] ||
    fail "handler race shard $shard did not retain $shard_groups bounded groups"
  shard_plan=$(grep '^go test -race .* ./handlers -run ' <<<"$plan")
  group_index=0
  while IFS= read -r group_line; do
    group_count=$(plan_test_names_ordered "$group_line" | wc -l | tr -d ' ')
    (( group_count >= 1 && group_count <= HANDLER_RACE_GROUP_SIZE )) ||
      fail "handler shard $shard group $group_index has $group_count tests, want 1-$HANDLER_RACE_GROUP_SIZE"
    group_index=$((group_index + 1))
  done <<<"$shard_plan"
  [[ "$group_index" -eq "$shard_groups" ]] ||
    fail "handler shard $shard emitted $group_index groups, want $shard_groups"
  handler_expected_order+="$(printf '%s\n' "${shard_names[@]}")"$'\n'
  handler_race_plan+="$plan"$'\n'
done
[[ "$handler_race_plan" == *'Concurrent'* && "$handler_race_plan" != *'TestRegression_'* && "$handler_race_plan" != *'TestAuthzFuzz_'* ]] ||
  fail 'handler race plan is not limited to concurrency contracts'
[[ "$(grep -c '^go test -race .* ./handlers -run ' <<<"$handler_race_plan")" -eq "$handler_expected_groups" ]] ||
  fail 'handler concurrency race omitted or duplicated a bounded group'
assert_plan_covers_discovery_once 'handler concurrency race' "$handler_race_plan" ./handlers \
  "$HANDLER_RACE_MATCH"
[[ "$(plan_test_names_ordered "$handler_race_plan")" == "$(printf '%s' "$handler_expected_order")" ]] ||
  fail 'handler grouped race plan changed discovery order, uniqueness, or round-robin assignment'
handler_all_plan=$("$RACE_RUNNER" --dry-run --lane=all github.com/inspr-at/paimos/backend/handlers)
[[ "$(grep -c '^go test -race -count=1 -timeout=8m ./handlers -run ' <<<"$handler_all_plan")" -eq "$handler_expected_groups" ]] ||
  fail "all-lane handler race did not retain all $handler_expected_groups bounded invocations"
assert_plan_covers_discovery_once 'all handler race' "$handler_all_plan" ./handlers "$HANDLER_RACE_MATCH"
if GO_COMMAND="$FIXTURES/unsafe-go-list.sh" "$RACE_RUNNER" --dry-run --lane=handlers \
  --shard=0/5 github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler race sharder filtered an unsafe discovered test name instead of failing closed'
fi

if "$RACE_RUNNER" --dry-run --lane=affected \
  github.com/inspr-at/paimos/backend/agentmode >/dev/null 2>&1; then
  fail 'affected race lane still allows unindexed package execution'
fi
if "$RACE_RUNNER" --dry-run --lane=affected --shard=0/3 \
  github.com/inspr-at/paimos/backend/agentmode >/dev/null 2>&1; then
  fail 'affected race lane accepts a drifted shard count'
fi
affected_race_plan=
for shard in 0 1 2 3; do
  plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
    github.com/inspr-at/paimos/backend/agentmode \
    github.com/inspr-at/paimos/backend/auth \
    github.com/inspr-at/paimos/backend/managedharness \
    github.com/inspr-at/paimos/backend/db \
    github.com/inspr-at/paimos/backend/handlers)
  affected_race_plan+="$plan"$'\n'
done
[[ "$(grep -c '^go test -race .* ./agentmode -run ' <<<"$affected_race_plan")" -eq 2 &&
  "$(grep -c '^go test -race .* ./auth -run ' <<<"$affected_race_plan")" -eq 1 ]] ||
  fail 'affected race package shards omitted or duplicated a selected package plan'
[[ "$affected_race_plan" != *'./managedharness'* && "$affected_race_plan" != *'./db'* &&
  "$affected_race_plan" != *'./handlers'* ]] ||
  fail 'affected race package shards duplicated an isolated DB, handler, or managed-harness lane'
affected_broad_plans=()
affected_broad_plan=
for shard in 0 1 2 3; do
  plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" './...')
  affected_broad_plans+=("$plan")
  affected_broad_plan+="$plan"$'\n'
done
for package in ./cmd/paimos ./supervision ./agentmessage ./agentmode ./agentd ./localjournal ./ownedprocess \
  ./lifecycleclient ./runtimeconsumer ./runtimehealth; do
  owners=0
  for plan in "${affected_broad_plans[@]}"; do
    [[ "$plan" != *" $package"* ]] || owners=$((owners + 1))
  done
  [[ "$owners" -eq 1 ]] ||
    fail "affected ./... race shards assigned $package to $owners runners, want exactly one"
done
[[ "$affected_broad_plan" != *' ./db'* && "$affected_broad_plan" != *' ./handlers'* &&
  "$affected_broad_plan" != *' ./managedharness'* ]] ||
  fail 'affected ./... race shards duplicated an isolated package lane'
[[ "${affected_broad_plans[0]}" != "${affected_broad_plans[1]}" ]] ||
  fail 'affected ./... race matrix emitted identical shard plans'

sequential_state="$TMP_ROOT/sequential-race"
mkdir -p "$sequential_state"
if ! FAKE_GO_STATE="$sequential_state" GO_COMMAND="$FIXTURES/sequential-go.sh" \
  "$RACE_RUNNER" --lane=all github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'full backend handler race shards did not run sequentially'
fi
[[ ! -e "$sequential_state/overlap" && "$(wc -l < "$sequential_state/runs" | tr -d ' ')" -eq 5 ]] ||
  fail 'full backend handler race plan overlapped or omitted an indexed shard'
supervision_default_plan=$(
  "$RACE_RUNNER" --dry-run github.com/inspr-at/paimos/backend/supervision
)
[[ "$supervision_default_plan" == *'-timeout=8m ./supervision'* ]] ||
  fail 'ordinary race plan lost its bounded eight-minute package timeout'
supervision_full_plan=$(
  BACKEND_RACE_PACKAGE_TIMEOUT=15m \
    "$RACE_RUNNER" --dry-run github.com/inspr-at/paimos/backend/supervision
)
[[ "$supervision_full_plan" == *'-timeout=15m ./supervision'* &&
  "$supervision_full_plan" != *'-timeout=8m'* ]] ||
  fail 'exhaustive race plan did not apply its measured package timeout'
for invalid_timeout in 0m 15m30s 16m 600m; do
  invalid_output=
  if invalid_output=$(BACKEND_RACE_PACKAGE_TIMEOUT="$invalid_timeout" \
    "$RACE_RUNNER" --dry-run github.com/inspr-at/paimos/backend/supervision 2>&1); then
    fail "race runner accepted invalid package timeout $invalid_timeout"
  else
    invalid_status=$?
  fi
  [[ "$invalid_status" -eq 2 && "$invalid_output" == backend-pr-race:*timeout*"$invalid_timeout" ]] ||
    fail "race runner did not reject $invalid_timeout with its typed timeout error"
done
agentmode_race_plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard=0/4 github.com/inspr-at/paimos/backend/agentmode)
[[ "$agentmode_race_plan" == *'subscribe\ before\ high-water'* && "$agentmode_race_plan" == *'permission\ grant\ and\ revoke'* ]] ||
  fail 'agentmode race plan lost non-performance stream concurrency subtests'
[[ "$agentmode_race_plan" != *'overflow\ lost\ wake'* ]] ||
  fail 'agentmode race plan includes a latency budget invalid under race instrumentation'
auth_race_plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard=0/4 github.com/inspr-at/paimos/backend/auth)
[[ "$(grep -c '^go test -race .* ./auth -run ' <<<"$auth_race_plan")" -eq 1 &&
  "$auth_race_plan" == *'TestResolveAPIKeyUsageStampNeverInheritsSQLiteBusyTimeout'* ]] ||
  fail 'auth PR race plan lost its package-local SQLite contention proofs'
[[ "$auth_race_plan" != *'TestResolveAPIKeyRecentUsageStaysReadOnlyWhileSQLiteWriterIsBusy'* &&
  "$(grep -Ec '^go test -race -count=1 -timeout=8m \./auth$' <<<"$auth_race_plan")" -eq 0 &&
  "$auth_race_plan" != *'./...'* ]] ||
  fail 'auth PR race plan restored the latency-sensitive or exhaustive package suite'
externalstage_race_plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard=0/4 github.com/inspr-at/paimos/backend/externalstage)
for required_test in \
  TestConcurrentCreateCommitsOneHandoffAndOneReplay \
  TestServiceJanusDependencyIsAtomicAndCannotOwnCanonicalStage \
  TestServiceOwnerLifecycleReplayHeartbeatAndRestart \
  TestServiceReportV2PersistsExplicitReleaseIdentityAndBindsReplay
do
  [[ "$externalstage_race_plan" == *"$required_test"* ]] ||
    fail "external-stage PR race plan lost $required_test"
done
[[ "$(grep -c '^go test -race .* ./externalstage -run ' <<<"$externalstage_race_plan")" -eq 1 &&
  "$(grep -Ec '^go test -race -count=1 -timeout=8m \./externalstage$' <<<"$externalstage_race_plan")" -eq 0 ]] ||
  fail 'external-stage PR race plan restored its exhaustive migration-heavy package suite'
managedharness_race_match='^(TestStoppedSessionCanRegisterNewActiveGeneration|Test.*(Concurrent|Concurrency|Race|Atomic|Replay|Recovers).*)$'
if "$RACE_RUNNER" --dry-run --lane=managedharness \
  github.com/inspr-at/paimos/backend/managedharness >/dev/null 2>&1; then
  fail 'managed-harness race lane still allows unindexed execution'
fi
if "$RACE_RUNNER" --dry-run --lane=managedharness --shard=0/6 \
  github.com/inspr-at/paimos/backend/managedharness >/dev/null 2>&1; then
  fail 'managed-harness race lane accepts a drifted shard count'
fi
managedharness_race_plan=
managedharness_min_oracles=999
managedharness_max_oracles=0
for shard in 0 1 2 3 4 5 6; do
  plan=$("$RACE_RUNNER" --dry-run --lane=managedharness --shard="$shard/7" \
    github.com/inspr-at/paimos/backend/managedharness)
  managedharness_oracles=$(plan_test_names "$plan" | wc -l | tr -d ' ')
  [[ "$(grep -c '^go test -race .* ./managedharness -run ' <<<"$plan")" -eq 1 &&
    "$managedharness_oracles" -ge 1 ]] ||
    fail "managed-harness race shard $shard does not own one nonempty invocation"
  (( managedharness_oracles < managedharness_min_oracles )) &&
    managedharness_min_oracles=$managedharness_oracles
  (( managedharness_oracles > managedharness_max_oracles )) &&
    managedharness_max_oracles=$managedharness_oracles
  managedharness_race_plan+="$plan"$'\n'
done
assert_plan_covers_discovery_once 'managed-harness targeted race' "$managedharness_race_plan" \
  ./managedharness "$managedharness_race_match"
(( managedharness_max_oracles - managedharness_min_oracles <= 1 )) ||
  fail 'managed-harness race oracles are not balanced across seven runners'
[[ "$(grep -Ec '^go test -race -count=1 -timeout=8m \./managedharness$' <<<"$managedharness_race_plan")" -eq 0 &&
  "$managedharness_race_plan" != *'./...'* ]] ||
  fail 'managed-harness PR race plan restored the exhaustive migration-heavy package suite'
# Lifecycle fixtures each rebuild all migrations. Split the complete discovered
# set over the existing four runners without pruning authorization/replay tests.
lifecycle_race_plan=
for shard in 0 1 2 3; do
  plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
    github.com/inspr-at/paimos/backend/agentd \
    github.com/inspr-at/paimos/backend/lifecycleintents \
    github.com/inspr-at/paimos/backend/localjournal)
  [[ "$(grep -c '^go test -race -count=1 -timeout=8m ./lifecycleintents -run ' <<<"$plan")" -eq 1 ]] ||
    fail "lifecycle race shard $shard did not retain one bounded invocation"
  lifecycle_race_plan+="$plan"$'\n'
done
lifecycle_only_plan=$(grep '^go test -race .* ./lifecycleintents -run ' <<<"$lifecycle_race_plan")
assert_plan_covers_discovery_once 'affected lifecycle race' "$lifecycle_only_plan" ./lifecycleintents '^(Test|Fuzz)'
[[ "$(grep -c '^go test -race .* ./agentd$' <<<"$lifecycle_race_plan")" -eq 1 &&
  "$(grep -c '^go test -race .* ./localjournal$' <<<"$lifecycle_race_plan")" -eq 1 ]] ||
  fail 'lifecycle sharding duplicated or omitted another affected package'
lifecycle_first_count=$(plan_test_names "$(sed -n '1p' <<<"$lifecycle_only_plan")" | wc -l)
lifecycle_last_count=$(plan_test_names "$(sed -n '4p' <<<"$lifecycle_only_plan")" | wc -l)
(( lifecycle_first_count >= lifecycle_last_count && lifecycle_first_count - lifecycle_last_count <= 1 )) ||
  fail 'lifecycle test names were not balanced across the four race runners'
for shard in 0 1 2 3; do
  plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
    github.com/inspr-at/paimos/backend/agentd github.com/inspr-at/paimos/backend/localjournal)
  [[ "$plan" != *'./lifecycleintents'* ]] || fail 'unselected lifecycle package leaked into the affected race plan'
done
lifecycle_all_plan=$("$RACE_RUNNER" --dry-run --lane=all github.com/inspr-at/paimos/backend/lifecycleintents)
[[ "$(grep -c '^go test -race -count=1 -timeout=8m ./lifecycleintents -run ' <<<"$lifecycle_all_plan")" -eq 4 ]] ||
  fail 'all-lane lifecycle race did not retain all four bounded invocations'
assert_plan_covers_discovery_once 'all lifecycle race' "$lifecycle_all_plan" ./lifecycleintents '^(Test|Fuzz)'
# Delivery and baseline-batch fixtures each rebuild all migrations. Hosted
# CI 34175171447 still exhausted the 8m package timeout on 12-test shards
# after five completed Open()/migrateThrough fixtures (28-47s into the next).
# Keep every discovered test on the existing four runners and split each
# runner into sequential groups of four (~346s vs 480s on hosted timings).
MIGRATION_RACE_GROUP_SIZE=4
for sharded in delivery baselinebatch releaseacceptance; do
  package="./$sharded"
  import="github.com/inspr-at/paimos/backend/$sharded"
  sharded_race_plan=
  listed_names=()
  while IFS= read -r name; do
    listed_names+=("$name")
  done < <(discover_test_names_ordered "$package" '^(Test|Fuzz)')
  expected_order=
  for ((shard = 0; shard < 4; shard++)); do
    shard_names=()
    for ((index = shard; index < ${#listed_names[@]}; index += 4)); do
      shard_names+=("${listed_names[$index]}")
    done
    (( ${#shard_names[@]} > 0 )) || fail "$sharded race shard $shard is empty"
    expected_groups=$(( (${#shard_names[@]} + MIGRATION_RACE_GROUP_SIZE - 1) / MIGRATION_RACE_GROUP_SIZE ))
    plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
      github.com/inspr-at/paimos/backend/agentd \
      "$import" \
      github.com/inspr-at/paimos/backend/localjournal)
    [[ "$(grep -c "^go test -race -count=1 -timeout=8m $package -run " <<<"$plan")" -eq "$expected_groups" ]] ||
      fail "$sharded race shard $shard did not retain $expected_groups bounded groups"
    shard_plan=$(grep "^go test -race .* $package -run " <<<"$plan")
    group_index=0
    while IFS= read -r group_line; do
      group_count=$(plan_test_names_ordered "$group_line" | wc -l | tr -d ' ')
      (( group_count >= 1 && group_count <= MIGRATION_RACE_GROUP_SIZE )) ||
        fail "$sharded shard $shard group $group_index has $group_count tests, want 1-$MIGRATION_RACE_GROUP_SIZE"
      group_index=$((group_index + 1))
    done <<<"$shard_plan"
    [[ "$group_index" -eq "$expected_groups" ]] ||
      fail "$sharded shard $shard emitted $group_index groups, want $expected_groups"
    expected_order+="$(printf '%s\n' "${shard_names[@]}")"$'\n'
    sharded_race_plan+="$plan"$'\n'
  done
  sharded_only_plan=$(grep "^go test -race .* $package -run " <<<"$sharded_race_plan")
  assert_plan_covers_discovery_once "affected $sharded race" "$sharded_only_plan" "$package" '^(Test|Fuzz)'
  [[ "$(plan_test_names_ordered "$sharded_only_plan")" == "$(printf '%s' "$expected_order")" ]] ||
    fail "$sharded grouped race plan changed discovery order, uniqueness, or round-robin assignment"
  [[ "$(grep -c '^go test -race .* ./agentd$' <<<"$sharded_race_plan")" -eq 1 &&
    "$(grep -c '^go test -race .* ./localjournal$' <<<"$sharded_race_plan")" -eq 1 ]] ||
    fail "$sharded sharding duplicated or omitted another affected package"
  shard_counts=()
  for shard in 0 1 2 3; do
    plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" "$import")
    shard_counts+=("$(plan_test_names "$plan" | wc -l | tr -d ' ')")
  done
  (( shard_counts[0] >= shard_counts[3] && shard_counts[0] - shard_counts[3] <= 1 )) ||
    fail "$sharded test names were not balanced across the four race runners"
  for shard in 0 1 2 3; do
    plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
      github.com/inspr-at/paimos/backend/agentd github.com/inspr-at/paimos/backend/localjournal)
    [[ "$plan" != *"$package"* ]] || fail "unselected $sharded package leaked into the affected race plan"
  done
  sharded_all_plan=$("$RACE_RUNNER" --dry-run --lane=all "$import")
  expected_all_groups=0
  for shard in 0 1 2 3; do
    shard_n=0
    for ((index = shard; index < ${#listed_names[@]}; index += 4)); do
      shard_n=$((shard_n + 1))
    done
    expected_all_groups=$((expected_all_groups + (shard_n + MIGRATION_RACE_GROUP_SIZE - 1) / MIGRATION_RACE_GROUP_SIZE))
  done
  [[ "$(grep -c "^go test -race -count=1 -timeout=8m $package -run " <<<"$sharded_all_plan")" -eq "$expected_all_groups" ]] ||
    fail "all-lane $sharded race did not retain all $expected_all_groups bounded invocations"
  assert_plan_covers_discovery_once "all $sharded race" "$sharded_all_plan" "$package" '^(Test|Fuzz)'
  sharded_sequential_state="$TMP_ROOT/$sharded-sequential-race"
  mkdir -p "$sharded_sequential_state"
  if ! FAKE_GO_STATE="$sharded_sequential_state" GO_COMMAND="$FIXTURES/grouped-go.sh" \
    "$RACE_RUNNER" --lane=all "$import" >/dev/null 2>&1; then
    fail "all-lane $sharded race did not execute sequentially"
  fi
  [[ ! -e "$sharded_sequential_state/overlap" && "$(wc -l < "$sharded_sequential_state/runs" | tr -d ' ')" -eq 8 ]] ||
    fail "all-lane $sharded race overlapped or omitted a grouped shard invocation"
  if GO_COMMAND="$FIXTURES/unsafe-go-list.sh" "$RACE_RUNNER" --dry-run --lane=affected \
    --shard=0/4 "$import" >/dev/null 2>&1; then
    fail "$sharded race sharder accepted an unsafe discovered test name"
  fi
done
lifecycle_sequential_state="$TMP_ROOT/lifecycle-sequential-race"
mkdir -p "$lifecycle_sequential_state"
if ! FAKE_GO_STATE="$lifecycle_sequential_state" GO_COMMAND="$FIXTURES/sequential-go.sh" \
  "$RACE_RUNNER" --lane=all github.com/inspr-at/paimos/backend/lifecycleintents >/dev/null 2>&1; then
  fail 'all-lane lifecycle race did not execute sequentially'
fi
[[ ! -e "$lifecycle_sequential_state/overlap" && "$(wc -l < "$lifecycle_sequential_state/runs" | tr -d ' ')" -eq 4 ]] ||
  fail 'all-lane lifecycle race overlapped or omitted a shard'
if GO_COMMAND="$FIXTURES/unsafe-go-list.sh" "$RACE_RUNNER" --dry-run --lane=affected \
  --shard=0/4 github.com/inspr-at/paimos/backend/lifecycleintents >/dev/null 2>&1; then
  fail 'lifecycle race sharder accepted an unsafe discovered test name'
fi
# The root package has the same cumulative migration budget issue. Both root
# and lifecycle must fan out when selected together, without duplicating peers.
root_race_plan=
for shard in 0 1 2 3; do
  plan=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
    github.com/inspr-at/paimos/backend \
    github.com/inspr-at/paimos/backend/agentd \
    github.com/inspr-at/paimos/backend/lifecycleintents)
  [[ "$(grep -Fc 'go test -race -count=1 -timeout=8m . -run ' <<<"$plan")" -eq 1 &&
    "$(grep -Fc 'go test -race -count=1 -timeout=8m ./lifecycleintents -run ' <<<"$plan")" -eq 1 ]] ||
    fail "root and lifecycle race shard $shard did not retain one bounded invocation each"
  root_race_plan+="$plan"$'\n'
  absent=$("$RACE_RUNNER" --dry-run --lane=affected --shard="$shard/4" \
    github.com/inspr-at/paimos/backend/agentd)
  [[ "$absent" != *' . -run '* ]] || fail 'unselected root package leaked into the affected race plan'
done
root_only_plan=$(grep -F ' . -run ' <<<"$root_race_plan")
assert_plan_covers_discovery_once 'affected root race' "$root_only_plan" . '^(Test|Fuzz)'
[[ "$(grep -Fxc 'go test -race -count=1 -timeout=8m ./agentd' <<<"$root_race_plan")" -eq 1 ]] ||
  fail 'root sharding duplicated or omitted another affected package'
root_all_plan=$("$RACE_RUNNER" --dry-run --lane=all github.com/inspr-at/paimos/backend)
[[ "$(grep -Fc 'go test -race -count=1 -timeout=8m . -run ' <<<"$root_all_plan")" -eq 4 ]] ||
  fail 'all-lane root race did not retain all four bounded invocations'
assert_plan_covers_discovery_once 'all root race' "$root_all_plan" . '^(Test|Fuzz)'
root_full_plan=$(BACKEND_RACE_PACKAGE_TIMEOUT=15m \
  "$RACE_RUNNER" --dry-run --lane=all github.com/inspr-at/paimos/backend)
[[ "$(grep -Fc 'go test -race -count=1 -timeout=15m . -run ' <<<"$root_full_plan")" -eq 4 ]] ||
  fail 'all-lane root race lost its explicit exhaustive package timeout'
root_sequential_state="$TMP_ROOT/root-sequential-race"
mkdir -p "$root_sequential_state"
if ! FAKE_GO_STATE="$root_sequential_state" GO_COMMAND="$FIXTURES/sequential-go.sh" \
  "$RACE_RUNNER" --lane=all github.com/inspr-at/paimos/backend >/dev/null 2>&1; then
  fail 'all-lane root race did not execute sequentially'
fi
[[ ! -e "$root_sequential_state/overlap" && "$(wc -l < "$root_sequential_state/runs" | tr -d ' ')" -eq 4 ]] ||
  fail 'all-lane root race overlapped or omitted a shard'
broad_race_plan=$("$RACE_RUNNER" --dry-run './...')
assert_plan_covers_discovery_once 'broad affected root race' \
  "$(grep -F ' . -run ' <<<"$affected_broad_plan")" . '^(Test|Fuzz)'
assert_plan_covers_discovery_once 'broad all root race' \
  "$(grep -F ' . -run ' <<<"$broad_race_plan")" . '^(Test|Fuzz)'
for package in ./lifecycleclient ./runtimeconsumer ./runtimehealth; do
  invocation="go test -race -count=1 -timeout=8m $package"
  [[ "$(grep -Fxc "$invocation" <<<"$affected_broad_plan")" -eq 1 &&
    "$(grep -Fxc "$invocation" <<<"$broad_race_plan")" -eq 1 ]] ||
    fail "broad race plan omitted, duplicated, or filtered the full $package suite"
done
[[ "$(grep -c '^go test -race .* ./managedharness -run ' <<<"$broad_race_plan")" -eq 7 ]] ||
  fail 'broad race plan omitted or duplicated the managed-harness concurrency and recovery oracles'
broad_lifecycle_plan=$(grep '^go test -race .* ./lifecycleintents -run ' <<<"$broad_race_plan")
[[ "$(grep -c '^go test -race .* ./lifecycleintents -run ' <<<"$affected_broad_plan")" -eq 4 ]] ||
  fail 'broad affected lifecycle race omitted or duplicated a runner'
assert_plan_covers_discovery_once 'broad affected lifecycle race' \
  "$(grep '^go test -race .* ./lifecycleintents -run ' <<<"$affected_broad_plan")" ./lifecycleintents '^(Test|Fuzz)'
assert_plan_covers_discovery_once 'broad all lifecycle race' "$broad_lifecycle_plan" ./lifecycleintents '^(Test|Fuzz)'
broad_managedharness_plan=$(grep '^go test -race .* ./managedharness -run ' <<<"$broad_race_plan")
assert_plan_covers_discovery_once 'broad managed-harness race' "$broad_managedharness_plan" \
  ./managedharness "$managedharness_race_match"

# Exhaustive groups partition the exact default plan, including every root and
# lifecycle shard. They only affect broad all-lane runs, never PR selection.
core_group_plan=$("$RACE_RUNNER" --dry-run --group=core './...')
handlers_group_plan=$("$RACE_RUNNER" --dry-run --group=handlers './...')
runtime_group_plan=$("$RACE_RUNNER" --dry-run --group=runtime './...')
[[ -n "$core_group_plan" && -n "$handlers_group_plan" && -n "$runtime_group_plan" ]] || fail 'a broad race group is empty'
group_union=$(printf '%s\n' "$core_group_plan" "$handlers_group_plan" "$runtime_group_plan" | LC_ALL=C sort)
[[ "$group_union" == "$(LC_ALL=C sort <<<"$broad_race_plan")" ]] ||
  fail 'broad groups omitted, duplicated, or changed a default race invocation'
[[ -z "$(comm -12 <(LC_ALL=C sort <<<"$core_group_plan") <(LC_ALL=C sort <<<"$runtime_group_plan"))" ]] ||
  fail 'broad core and runtime groups overlap'
[[ "$handlers_group_plan" == "$handler_all_plan" ]] ||
  fail 'handlers broad group changed its complete bounded concurrency plan or ordering'
expected_runtime_group=$(printf '%s\n' "$lifecycle_all_plan" \
  'go test -race -count=1 -timeout=8m ./lifecycleclient' \
  'go test -race -count=1 -timeout=8m ./runtimeconsumer' \
  'go test -race -count=1 -timeout=8m ./runtimehealth' "$root_all_plan")
[[ "$runtime_group_plan" == "$expected_runtime_group" ]] ||
  fail 'runtime broad group changed its bounded package ownership or ordering'
for invalid_group in '' all unknown; do
  if "$RACE_RUNNER" --dry-run --group="$invalid_group" './...' >/dev/null 2>&1; then
    fail "race runner accepted invalid or empty broad group [$invalid_group]"
  fi
done
for group in core handlers runtime; do
  if "$RACE_RUNNER" --dry-run --group="$group" github.com/inspr-at/paimos/backend >/dev/null 2>&1 ||
    "$RACE_RUNNER" --dry-run --group="$group" --lane=affected --shard=0/4 './...' >/dev/null 2>&1; then
    fail 'broad group filtering escaped its all-lane ./... interface'
  fi
done
# The existing overlap detector needs at least seven listed tests for the
# managed-harness group. Supply only discovery here; every fake race execution
# still goes through the shared fixture's exclusive in-progress marker.
group_go="$TMP_ROOT/group-sequential-go.sh"
cat >"$group_go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *' test '* && " $* " == *' -list '* ]]; then
  for index in {1..14}; do printf 'TestConcurrent%s\n' "$index"; done
  exit 0
fi
exec "${SEQUENTIAL_GO_FIXTURE:?}" "$@"
EOF
chmod +x "$group_go"
for group in core handlers runtime; do
  group_state="$TMP_ROOT/$group-sequential-race"
  mkdir -p "$group_state"
  expected_group_plan=$(GO_COMMAND="$group_go" "$RACE_RUNNER" --dry-run --group="$group" './...')
  if ! FAKE_GO_STATE="$group_state" SEQUENTIAL_GO_FIXTURE="$FIXTURES/sequential-go.sh" \
    GO_COMMAND="$group_go" "$RACE_RUNNER" --group="$group" './...' >/dev/null 2>&1; then
    fail "$group broad race group did not execute sequentially"
  fi
  [[ ! -e "$group_state/overlap" &&
    "$(wc -l < "$group_state/runs" | tr -d ' ')" -eq "$(wc -l <<<"$expected_group_plan" | tr -d ' ')" ]] ||
    fail "$group broad race group overlapped or omitted an invocation"
done

job_block() {
  local job="$1" file="${2:-$WORKFLOW}"
  awk -v start="  ${job}:" '
    $0 == start {inside=1}
    inside && $0 ~ /^  [A-Za-z0-9_-]+:$/ && $0 != start {exit}
    inside {print}
  ' "$file"
}

[[ -f "$FULL_WORKFLOW" ]] || fail 'dedicated full backend workflow is missing'
grep -q '^  pull_request:$' "$FULL_WORKFLOW" || fail 'labeled PR evidence trigger is missing'
grep -q '^    types: \[labeled\]$' "$FULL_WORKFLOW" || fail 'PR evidence trigger is not limited to label events'
grep -q '^  schedule:$' "$FULL_WORKFLOW" || fail 'nightly schedule trigger is missing'
grep -q '^  workflow_dispatch:$' "$FULL_WORKFLOW" || fail 'manual full-suite trigger is missing'
! grep -q '^  push:' "$FULL_WORKFLOW" || fail 'push still triggers full backend suites'
! grep -q "tags: \['v\*'\]" "$FULL_WORKFLOW" || fail 'tag duplicates already-green protected-main exhaustive assurance'

vet=$(job_block backend-pr-vet)
normal=$(job_block backend-pr)
db=$(job_block backend-pr-db)
handlers=$(job_block backend-pr-handlers)
performance=$(job_block backend-pr-performance)
race=$(job_block backend-pr-race)
managedharness_race=$(job_block backend-pr-managedharness-race)
db_race=$(job_block backend-pr-db-race)
handlers_race=$(job_block backend-pr-handlers-race)
invariants=$(job_block backend-security-invariants)
publish_invariants=$(job_block backend-publish-invariants)
full_serial=$(job_block backend-full-serial "$FULL_WORKFLOW")
full_race=$(job_block backend-full-race "$FULL_WORKFLOW")
full=$(job_block backend-full "$FULL_WORKFLOW")
full_authorize=$(job_block backend-full-authorize "$FULL_WORKFLOW")
quality=$(job_block quality)
frontend=$(job_block frontend-quality)
aggregate=$(job_block test)
docker=$(job_block docker)

grep -qF "RACE_PACKAGE_TIMEOUT=\${BACKEND_RACE_PACKAGE_TIMEOUT:-8m}" "$RACE_RUNNER" ||
  fail 'race package timeout lacks the bounded PR default'
grep -qF "[[ \"\$RACE_PACKAGE_TIMEOUT\" =~ ^[1-9][0-9]*m\$ ]]" "$RACE_RUNNER" ||
  fail 'race package timeout accepts an unsafe or unbounded shape'

[[ "$vet" == *"github.event_name == 'pull_request'"* && "$vet" == *'go vet ./...'* ]] ||
  fail 'parallel PR vet lane is incomplete'
for lane_and_plan in \
  "affected:$normal" \
  "db:$db" \
  "handlers:$handlers" \
  "performance:$performance"
do
  lane=${lane_and_plan%%:*}
  plan=${lane_and_plan#*:}
  [[ "$plan" == *"github.event_name == 'pull_request'"* && "$plan" == *'needs.backend-pr-plan.outputs.selection'* &&
    "$plan" == *"backend-pr-test.sh --lane=$lane"* ]] ||
    fail "parallel PR $lane lane is incomplete"
  [[ "$plan" == *'needs: backend-pr-plan'* &&
    "$plan" != *'backend-ci-packages.sh'* ]] ||
    fail "parallel PR $lane lane bypasses the shared plan"
  [[ "$plan" != *'-p 1'* && "$plan" != *'go test -count=1 -timeout=30m ./...'* ]] ||
    fail "parallel PR $lane lane still runs the serialized/full tree"
done
[[ "$handlers" == *'matrix:'* && "$handlers" == *'needs.backend-pr-plan.outputs.backend-pr-handlers-shards'* &&
  "$handlers" == *"--shard=\"\${{ matrix.shard }}/5\""* ]] ||
  fail 'handler normal shards do not run on five independent matrix runners'
[[ "$normal" == *'matrix:'* && "$normal" == *'needs.backend-pr-plan.outputs.backend-pr-shards'* &&
  "$normal" == *"--shard=\"\${{ matrix.shard }}/2\""* ]] ||
  fail 'affected normal packages do not run on two independent matrix runners'

[[ "$race" == *"github.event_name == 'pull_request'"* ]] || fail 'race PR lane is not pull-request-only'
[[ "$race" == *'needs.backend-pr-plan.outputs.direct_selection'* && "$race" == *'backend-pr-race.sh --lane=affected'* ]] ||
  fail 'race PR lane does not race changed packages'
for lane_and_plan in "race:$race" "managedharness-race:$managedharness_race" "db-race:$db_race" "handlers-race:$handlers_race"; do
  lane=${lane_and_plan%%:*}
  plan=${lane_and_plan#*:}
  [[ "$plan" == *'needs: backend-pr-plan'* &&
    "$plan" != *'backend-ci-packages.sh'* ]] ||
    fail "parallel PR $lane lane bypasses the shared plan"
done
[[ "$race" != *'-p 1'* && "$race" != *'go test -race -count=1 -timeout=30m ./...'* ]] ||
  fail 'race PR lane still races the full tree'
[[ "$race" == *'matrix:'* && "$race" == *'needs.backend-pr-plan.outputs.backend-pr-race-shards'* &&
  "$race" == *"--shard=\"\${{ matrix.shard }}/4\""* ]] ||
  fail 'affected race packages do not run on four independent matrix runners'
[[ "$managedharness_race" == *'needs.backend-pr-plan.outputs.direct_selection'* &&
  "$managedharness_race" == *'backend-pr-race.sh --lane=managedharness'* &&
  "$managedharness_race" == *'matrix:'* && "$managedharness_race" == *'needs.backend-pr-plan.outputs.backend-pr-managedharness-race-shards'* &&
  "$managedharness_race" == *"--shard=\"\${{ matrix.shard }}/7\""* ]] ||
  fail 'managed-harness race oracles do not run on seven independent matrix runners'
[[ "$db_race" == *'needs.backend-pr-plan.outputs.direct_selection'* && "$db_race" == *'backend-pr-race.sh --lane=db'* ]] ||
  fail 'parallel PR DB race lane is incomplete'
[[ "$handlers_race" == *'needs.backend-pr-plan.outputs.direct_selection'* && "$handlers_race" == *'backend-pr-race.sh --lane=handlers'* ]] ||
  fail 'parallel PR handler race lane is incomplete'
[[ "$handlers_race" == *'matrix:'* && "$handlers_race" == *'needs.backend-pr-plan.outputs.backend-pr-handlers-race-shards'* &&
  "$handlers_race" == *"--shard=\"\${{ matrix.shard }}/5\""* ]] ||
  fail 'handler race shards do not run on five independent matrix runners'

[[ "$invariants" != *'TestRegression_'* && "$invariants" != *'TestAuthzFuzz_'* && "$invariants" == *'paimos_test_unsupported'* ]] ||
  fail 'parallel security/platform invariant lane is incomplete'
[[ "$publish_invariants" == *"github.event_name == 'push'"* &&
  "$publish_invariants" == *"github.ref_type == 'tag'"* &&
  "$publish_invariants" != *'go test'* &&
  "$publish_invariants" == *"$FULL_WAIT_CALL"* ]] ||
  fail 'tag publish path duplicates tests, polls, or lacks exact-code backend assurance'

[[ "$full_authorize" == *'backend-full-authorize.sh'* &&
  "$full_authorize" == *"if: $FULL_PR_GUARD"* &&
  "$full_authorize" == *"$FULL_LABEL_ENV"* ]] ||
  fail 'labeled PR exhaustive proof lacks a fail-closed operator-label guard'
[[ "$full_serial" == *'needs: backend-full-authorize'* && "$full_serial" == *'timeout-minutes: 90'* &&
  "$full_serial" == *'go test -count=1 -p 1 -timeout=40m ./...'* &&
  "$full_serial" == *'paimos_test_unsupported'* ]] ||
  fail 'full backend serial/platform assurance lacks an explicit independent budget'
[[ "$full_race" == *'needs: backend-full-authorize'* && "$full_race" == *'timeout-minutes: 90'* &&
  "$full_race" == *'BACKEND_RACE_PACKAGE_TIMEOUT: 15m'* &&
  "$full_race" == *'matrix:'* && "$full_race" == *'group: [core, handlers, runtime]'* &&
  "$full_race" == *'fail-fast: false'* && "$full_race" != *'continue-on-error:'* &&
  "$full_race" == *"backend-pr-race.sh --group=\"\${{ matrix.group }}\" './...'"* &&
  "$full_race" == *'sequential'* ]] ||
  fail 'full backend broad race lacks an explicit independent budget or sequential topology'
[[ "$full" == *'needs: [backend-full-authorize, backend-full-serial, backend-full-race]'* &&
  "$full" == *"if: $FULL_AGGREGATE_GUARD"* && "$full" == *"$FULL_AUTH_RESULT"* &&
  "$full" == *"$FULL_AUTH_ASSERT"* && "$full" == *"$FULL_SERIAL_RESULT"* &&
  "$full" == *"$FULL_RACE_RESULT"* && "$full" == *"$FULL_SERIAL_ASSERT"* &&
  "$full" == *"$FULL_RACE_ASSERT"* ]] ||
  fail 'full backend workflow lacks a fail-closed serial/race aggregator'
[[ "$(cat "$FULL_WORKFLOW")" != *'run_full'* &&
  "$(cat "$FULL_WORKFLOW")" != *'backend-full-reuse'* &&
  "$(cat "$FULL_WORKFLOW")" != *'continue-on-error'* ]] ||
  fail 'nightly backend evidence can skip execution or hide a failure'
grep -q 'BACKEND_FULL_TIMEOUT_SECONDS:-6000' "$FULL_WAITER" ||
  fail 'exact-head full-suite waiter budget is not derived from parallel job budgets'

[[ -n "$quality" ]] || fail 'quality lane is missing'
[[ "$frontend" == *'npm run schema:check'* && "$frontend" == *'npm test'* ]] ||
  fail 'frontend quality lane lost schema, lint, type, or unit assurance'
for dependency in \
  backend-pr-plan backend-pr-vet backend-pr backend-pr-db backend-pr-handlers backend-pr-performance \
  backend-pr-race backend-pr-managedharness-race backend-pr-db-race backend-pr-handlers-race \
  backend-security-invariants backend-publish-invariants quality frontend-quality
do
  [[ "$aggregate" == *"$dependency"* ]] || fail "required test aggregator does not depend on $dependency"
done
[[ "$aggregate" == *'BACKEND_PR_MANAGEDHARNESS_RACE: ${{ needs.backend-pr-managedharness-race.result }}'* &&
  "$aggregate" == *'require_planned_lane "$BACKEND_PR_MANAGEDHARNESS_RACE_PLANNED" "$BACKEND_PR_MANAGEDHARNESS_RACE"'* &&
  "$aggregate" == *'[[ "$BACKEND_PR_MANAGEDHARNESS_RACE" == '\''skipped'\'' ]]'* ]] ||
  fail 'required test aggregator does not fail closed on the managed-harness race matrix result'
[[ "$aggregate" != *'backend-full'* ]] || fail 'required PR test aggregator still depends on the full backend suite'
[[ "$docker" == *'needs.test.result == '\''success'\'''* &&
  "$docker" != *"needs.test.result != 'failure'"* ]] ||
  fail 'docker can publish after the event-relevant test aggregator was skipped'

grep -q 'two tag workflows' "$RELEASE_DOC" || fail 'release documentation does not name the two artifact tag workflows'
grep -q 'backend-full.yml' "$RELEASE_DOC" || fail 'release documentation omits pre-tag exhaustive backend assurance'

python3 "$ROOT/scripts/test-backend-full-evidence.py"
python3 "$ROOT/scripts/test-backend-ci-dedupe.py"
python3 "$ROOT/scripts/test-backend-pr-plan.py"
echo 'test-backend-pr-gate: ok'
