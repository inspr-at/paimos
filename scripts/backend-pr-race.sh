#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
RACE_GOMAXPROCS=${BACKEND_RACE_GOMAXPROCS:-2}
LANE=all
DRY_RUN=0
SELECTED_SHARD=-1
SELECTED_SHARD_COUNT=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --lane=*)
      LANE=${1#--lane=}
      shift
      ;;
    --shard=*)
      shard_spec=${1#--shard=}
      [[ "$shard_spec" =~ ^[0-9]+/[1-9][0-9]*$ ]] || {
        echo "backend-pr-race: invalid shard: $shard_spec" >&2
        exit 2
      }
      SELECTED_SHARD=$((10#${shard_spec%/*}))
      SELECTED_SHARD_COUNT=$((10#${shard_spec#*/}))
      (( SELECTED_SHARD < SELECTED_SHARD_COUNT )) || {
        echo "backend-pr-race: shard index is outside count: $shard_spec" >&2
        exit 2
      }
      shift
      ;;
    *)
      break
      ;;
  esac
done
case "$LANE" in
  all|affected|db|handlers|managedharness) ;;
  *)
    echo "backend-pr-race: invalid lane: $LANE" >&2
    exit 2
    ;;
esac
case "$LANE" in
  affected)
    [[ "$SELECTED_SHARD_COUNT" -eq 4 ]] || {
      echo "backend-pr-race: affected lane requires exactly one --shard=INDEX/4" >&2
      exit 2
    }
    ;;
  handlers)
    [[ "$SELECTED_SHARD_COUNT" -eq 5 ]] || {
      echo "backend-pr-race: handlers lane requires exactly one --shard=INDEX/5" >&2
      exit 2
    }
    ;;
  managedharness)
    [[ "$SELECTED_SHARD_COUNT" -eq 7 ]] || {
      echo "backend-pr-race: managedharness lane requires exactly one --shard=INDEX/7" >&2
      exit 2
    }
    ;;
  *)
    (( SELECTED_SHARD < 0 )) || {
      echo "backend-pr-race: --shard is unsupported for the $LANE lane" >&2
      exit 2
    }
    ;;
