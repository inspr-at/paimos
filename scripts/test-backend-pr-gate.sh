#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SELECTOR="$ROOT/scripts/backend-changed-packages.sh"
TEST_RUNNER="$ROOT/scripts/backend-pr-test.sh"
RACE_RUNNER="$ROOT/scripts/backend-pr-race.sh"
WORKFLOW="$ROOT/.github/workflows/ci-v2.yml"
FULL_WORKFLOW="$ROOT/.github/workflows/backend-full.yml"

fail() {
  echo "test-backend-pr-gate: $*" >&2
  exit 1
}

check_selection() {
  local expected="$1"
  shift
  local actual
  actual=$(printf '%s\n' "$@" | "$SELECTOR" --files-from -)
  [[ "$actual" == "$expected" ]] ||
    fail "selection for [$*] was [$actual], want [$expected]"
}

check_selection_contains() {
  local files="$1"
  shift
  local actual file_array=()
  read -r -a file_array <<<"$files"
  actual=$(printf '%s\n' "${file_array[@]}" | "$SELECTOR" --files-from -)
  for expected in "$@"; do
    grep -Fxq "$expected" <<<"$actual" ||
      fail "affected selection for [$files] omitted [$expected]: [$actual]"
  done
}

[[ -x "$SELECTOR" ]] || fail "missing executable changed-package selector: $SELECTOR"
[[ -x "$TEST_RUNNER" ]] || fail "missing executable changed-package test runner: $TEST_RUNNER"
[[ -x "$RACE_RUNNER" ]] || fail "missing executable changed-package race runner: $RACE_RUNNER"

check_selection '' docs/INSTALL.md
check_selection_contains 'backend/supervision/service.go backend/supervision/service_integration_test.go' \
  github.com/inspr-at/paimos/backend/supervision \
  github.com/inspr-at/paimos/backend/cmd/paimos
check_selection_contains 'backend/db/db.go' \
  github.com/inspr-at/paimos/backend/db \
  github.com/inspr-at/paimos/backend/auth \
  github.com/inspr-at/paimos/backend/handlers
db_affected=$(printf '%s\n' backend/db/db.go | "$SELECTOR" --files-from -)
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
  github.com/inspr-at/paimos/backend/managedharness \
  github.com/inspr-at/paimos/backend/supervision)
[[ "$db_affected" == "$db_expected" ]] ||
  fail "db reverse-dependency closure drifted: [$db_affected]"
! grep -Fxq 'github.com/inspr-at/paimos/backend/pharoslink' <<<"$db_affected" ||
  fail 'db reverse-dependency closure included an unrelated package'
check_selection_contains 'backend/contracts/fixtures/external-stage/dependency-janus-v1.json' \
  github.com/inspr-at/paimos/backend/contracts \
  github.com/inspr-at/paimos/backend/externalstage
direct_db=$(printf '%s\n' backend/db/db.go | "$SELECTOR" --direct --files-from -)
[[ "$direct_db" == 'github.com/inspr-at/paimos/backend/db' ]] ||
  fail "direct changed-package selection unexpectedly expanded: [$direct_db]"
check_selection './...' backend/go.mod
check_selection './...' backend/removed-package/deleted.go
check_selection './...' backend/contracts/fixtures/deleted.go

affected_plan=$("$TEST_RUNNER" --dry-run --lane=affected \
  github.com/inspr-at/paimos/backend/agentmode \
  github.com/inspr-at/paimos/backend/db \
  github.com/inspr-at/paimos/backend/handlers)
[[ "$affected_plan" == *'subscribe\ before\ high-water'* && "$affected_plan" == *'permission\ grant\ and\ revoke'* ]] ||
  fail 'affected normal lane lost non-performance Agent Mode stream contracts'
[[ "$affected_plan" != *'overflow\ lost\ wake\ coalescing\ and\ restart'* && "$affected_plan" != *'./db'* && "$affected_plan" != *'./handlers'* ]] ||
  fail 'affected normal lane duplicates an isolated performance, DB, or handler contract'

db_plan=$("$TEST_RUNNER" --dry-run --lane=db github.com/inspr-at/paimos/backend/db)
[[ "$(grep -c '^go test .* ./db -run ' <<<"$db_plan")" -eq 4 ]] ||
  fail 'db package is not split into four normal-test shards'
[[ "$(printf '%s\n' "$db_plan" | rg -o 'Test[A-Za-z0-9_]+' | LC_ALL=C sort -u | wc -l | tr -d ' ')" -eq 136 ]] ||
  fail 'db normal-test shards do not account for every top-level test'

