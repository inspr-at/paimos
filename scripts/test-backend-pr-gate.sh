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
CI_SELECTION_CALL="selection=\$(../scripts/backend-ci-packages.sh"
FULL_WAIT_CALL="wait-backend-full.sh \"\$GITHUB_SHA\""
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
GITHUB_EVENT_NAME=push "$FULL_AUTHORIZER" ||
  fail 'backend-full evidence authorizer rejected protected-main execution'
if GITHUB_EVENT_NAME=pull_request BACKEND_FULL_LABEL=unrelated "$FULL_AUTHORIZER" >/dev/null 2>&1; then
  fail 'backend-full evidence authorizer accepted an unrelated PR label'
fi

fixture_head='1111111111111111111111111111111111111111'
FAKE_HEAD_SHA="$fixture_head" FAKE_BACKEND_FULL_MODE=success \
  GH_COMMAND="$FIXTURES/backend-full-gh.sh" "$FULL_WAITER" "$fixture_head" >/dev/null ||
  fail 'exact-head backend-full waiter rejected successful exhaustive evidence'
for mode in failed wrong-head skipped; do
  if FAKE_HEAD_SHA="$fixture_head" FAKE_BACKEND_FULL_MODE="$mode" \
    GH_COMMAND="$FIXTURES/backend-full-gh.sh" "$FULL_WAITER" "$fixture_head" >/dev/null 2>&1; then
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
  github.com/inspr-at/paimos/backend/cmd/dev-fixture-sql \
  github.com/inspr-at/paimos/backend/cmd/paimos \
  github.com/inspr-at/paimos/backend/cmd/paimos-mcp \
  github.com/inspr-at/paimos/backend/db \
  github.com/inspr-at/paimos/backend/delivery \
  github.com/inspr-at/paimos/backend/externalstage \
  github.com/inspr-at/paimos/backend/handlers \
  github.com/inspr-at/paimos/backend/handlers/crm/http \
  github.com/inspr-at/paimos/backend/handlers/crm/hubspot \
  github.com/inspr-at/paimos/backend/handlers/knowledge \
  github.com/inspr-at/paimos/backend/internal/knowledge857 \
  github.com/inspr-at/paimos/backend/managedharness \
  github.com/inspr-at/paimos/backend/supervision)
[[ "$db_affected" == "$db_expected" ]] ||
  fail "db reverse-dependency closure drifted: [$db_affected]"
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
  affected_plan+="$plan"$'\n'
done
[[ "$affected_plan" == *'subscribe\ before\ high-water'* && "$affected_plan" == *'permission\ grant\ and\ revoke'* ]] ||
  fail 'affected normal lane lost non-performance Agent Mode stream contracts'
[[ "$affected_plan" != *'overflow\ lost\ wake\ coalescing\ and\ restart'* && "$affected_plan" != *'./db'* && "$affected_plan" != *'./handlers'* ]] ||
  fail 'affected normal lane duplicates an isolated performance, DB, or handler contract'
[[ "$(printf '%s\n' "$affected_plan" | rg -o 'github.com/inspr-at/paimos/backend/auth' | wc -l | tr -d ' ')" -eq 1 ]] ||
  fail 'affected normal shards omit or duplicate a selected package'

db_plan=$("$TEST_RUNNER" --dry-run --lane=db github.com/inspr-at/paimos/backend/db)
[[ "$(grep -c '^go test .* ./db -run ' <<<"$db_plan")" -eq 4 ]] ||
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
  [[ "$(grep -c '^go test .* ./handlers -run ' <<<"$plan")" -eq 1 ]] ||
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
for shard in 0 1 2 3 4; do
  plan=$("$RACE_RUNNER" --dry-run --lane=handlers --shard="$shard/5" github.com/inspr-at/paimos/backend/handlers)
  [[ "$(grep -c '^go test -race .* ./handlers -run ' <<<"$plan")" -eq 1 ]] ||
    fail "handler race shard $shard does not own exactly one invocation"
  [[ "$(printf '%s\n' "$plan" | rg -o 'Test[A-Za-z0-9_]+' | wc -l | tr -d ' ')" -gt 0 ]] ||
    fail "handler race shard $shard is empty"
  handler_race_plan+="$plan"$'\n'
done
[[ "$handler_race_plan" == *'Concurrent'* && "$handler_race_plan" != *'TestRegression_'* && "$handler_race_plan" != *'TestAuthzFuzz_'* ]] ||
  fail 'handler race plan is not limited to concurrency contracts'
[[ "$(grep -c '^go test -race .* ./handlers -run ' <<<"$handler_race_plan")" -eq 5 ]] ||
  fail 'handler concurrency race does not have five independently runnable shards'