esac
[[ $# -gt 0 ]] || {
  echo "usage: $0 [--dry-run] [--lane=all|affected|db|handlers|managedharness] [--shard=INDEX/COUNT] <changed-package>..." >&2
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
    GOMAXPROCS="$RACE_GOMAXPROCS" "$GO_COMMAND" test -race -count=1 -timeout=8m "$package" -run "$pattern"
  else
    GOMAXPROCS="$RACE_GOMAXPROCS" "$GO_COMMAND" test -race -count=1 -timeout=8m "$package"
  fi
}

run_race_shards() {
  local package="$1" match="$2" shard_count="$3"
  local listed name names=() shard index pattern shard_start=0 shard_end="$shard_count"
  cd "$BACKEND"
  listed=$("$GO_COMMAND" test -list "$match" "$package")
  while IFS= read -r name; do
    case "$name" in
      Test*|Fuzz*)
        [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] || {
          echo "backend-pr-race: unsafe test name for $package: $name" >&2
          exit 2
        }
        names+=("$name")
        ;;
    esac
  done <<<"$listed"
  [[ "${#names[@]}" -gt 0 ]] || {
    echo "backend-pr-race: no tests matched $match in $package" >&2
    exit 1
  }

  if (( SELECTED_SHARD >= 0 )); then
    [[ "$SELECTED_SHARD_COUNT" -eq "$shard_count" ]] || {
      echo "backend-pr-race: shard count=$SELECTED_SHARD_COUNT, want $shard_count" >&2
      exit 2
    }
    shard_start=$SELECTED_SHARD
    shard_end=$((SELECTED_SHARD + 1))
  fi

  for ((shard = shard_start; shard < shard_end; shard++)); do
    pattern='^('
    for ((index = shard; index < ${#names[@]}; index += shard_count)); do
      [[ "$pattern" == '^(' ]] || pattern+='|'
      pattern+="${names[$index]}"
    done
    [[ "$pattern" != '^(' ]] || {
      echo "backend-pr-race: shard $shard is empty for $package" >&2
      exit 1
    }
    pattern+=')$'
    if [[ "$DRY_RUN" -eq 1 ]]; then
      printf 'go test -race -count=1 -timeout=8m %q -run %q\n' "$package" "$pattern"
    else
      # An indexed PR job executes one shard. The exhaustive workflow walks all
      # shards here in the foreground so heavyweight SQLite contracts never
      # contend as multiple Go processes on one two-core runner.
      GOMAXPROCS="$RACE_GOMAXPROCS" "$GO_COMMAND" test -race -count=1 -timeout=8m "$package" -run "$pattern"
    fi
  done
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
      run_race ./db '^(TestApplyMigrationAtomic.*|TestSchemaAgentRunTelemetryTerminalWriteRace)$'
      # Race instrumentation uses the production pool in isolated processes.
      # The exhaustive normal plan separately retains the original same-name
      # 32-connection/32-writer M147 contention oracles without weakening them.
      run_race ./db '^TestM147ConcurrentCanonicalCommandsConvergeProductionPool$'
      run_race ./db '^TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwnerProductionPool$'
      ;;
    ./handlers)
      run_race_shards ./handlers '^Test.*(Concurrent|Concurrency|Race|Atomic|BatchesReleaseWriter|RacedPoke).*$' 5
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
    ./auth)
      # The exhaustive auth package rebuilds the complete migration chain for
      # every test and cannot fit the bounded race lane. Keep the package-local
      # SQLite contention proof here. The related recent-usage contract keeps
      # its 500 ms latency oracle in the exhaustive serial suite; race
      # instrumentation invalidates that wall-clock budget.
      run_race ./auth '^TestResolveAPIKeyUsageStampNeverInheritsSQLiteBusyTimeout$'
      ;;
    ./managedharness)
      # Every managed-harness test rebuilds the complete SQLite migration chain.
      # Keep PR race instrumentation on the package's actual concurrency and
      # crash-replay oracles; normal PR and exhaustive workflows retain all
      # serial registration, routing, authorization, and lifecycle contracts.
      # A new concurrency/recovery oracle must use one of these semantic name
      # markers (or add an explicit alternative) and update the shard contract.
      managedharness_match='^(TestStoppedSessionCanRegisterNewActiveGeneration|Test.*(Concurrent|Concurrency|Race|Atomic|Replay|Recovers).*)$'
      run_race_shards ./managedharness "$managedharness_match" 7
      ;;
    *)
      run_race "$package"
      ;;
  esac
}

run_selected_package() {
  local import_path="$1"
  case "$LANE" in
    all)
      run_package "$import_path"
      ;;
    affected)
      if [[ "$import_path" != "$MODULE/db" && "$import_path" != "$MODULE/handlers" && "$import_path" != "$MODULE/managedharness" ]]; then
        if (( affected_index % SELECTED_SHARD_COUNT == SELECTED_SHARD )); then
          run_package "$import_path"
        fi
        affected_index=$((affected_index + 1))
      fi
      ;;
    db)
      [[ "$import_path" != "$MODULE/db" ]] || run_package "$import_path"
      ;;
    handlers)
      [[ "$import_path" != "$MODULE/handlers" ]] || run_package "$import_path"
      ;;
    managedharness)
      [[ "$import_path" != "$MODULE/managedharness" ]] || run_package "$import_path"
      ;;
  esac
}

affected_index=0
for import_path in "$@"; do
  if [[ "$import_path" == './...' ]]; then
    for affected in \
      "$MODULE/db" \
      "$MODULE/handlers" \
      "$MODULE/cmd/paimos" \
      "$MODULE/supervision" \
      "$MODULE/agentmessage" \
      "$MODULE/managedharness" \
      "$MODULE/agentmode" \
      "$MODULE/agentd" \
      "$MODULE/localjournal" \
      "$MODULE/ownedprocess"
    do
      run_selected_package "$affected"
    done
  else
    run_selected_package "$import_path"
  fi
done