handler_plan=
if "$TEST_RUNNER" --dry-run --lane=handlers github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler normal lane still allows four local processes instead of requiring one matrix shard'
fi
for shard in 0 1 2 3; do
  plan=$("$TEST_RUNNER" --dry-run --lane=handlers --shard="$shard/4" github.com/inspr-at/paimos/backend/handlers)
  [[ "$(grep -c '^go test .* ./handlers -run ' <<<"$plan")" -eq 1 ]] ||
    fail "handler normal shard $shard does not own exactly one invocation"
  handler_plan+="$plan"$'\n'
done
[[ "$(printf '%s\n' "$handler_plan" | rg -o 'Test[A-Za-z0-9_]+' | LC_ALL=C sort -u | wc -l | tr -d ' ')" -eq 664 ]] ||
  fail 'handlers normal-test shards do not account for every top-level test'

performance_plan=$("$TEST_RUNNER" --dry-run --lane=performance github.com/inspr-at/paimos/backend/agentmode)
[[ "$performance_plan" == *'overflow\ lost\ wake\ coalescing\ and\ restart'* && "$performance_plan" != *'subscribe\ before\ high-water'* ]] ||
  fail 'isolated normal lane does not exclusively own the unchanged Agent Mode performance contract'

db_race_plan=$("$RACE_RUNNER" --dry-run --lane=db github.com/inspr-at/paimos/backend/db)
[[ "$db_race_plan" == *'./db'* && "$db_race_plan" == *'TestSchemaAgentRunTelemetryTerminalWriteRace'* &&
  "$db_race_plan" == *'TestM147ConcurrentCanonicalCommandsConverge'* &&
  "$db_race_plan" == *'TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwner'* ]] ||
  fail 'db race plan lost its package-local concurrency proof'
[[ "$(grep -c '^go test -race .* ./db -run ' <<<"$db_race_plan")" -eq 3 ]] ||
  fail 'db race plan does not isolate the two 32-writer arbitration proofs'
[[ "$db_race_plan" != *'./...'* && "$db_race_plan" != *'./handlers'* ]] ||
  fail 'db race plan escaped the changed package'
handler_race_plan=
if "$RACE_RUNNER" --dry-run --lane=handlers github.com/inspr-at/paimos/backend/handlers >/dev/null 2>&1; then
  fail 'handler race lane still allows four local processes instead of requiring one matrix shard'
fi
for shard in 0 1 2 3; do
  plan=$("$RACE_RUNNER" --dry-run --lane=handlers --shard="$shard/4" github.com/inspr-at/paimos/backend/handlers)
  [[ "$(grep -c '^go test -race .* ./handlers -run ' <<<"$plan")" -eq 1 ]] ||
    fail "handler race shard $shard does not own exactly one invocation"
  handler_race_plan+="$plan"$'\n'
done
[[ "$handler_race_plan" == *'Concurrent'* && "$handler_race_plan" != *'TestRegression_'* && "$handler_race_plan" != *'TestAuthzFuzz_'* ]] ||
  fail 'handler race plan is not limited to concurrency contracts'
[[ "$(grep -c '^go test -race .* ./handlers -run ' <<<"$handler_race_plan")" -eq 4 ]] ||
  fail 'handler concurrency race does not have four independently runnable shards'
[[ "$(printf '%s\n' "$handler_race_plan" | rg -o 'Test[A-Za-z0-9_]+' | LC_ALL=C sort -u | wc -l | tr -d ' ')" -eq 19 ]] ||
  fail 'handler concurrency race shards do not account for all 19 top-level contracts exactly once'
agentmode_race_plan=$("$RACE_RUNNER" --dry-run --lane=affected github.com/inspr-at/paimos/backend/agentmode)
[[ "$agentmode_race_plan" == *'subscribe\ before\ high-water'* && "$agentmode_race_plan" == *'permission\ grant\ and\ revoke'* ]] ||
  fail 'agentmode race plan lost non-performance stream concurrency subtests'
[[ "$agentmode_race_plan" != *'overflow\ lost\ wake'* ]] ||
  fail 'agentmode race plan includes a latency budget invalid under race instrumentation'

job_block() {
  local job="$1" file="${2:-$WORKFLOW}"
  awk -v start="  ${job}:" '
    $0 == start {inside=1}
    inside && $0 ~ /^  [A-Za-z0-9_-]+:$/ && $0 != start {exit}
    inside {print}
  ' "$file"
}