assert_plan_covers_discovery_once 'handler concurrency race' "$handler_race_plan" ./handlers \
  '^Test.*(Concurrent|Concurrency|Race|Atomic|BatchesReleaseWriter|RacedPoke).*$'
if GO_COMMAND="$FIXTURES/unsafe-go-list.sh" "$RACE_RUNNER" --dry-run --lane=handlers \
  --shard=0/5 github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler race sharder filtered an unsafe discovered test name instead of failing closed'
fi

sequential_state="$TMP_ROOT/sequential-race"
mkdir -p "$sequential_state"
if ! FAKE_GO_STATE="$sequential_state" GO_COMMAND="$FIXTURES/sequential-go.sh" \
  "$RACE_RUNNER" --lane=all github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'full backend handler race shards did not run sequentially'
fi
[[ ! -e "$sequential_state/overlap" && "$(wc -l < "$sequential_state/runs" | tr -d ' ')" -eq 5 ]] ||
  fail 'full backend handler race plan overlapped or omitted an indexed shard'
agentmode_race_plan=$("$RACE_RUNNER" --dry-run --lane=affected github.com/inspr-at/paimos/backend/agentmode)
[[ "$agentmode_race_plan" == *'subscribe\ before\ high-water'* && "$agentmode_race_plan" == *'permission\ grant\ and\ revoke'* ]] ||
  fail 'agentmode race plan lost non-performance stream concurrency subtests'
[[ "$agentmode_race_plan" != *'overflow\ lost\ wake'* ]] ||
  fail 'agentmode race plan includes a latency budget invalid under race instrumentation'
auth_race_plan=$("$RACE_RUNNER" --dry-run --lane=affected github.com/inspr-at/paimos/backend/auth)
[[ "$(grep -c '^go test -race .* ./auth -run ' <<<"$auth_race_plan")" -eq 1 &&
  "$auth_race_plan" == *'TestResolveAPIKeyUsageStampNeverInheritsSQLiteBusyTimeout'* &&
  "$auth_race_plan" == *'TestResolveAPIKeyRecentUsageStaysReadOnlyWhileSQLiteWriterIsBusy'* ]] ||
  fail 'auth PR race plan lost its package-local SQLite contention proofs'
[[ "$auth_race_plan" != *' ./auth$' && "$auth_race_plan" != *'./...'* ]] ||
  fail 'auth PR race plan expanded back to the exhaustive package suite'

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
grep -q 'branches: \[main\]' "$FULL_WORKFLOW" || fail 'main full-suite trigger is missing'
! grep -q "tags: \['v\*'\]" "$FULL_WORKFLOW" || fail 'tag duplicates already-green protected-main exhaustive assurance'

vet=$(job_block backend-pr-vet)
normal=$(job_block backend-pr)
db=$(job_block backend-pr-db)
handlers=$(job_block backend-pr-handlers)
performance=$(job_block backend-pr-performance)
race=$(job_block backend-pr-race)
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
  [[ "$plan" == *"github.event_name == 'pull_request'"* && "$plan" == *'backend-ci-packages.sh'* &&
    "$plan" == *"backend-pr-test.sh --lane=$lane"* ]] ||
    fail "parallel PR $lane lane is incomplete"
  [[ "$plan" == *"$CI_SELECTION_CALL"* &&
    "$plan" != *'mapfile -t packages < <('* ]] ||
    fail "parallel PR $lane lane does not propagate selector failures"
  [[ "$plan" != *'-p 1'* && "$plan" != *'go test -count=1 -timeout=30m ./...'* ]] ||
    fail "parallel PR $lane lane still runs the serialized/full tree"
done
[[ "$handlers" == *'matrix:'* && "$handlers" == *'shard: [0, 1, 2, 3, 4]'* &&
  "$handlers" == *"--shard=\"\${{ matrix.shard }}/5\""* ]] ||
  fail 'handler normal shards do not run on five independent matrix runners'
[[ "$normal" == *'matrix:'* && "$normal" == *'shard: [0, 1]'* &&
  "$normal" == *"--shard=\"\${{ matrix.shard }}/2\""* ]] ||
  fail 'affected normal packages do not run on two independent matrix runners'

[[ "$race" == *"github.event_name == 'pull_request'"* ]] || fail 'race PR lane is not pull-request-only'
[[ "$race" == *'backend-ci-packages.sh --direct'* && "$race" == *'backend-pr-race.sh --lane=affected'* ]] ||
  fail 'race PR lane does not race changed packages'
