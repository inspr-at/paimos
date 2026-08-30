#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
DRY_RUN=0

if [[ "${1:-}" == '--dry-run' ]]; then
  DRY_RUN=1
  shift
fi
[[ $# -gt 0 ]] || {
  echo "usage: $0 [--dry-run] <changed-package>..." >&2
  exit 2
}

run_race() {
  local package="$1" pattern="${2:-}"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    if [[ -n "$pattern" ]]; then
      printf 'go test -race -count=1 -timeout=8m %q -run %q\n' "$package" "$pattern"
    else
      printf 'go test -race -count=1 -timeout=8m %q\n' "$package"
    fi
    return
  fi

  cd "$BACKEND"
  if [[ -n "$pattern" ]]; then
    local listed list_pattern="${pattern%%/*}"
    listed=$("$GO_COMMAND" test -list "$list_pattern" "$package")
    grep -Eq '^(Test|Fuzz)' <<<"$listed" || {
      echo "backend-pr-race: no tests matched $pattern in $package" >&2
      exit 1
    }
    "$GO_COMMAND" test -race -count=1 -timeout=8m "$package" -run "$pattern"
  else
    "$GO_COMMAND" test -race -count=1 -timeout=8m "$package"
  fi
}

run_race_shards() {
  local package="$1" match="$2" shard_count="$3"
  local names=() shard index pattern pid failed=0
  local pids=()
  cd "$BACKEND"
  while IFS= read -r name; do
    [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] || {
      echo "backend-pr-race: unsafe test name for $package: $name" >&2
      exit 2
    }
    names+=("$name")
  done < <("$GO_COMMAND" test -list "$match" "$package" | awk '/^(Test|Fuzz)[A-Za-z0-9_]+$/')
  [[ "${#names[@]}" -gt 0 ]] || {
    echo "backend-pr-race: no tests matched $match in $package" >&2
    exit 1
  }

  for ((shard = 0; shard < shard_count; shard++)); do
    pattern='^('
    for ((index = shard; index < ${#names[@]}; index += shard_count)); do
      [[ "$pattern" == '^(' ]] || pattern+='|'
      pattern+="${names[$index]}"
    done
    pattern+=')$'
    if [[ "$DRY_RUN" -eq 1 ]]; then
      printf 'go test -race -count=1 -timeout=8m %q -run %q\n' "$package" "$pattern"
    else
      "$GO_COMMAND" test -race -count=1 -timeout=8m "$package" -run "$pattern" &
      pids+=("$!")
    fi
  done

  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  for pid in "${pids[@]}"; do
    if ! wait "$pid"; then
      failed=1
    fi
  done
  [[ "$failed" -eq 0 ]]
}

run_package() {
  local import_path="$1" package
  [[ "$import_path" == "$MODULE" || "$import_path" == "$MODULE/"* ]] || {
    echo "backend-pr-race: refusing package outside $MODULE: $import_path" >&2
    exit 2
  }
  package=".${import_path#"$MODULE"}"
  [[ "$package" =~ ^\./?[A-Za-z0-9_./-]*$ && "$package" != *'..'* ]] || {
    echo "backend-pr-race: invalid package path: $package" >&2
    exit 2
  }

  case "$package" in
    ./db)
      run_race ./db '^(TestApplyMigrationAtomic.*|TestSchemaAgentRunTelemetryTerminalWriteRace|TestM147ConcurrentCanonicalCommandsConverge|TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwner)$'
      ;;
    ./handlers)
      run_race_shards ./handlers '^Test.*(Concurrent|Concurrency|Race|Atomic|BatchesReleaseWriter|RacedPoke).*$' 4
      ;;
    ./cmd/paimos)
      run_race ./cmd/paimos '^(TestRunnerControlFakeAdapterConformance|TestRunnerControlJournalSerializesPumpAndResultWriters|TestHTTPRunnerReportTransportSerializesConcurrentSequence|TestAgentRunner.*Conflict.*)$'
      ;;
    ./supervision)
      run_race ./supervision '^(TestGrantReplayAndCompetingRevocationUseM147Truth|TestThirtyTwoConcurrentCommandCreateAndConfirmConverge|TestThirtyTwoConcurrentAcceptedEffectReservationAndClaimConverge|TestInputResponseAndSupersedeRaceConvergesToOneTerminalSeal|TestInputResponseAndRunTerminalRaceConvergesToOneTerminalEvent)$'
      ;;
    ./agentmessage)
      run_race ./agentmessage '^(TestBusConcurrentIdempotencyCreatesOneMessageAndDelivery|TestBusTargetParticipatesInAtomicSecretRotation|TestEnvelopeLedgerAllowSenderIsNameScopedAndIdempotent)$'
      ;;
    ./agentmode)
      run_race ./agentmode '^TestReaderPinsCatalogBeforeCapturingClockDuringConcurrentEstimateCommit$'
      # The omitted overflow subtest enforces a five-second commit budget that
      # race instrumentation intentionally invalidates; full serial still runs
      # it. Race the stream subscription and permission-reset paths themselves.
      run_race ./agentmode '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$/(subscribe before high-water|permission grant and revoke)$'
      ;;
    *)
      run_race "$package"
      ;;
  esac
}

for import_path in "$@"; do
  if [[ "$import_path" == './...' ]]; then
    for affected in \
      "$MODULE/db" \
      "$MODULE/handlers" \
      "$MODULE/cmd/paimos" \
      "$MODULE/supervision" \
      "$MODULE/agentmessage" \
      "$MODULE/agentmode" \
      "$MODULE/agentd" \
      "$MODULE/localjournal" \
      "$MODULE/ownedprocess"
    do
      run_package "$affected"
    done
  else
    run_package "$import_path"
  fi
done