[[ -f "$FULL_WORKFLOW" ]] || fail 'dedicated full backend workflow is missing'
grep -q '^  schedule:$' "$FULL_WORKFLOW" || fail 'nightly schedule trigger is missing'
grep -q '^  workflow_dispatch:$' "$FULL_WORKFLOW" || fail 'manual full-suite trigger is missing'
grep -q 'branches: \[main\]' "$FULL_WORKFLOW" || fail 'main full-suite trigger is missing'
grep -q "tags: \['v\*'\]" "$FULL_WORKFLOW" || fail 'tag full-suite trigger is missing'

vet=$(job_block backend-pr-vet)
normal=$(job_block backend-pr)
db=$(job_block backend-pr-db)
handlers=$(job_block backend-pr-handlers)
performance=$(job_block backend-pr-performance)
race=$(job_block backend-pr-race)
db_race=$(job_block backend-pr-db-race)
handlers_race=$(job_block backend-pr-handlers-race)
invariants=$(job_block backend-security-invariants)
full=$(job_block backend-full "$FULL_WORKFLOW")
quality=$(job_block quality)
frontend=$(job_block frontend-quality)
aggregate=$(job_block test)

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
  [[ "$plan" == *"github.event_name == 'pull_request'"* && "$plan" == *'backend-changed-packages.sh'* &&
    "$plan" == *"backend-pr-test.sh --lane=$lane"* ]] ||
    fail "parallel PR $lane lane is incomplete"
  [[ "$plan" != *'-p 1'* && "$plan" != *'go test -count=1 -timeout=30m ./...'* ]] ||
    fail "parallel PR $lane lane still runs the serialized/full tree"
done
[[ "$handlers" == *'matrix:'* && "$handlers" == *'shard: [0, 1, 2, 3]'* &&
  "$handlers" == *"--shard=\"\${{ matrix.shard }}/4\""* ]] ||
  fail 'handler normal shards do not run on four independent matrix runners'

[[ "$race" == *"github.event_name == 'pull_request'"* ]] || fail 'race PR lane is not pull-request-only'
[[ "$race" == *'backend-changed-packages.sh --direct'* && "$race" == *'backend-pr-race.sh --lane=affected'* ]] ||
  fail 'race PR lane does not race changed packages'
[[ "$race" != *'-p 1'* && "$race" != *'go test -race -count=1 -timeout=30m ./...'* ]] ||
  fail 'race PR lane still races the full tree'
[[ "$db_race" == *'backend-changed-packages.sh --direct'* && "$db_race" == *'backend-pr-race.sh --lane=db'* ]] ||
  fail 'parallel PR DB race lane is incomplete'
[[ "$handlers_race" == *'backend-changed-packages.sh --direct'* && "$handlers_race" == *'backend-pr-race.sh --lane=handlers'* ]] ||
  fail 'parallel PR handler race lane is incomplete'
[[ "$handlers_race" == *'matrix:'* && "$handlers_race" == *'shard: [0, 1, 2, 3]'* &&
  "$handlers_race" == *"--shard=\"\${{ matrix.shard }}/4\""* ]] ||
  fail 'handler race shards do not run on four independent matrix runners'

[[ "$invariants" == *'TestRegression_'* && "$invariants" == *'TestAuthzFuzz_'* && "$invariants" == *'paimos_test_unsupported'* ]] ||
  fail 'parallel security/platform invariant lane is incomplete'

[[ "$full" == *'go test -count=1 -p 1 -timeout=30m ./...'* && "$full" == *"backend-pr-race.sh './...'"* ]] ||
  fail 'full backend lane lost serial or broad-race assurance'
[[ "$full" != *"github.event_name == 'pull_request'"* ]] || fail 'full backend lane still runs on pull requests'

[[ -n "$quality" ]] || fail 'quality lane is missing'
[[ "$frontend" == *'npm run schema:check'* && "$frontend" == *'npm test'* ]] ||
  fail 'frontend quality lane lost schema, lint, type, or unit assurance'
for dependency in \
  backend-pr-vet backend-pr backend-pr-db backend-pr-handlers backend-pr-performance \
  backend-pr-race backend-pr-db-race backend-pr-handlers-race \
  backend-security-invariants quality frontend-quality
do
  [[ "$aggregate" == *"$dependency"* ]] || fail "required test aggregator does not depend on $dependency"
done
[[ "$aggregate" != *'backend-full'* ]] || fail 'required PR test aggregator still depends on the full backend suite'

echo 'test-backend-pr-gate: ok'