for lane_and_plan in "race:$race" "db-race:$db_race" "handlers-race:$handlers_race"; do
  lane=${lane_and_plan%%:*}
  plan=${lane_and_plan#*:}
  [[ "$plan" == *"$CI_SELECTION_CALL"* &&
    "$plan" != *'mapfile -t packages < <('* ]] ||
    fail "parallel PR $lane lane does not propagate selector failures"
done
[[ "$race" != *'-p 1'* && "$race" != *'go test -race -count=1 -timeout=30m ./...'* ]] ||
  fail 'race PR lane still races the full tree'
[[ "$db_race" == *'backend-ci-packages.sh --direct'* && "$db_race" == *'backend-pr-race.sh --lane=db'* ]] ||
  fail 'parallel PR DB race lane is incomplete'
[[ "$handlers_race" == *'backend-ci-packages.sh --direct'* && "$handlers_race" == *'backend-pr-race.sh --lane=handlers'* ]] ||
  fail 'parallel PR handler race lane is incomplete'
[[ "$handlers_race" == *'matrix:'* && "$handlers_race" == *'shard: [0, 1, 2, 3, 4]'* &&
  "$handlers_race" == *"--shard=\"\${{ matrix.shard }}/5\""* ]] ||
  fail 'handler race shards do not run on five independent matrix runners'

[[ "$invariants" == *'TestRegression_'* && "$invariants" == *'TestAuthzFuzz_'* && "$invariants" == *'paimos_test_unsupported'* ]] ||
  fail 'parallel security/platform invariant lane is incomplete'
[[ "$publish_invariants" == *"github.event_name == 'push'"* &&
  "$publish_invariants" == *'paimos_test_unsupported'* &&
  "$publish_invariants" == *"$FULL_WAIT_CALL"* ]] ||
  fail 'main/tag publish path lacks executable backend fail-closed assurance'

[[ "$full_authorize" == *'backend-full-authorize.sh'* &&
  "$full_authorize" == *"if: $FULL_PR_GUARD"* &&
  "$full_authorize" == *"$FULL_LABEL_ENV"* ]] ||
  fail 'labeled PR exhaustive proof lacks a fail-closed operator-label guard'
[[ "$full_serial" == *'needs: backend-full-authorize'* && "$full_serial" == *'timeout-minutes: 40'* &&
  "$full_serial" == *'go test -count=1 -p 1 -timeout=30m ./...'* &&
  "$full_serial" == *'paimos_test_unsupported'* ]] ||
  fail 'full backend serial/platform assurance lacks an explicit independent budget'
[[ "$full_race" == *'needs: backend-full-authorize'* && "$full_race" == *'timeout-minutes: 40'* &&
  "$full_race" == *"backend-pr-race.sh './...'"* && "$full_race" == *'sequential'* ]] ||
  fail 'full backend broad race lacks an explicit independent budget or sequential topology'
[[ "$full" == *'needs: [backend-full-authorize, backend-full-serial, backend-full-race]'* &&
  "$full" == *"if: $FULL_AGGREGATE_GUARD"* && "$full" == *"$FULL_AUTH_RESULT"* &&
  "$full" == *"$FULL_AUTH_ASSERT"* && "$full" == *"$FULL_SERIAL_RESULT"* &&
  "$full" == *"$FULL_RACE_RESULT"* && "$full" == *"$FULL_SERIAL_ASSERT"* &&
  "$full" == *"$FULL_RACE_ASSERT"* ]] ||
  fail 'full backend workflow lacks a fail-closed serial/race aggregator'
grep -q 'BACKEND_FULL_TIMEOUT_SECONDS:-3000' "$FULL_WAITER" ||
  fail 'exact-head full-suite waiter budget is not derived from parallel job budgets'

[[ -n "$quality" ]] || fail 'quality lane is missing'
[[ "$frontend" == *'npm run schema:check'* && "$frontend" == *'npm test'* ]] ||
  fail 'frontend quality lane lost schema, lint, type, or unit assurance'
for dependency in \
  backend-pr-vet backend-pr backend-pr-db backend-pr-handlers backend-pr-performance \
  backend-pr-race backend-pr-db-race backend-pr-handlers-race \
  backend-security-invariants backend-publish-invariants quality frontend-quality
do
  [[ "$aggregate" == *"$dependency"* ]] || fail "required test aggregator does not depend on $dependency"
done
[[ "$aggregate" != *'backend-full'* ]] || fail 'required PR test aggregator still depends on the full backend suite'
[[ "$docker" == *'needs.test.result == '\''success'\'''* &&
  "$docker" != *"needs.test.result != 'failure'"* ]] ||
  fail 'docker can publish after the event-relevant test aggregator was skipped'

grep -q 'two tag workflows' "$RELEASE_DOC" || fail 'release documentation does not name the two artifact tag workflows'
grep -q 'backend-full.yml' "$RELEASE_DOC" || fail 'release documentation omits pre-tag exhaustive backend assurance'

echo 'test-backend-pr-gate: ok'
