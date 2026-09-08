#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
RACE_GOMAXPROCS=${BACKEND_RACE_GOMAXPROCS:-2}
RACE_PACKAGE_TIMEOUT=${BACKEND_RACE_PACKAGE_TIMEOUT:-8m}
RACE_PACKAGE_TIMEOUT_MAX_MINUTES=15
LANE=all
BROAD_GROUP=all
DRY_RUN=0
SELECTED_SHARD=-1
SELECTED_SHARD_COUNT=0

[[ "$RACE_PACKAGE_TIMEOUT" =~ ^[1-9][0-9]*m$ ]] || {
  echo "backend-pr-race: invalid package timeout: $RACE_PACKAGE_TIMEOUT" >&2
  exit 2
}
(( 10#${RACE_PACKAGE_TIMEOUT%m} <= RACE_PACKAGE_TIMEOUT_MAX_MINUTES )) || {
  echo "backend-pr-race: package timeout exceeds ${RACE_PACKAGE_TIMEOUT_MAX_MINUTES}m: $RACE_PACKAGE_TIMEOUT" >&2
  exit 2
}

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
    --group=*)
      BROAD_GROUP=${1#--group=}
      case "$BROAD_GROUP" in
        core|runtime) ;;
        *)
          echo "backend-pr-race: invalid broad group: $BROAD_GROUP" >&2
          exit 2
          ;;
      esac
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
  echo "usage: $0 [--dry-run] [--lane=all|affected|db|handlers|managedharness] [--shard=INDEX/COUNT] [--group=core|runtime] <changed-package>..." >&2
  exit 2
}
if [[ "$BROAD_GROUP" != all && ( "$LANE" != all || $# -ne 1 || "$1" != './...' ) ]]; then
  echo 'backend-pr-race: --group requires lane=all and exactly ./...' >&2
  exit 2
fi

run_race() {
  local package="$1" pattern="${2:-}"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    if [[ -n "$pattern" ]]; then
      printf 'go test -race -count=1 -timeout=%s %q -run %q\n' "$RACE_PACKAGE_TIMEOUT" "$package" "$pattern"
    else
      printf 'go test -race -count=1 -timeout=%s %q\n' "$RACE_PACKAGE_TIMEOUT" "$package"
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
    GOMAXPROCS="$RACE_GOMAXPROCS" "$GO_COMMAND" test -race -count=1 -timeout="$RACE_PACKAGE_TIMEOUT" "$package" -run "$pattern"
  else
    GOMAXPROCS="$RACE_GOMAXPROCS" "$GO_COMMAND" test -race -count=1 -timeout="$RACE_PACKAGE_TIMEOUT" "$package"
  fi
}

run_one_race_pattern() {
  local package="$1" pattern="$2"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf 'go test -race -count=1 -timeout=%s %q -run %q\n' "$RACE_PACKAGE_TIMEOUT" "$package" "$pattern"
    return
  fi
  # An indexed PR job executes one shard. Grouped packages emit several
  # sequential invocations here. Exhaustive lane=all walks every shard in
  # the foreground so heavyweight SQLite contracts never contend as multiple
  # Go processes on one two-core runner.
  GOMAXPROCS="$RACE_GOMAXPROCS" "$GO_COMMAND" test -race -count=1 -timeout="$RACE_PACKAGE_TIMEOUT" "$package" -run "$pattern"
}

pattern_for_names() {
  local pattern='^(' name
  [[ $# -gt 0 ]] || {
    echo 'backend-pr-race: refusing an empty race group' >&2
    exit 1
  }
  for name in "$@"; do
    [[ "$pattern" == '^(' ]] || pattern+='|'
    pattern+="$name"
  done
  printf '%s\n' "${pattern})$"
}

run_race_shards() {
  local package="$1" match="$2" shard_count="$3" group_size="${4:-0}"
  local listed name names=() shard index shard_start=0 shard_end="$shard_count"
  local shard_names=() offset group=()
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
  [[ "$group_size" =~ ^[0-9]+$ ]] || {
    echo "backend-pr-race: invalid group size: $group_size" >&2
    exit 2
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
    shard_names=()
    for ((index = shard; index < ${#names[@]}; index += shard_count)); do
      shard_names+=("${names[$index]}")
    done
    [[ "${#shard_names[@]}" -gt 0 ]] || {
      echo "backend-pr-race: shard $shard is empty for $package" >&2
      exit 1
    }
    if (( group_size == 0 )); then
      run_one_race_pattern "$package" "$(pattern_for_names "${shard_names[@]}")"
      continue
    fi
    for ((offset = 0; offset < ${#shard_names[@]}; offset += group_size)); do
      group=("${shard_names[@]:$offset:$group_size}")
      run_one_race_pattern "$package" "$(pattern_for_names "${group[@]}")"
    done
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
    .)
      # The root routing and seed contracts rebuild six complete databases.
      # Retain every discovered test while bounding cumulative migration cost
      # on the existing affected runners or sequential all-lane processes.
      run_race_shards . '^(Test|Fuzz)' 4
      ;;
    ./db)
      run_race ./db '^(TestApplyMigrationAtomic.*|TestSchemaAgentRunTelemetryTerminalWriteRace)$'
      # Race instrumentation uses the production pool in isolated processes.
      # The exhaustive normal plan separately retains the original same-name
      # 32-connection/32-writer M147 contention oracles without weakening them.
      run_race ./db '^TestM147ConcurrentCanonicalCommandsConvergeProductionPool$'
      run_race ./db '^TestM147ConcurrentRuntimeAcceptanceHasOneEffectOwnerProductionPool$'
      ;;
    ./handlers)
      # Hosted PR 247 backend-pr-handlers-race(0) exhausted 8m on a six-test
      # shard: the new Concurrent HTTP export/confirm test shifted an Atomic
      # contract onto shard 0. Each oracle still rebuilds the migration chain
      # (~50-87s hosted). Keep the five matrix runners and split each shard
      # into sequential groups of four so every race contract still runs.
      run_race_shards ./handlers '^Test.*(Concurrent|Concurrency|Race|Atomic|BatchesReleaseWriter|RacedPoke).*$' 5 4
      ;;
    ./cmd/paimos)
      run_race ./cmd/paimos '^(TestRunnerControlFakeAdapterConformance|TestRunnerControlJournalSerializesPumpAndResultWriters|TestHTTPRunnerReportTransportSerializesConcurrentSequence|TestAgentRunner.*Conflict.*)$'
      ;;
    ./supervision)
      run_race ./supervision '^(TestGrantReplayAndCompetingRevocationUseM147Truth|TestThirtyTwoConcurrentCommandCreateAndConfirmConverge|TestThirtyTwoConcurrentAcceptedEffectReservationAndClaimConverge|TestInputResponseAndSupersedeRaceConvergesToOneTerminalSeal|TestInputResponseAndRunTerminalRaceConvergesToOneTerminalEvent)$'
      ;;
    ./agentmessage)
      run_race ./agentmessage '^(TestBusConcurrentIdempotencyCreatesOneMessageAndDelivery|TestBusTargetParticipatesInAtomicSecretRotation|TestEnvelopeLedgerAllowSenderIsNameScopedAndIdempotent|TestAttentionProjectionCoalescedLeaseAndCrashSafeAck|TestConcurrentAttentionListenersLeaseOneBatch)$'
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
    ./externalstage)
      # Every external-stage test rebuilds the complete SQLite migration chain.
      # Keep PR race instrumentation on the actual concurrent create oracle, the
      # restart/replay lifecycle, and the new v2 persistence/replay boundary.
      # Normal PR and exhaustive workflows retain the full serial package.
      run_race ./externalstage '^(TestConcurrentCreateCommitsOneHandoffAndOneReplay|TestServiceJanusDependencyIsAtomicAndCannotOwnCanonicalStage|TestServiceOwnerLifecycleReplayHeartbeatAndRestart|TestServiceReportV2PersistsExplicitReleaseIdentityAndBindsReplay)$'
      ;;
    ./lifecycleintents)
      # Every lifecycle fixture rebuilds the complete migration chain. Keep all
      # discovered tests, but spread their cumulative race cost over the four
      # existing affected runners (or four sequential processes in lane=all).
      run_race_shards ./lifecycleintents '^(Test|Fuzz)' 4
      ;;
    ./delivery)
      # Every delivery fixture rebuilds the complete migration chain. Keep every
      # discovered test on the existing four affected runners, then split each
      # runner's tests into sequential groups of four. Hosted CI 34175171447
      # still exhausted 8m on 12-test baselinebatch shards; delivery shares the
      # same Open()/migrateThrough cost.
      run_race_shards ./delivery '^(Test|Fuzz)' 4 4
      ;;
    ./baselinebatch)
      # Hosted CI 34175171447 (jobs 101903029365/374/438, shards 3/2/0) timed
      # out at 8m after five completed migration fixtures, 28-47s into the next
      # (RetryAfterJanusDivergence / IssuedHandoffThenJanusChange /
      # PartialBuiltIdentity) still in migrateThrough. ~87s/test hosted; a
      # group of four is ~346s vs 480s. Keep every test; do not raise timeout.
      run_race_shards ./baselinebatch '^(Test|Fuzz)' 4 4
      ;;
    ./releaseacceptance)
      # Every release-acceptance fixture rebuilds the complete migration chain.
      # Hosted PR 247 backend-pr-race(0) exhausted 8m on the unsharded package
      # while TestRevokedSessionCannotConfirm was 54s into openFixture
      # (migrateThrough ~169/186). Same ~50-87s/test hosted cost as delivery;
      # groups of four stay under 480s. Keep every test; do not raise timeout.
      run_race_shards ./releaseacceptance '^(Test|Fuzz)' 4 4
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
        if [[ "$import_path" == "$MODULE" ||
              "$import_path" == "$MODULE/lifecycleintents" ||
              "$import_path" == "$MODULE/delivery" ||
              "$import_path" == "$MODULE/baselinebatch" ||
              "$import_path" == "$MODULE/releaseacceptance" ]] || (( affected_index % SELECTED_SHARD_COUNT == SELECTED_SHARD )); then
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
    # One membership list owns the default broad plan and both exhaustive
    # groups. Each group stays sequential on its own CI runner.
    for affected in \
      "core:$MODULE/db" \
      "core:$MODULE/handlers" \
      "core:$MODULE/cmd/paimos" \
      "core:$MODULE/supervision" \
      "core:$MODULE/agentmessage" \
      "core:$MODULE/managedharness" \
      "core:$MODULE/agentmode" \
      "core:$MODULE/agentd" \
      "core:$MODULE/localjournal" \
      "core:$MODULE/ownedprocess" \
      "runtime:$MODULE/lifecycleintents" \
      "runtime:$MODULE/lifecycleclient" \
      "runtime:$MODULE/runtimeconsumer" \
      "runtime:$MODULE/runtimehealth" \
      "runtime:$MODULE"
    do
      if [[ "$BROAD_GROUP" == all || "$BROAD_GROUP" == "${affected%%:*}" ]]; then
        run_selected_package "${affected#*:}"
      fi
    done
  else
    run_selected_package "$import_path"
  fi
done
