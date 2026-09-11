#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
AFFECTED_SHARDS=2
DB_SHARDS=4
HANDLER_SHARDS=5
# PAI-999: handler shard 3 measured 6 to 9 minutes on slow runners;
# give shards more headroom than run_normal's 8m timeout.
SHARD_TIMEOUT=15m
LANE=affected
DRY_RUN=0
SELECTED_SHARD=-1
SELECTED_SHARD_COUNT=0
DIRECT_PACKAGES=
RACE_COVERAGE=
BROAD_SELECTION=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --direct-packages=*)
      DIRECT_PACKAGES=${1#--direct-packages=}
      shift
      ;;
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
        echo "backend-pr-test: invalid shard: $shard_spec" >&2
        exit 2
      }
      SELECTED_SHARD=$((10#${shard_spec%/*}))
      SELECTED_SHARD_COUNT=$((10#${shard_spec#*/}))
      (( SELECTED_SHARD < SELECTED_SHARD_COUNT )) || {
        echo "backend-pr-test: shard index is outside count: $shard_spec" >&2
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
  affected|db|handlers|performance) ;;
  *)
    echo "backend-pr-test: invalid lane: $LANE" >&2
    exit 2
    ;;
esac
if (( SELECTED_SHARD >= 0 )) && [[ "$LANE" != affected && "$LANE" != handlers ]]; then
  echo "backend-pr-test: --shard is supported only for the affected and handlers lanes" >&2
  exit 2
fi
if [[ "$LANE" == affected || "$LANE" == handlers ]] && (( SELECTED_SHARD < 0 )); then
  echo "backend-pr-test: $LANE lane requires exactly one --shard=INDEX/COUNT" >&2
  exit 2
fi
[[ $# -gt 0 ]] || {
  echo "usage: $0 [--direct-packages=NEWLINE_SELECTION] [--dry-run] [--lane=affected|db|handlers|performance] [--shard=INDEX/COUNT] <affected-package>..." >&2
  exit 2
}

validate_package() {
  local package="$1"
  [[ "$package" == "$MODULE" || "$package" == "$MODULE/"* ]] || {
    echo "backend-pr-test: refusing package outside $MODULE: $package" >&2
    exit 2
  }
}

# A newline-separated direct selection, independently computed with the same
# PR base/head as the race jobs. Their full affected coverage drives dedupe.
# Missing selection conservatively keeps normal tests.
load_race_coverage() {
  [[ -n "$DIRECT_PACKAGES" && $# -gt 0 ]] || return 0
  if (( BROAD_SELECTION )); then
    # The full-tree fallback races a curated broad package set. Do not infer
    # race ownership for every package expanded by the normal selector.
    RACE_COVERAGE=$("$ROOT/scripts/backend-pr-race.sh" --coverage --direct-packages="$DIRECT_PACKAGES" './...')
  else
    RACE_COVERAGE=$("$ROOT/scripts/backend-pr-race.sh" --coverage --direct-packages="$DIRECT_PACKAGES" "$@")
  fi
  # Retain the guarded packages and their transitive importers, including
  # test-only imports. Other packages use the same affected race plan.
  race_exclusions=$(python3 "$ROOT/scripts/backend-race-exclusions.py" "$BACKEND")
  filtered_coverage=
  while IFS=$'\t' read -r package pattern; do
    [[ -n "$package" ]] || continue
    if grep -Fxq "$package" <<<"$race_exclusions"; then continue; fi
    filtered_coverage+="$package"$'\t'"$pattern"$'\n'
  done <<<"$RACE_COVERAGE"
  RACE_COVERAGE=$filtered_coverage
}

race_skip() {
  local wanted=".${1#"$MODULE"}" package pattern skip=
  [[ "$1" == . || "$1" == ./* ]] && wanted="$1"
  while IFS=$'\t' read -r package pattern; do
    [[ "$package" == "$wanted" && -n "$pattern" ]] || continue
    # A subtest selector does not cover the parent's other assertions.
    [[ "$pattern" != */* ]] || continue
    [[ -z "$skip" ]] || skip+='|'
    skip+="$pattern"
  done <<<"$RACE_COVERAGE"
  printf '%s' "$skip"
}

run_normal() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf 'go test -count=1 -timeout=8m'
    printf ' %q' "$@"
    printf '\n'
    return
  fi
  cd "$BACKEND"
  "$GO_COMMAND" test -count=1 -timeout=8m "$@"
}

run_shards() {
  local package="$1" match="$2" shard_count="$3" label="$4"
  local listed name names=() shard index pattern pid failed=0 shard_start=0 shard_end="$shard_count"
  local pids=() skip
  skip=$(race_skip "$package")
  cd "$BACKEND"
  listed=$("$GO_COMMAND" test -list "$match" "$package")
  while IFS= read -r name; do
    case "$name" in
      Test*|Fuzz*)
        [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] || {
          echo "backend-pr-test: unsafe $label test name: $name" >&2
          exit 2
        }
        if [[ -z "$skip" || ! "$name" =~ $skip ]]; then
          names+=("$name")
        fi
        ;;
    esac
  done <<<"$listed"
  [[ "${#names[@]}" -gt 0 || -z "$skip" ]] || return 0
  [[ "${#names[@]}" -gt 0 ]] || {
    echo "backend-pr-test: $label package has no tests to shard" >&2
    exit 1
  }

  if (( SELECTED_SHARD >= 0 )); then
    [[ "$SELECTED_SHARD_COUNT" -eq "$shard_count" ]] || {
      echo "backend-pr-test: $label shard count=$SELECTED_SHARD_COUNT, want $shard_count" >&2
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
    # Dedupe can legitimately empty an individual shard.
    [[ "$pattern" != '^(' ]] || continue
    pattern+=')$'
    if [[ "$DRY_RUN" -eq 1 ]]; then
      printf 'go test -count=1 -timeout=%s %q -run %q\n' "$SHARD_TIMEOUT" "$package" "$pattern"
    else
      "$GO_COMMAND" test -count=1 -timeout="$SHARD_TIMEOUT" "$package" -run "$pattern" &
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

packages=("$@")
if [[ " ${packages[*]} " == *' ./... '* ]]; then
  BROAD_SELECTION=1
  listed_packages=$(cd "$BACKEND" && "$GO_COMMAND" list ./...)
  packages=()
  while IFS= read -r package; do
    [[ -z "$package" ]] || packages+=("$package")
  done <<<"$listed_packages"
fi

normal=()
for package in "${packages[@]}"; do
  validate_package "$package"
done

has_package() {
  local wanted="$1" package
  for package in "${packages[@]}"; do
    [[ "$package" == "$wanted" ]] && return 0
  done
  return 1
}

case "$LANE" in
  affected)
    [[ "$SELECTED_SHARD_COUNT" -eq "$AFFECTED_SHARDS" ]] || {
      echo "backend-pr-test: affected shard count=$SELECTED_SHARD_COUNT, want $AFFECTED_SHARDS" >&2
      exit 2
    }
    normal_index=0
    for package in "${packages[@]}"; do
      case "$package" in
        "$MODULE/db"|"$MODULE/handlers") ;;
        *)
          if (( normal_index % AFFECTED_SHARDS == SELECTED_SHARD )); then
            normal+=("$package")
          fi
          normal_index=$((normal_index + 1))
          ;;
      esac
    done
    # Query only this lane's packages, preserving the same per-package mask.
    load_race_coverage ${normal[@]+"${normal[@]}"}
    unfiltered=()
    for package in ${normal[@]+"${normal[@]}"}; do
      skip=$(race_skip "$package")
      if [[ -z "$skip" ]]; then
        unfiltered+=("$package")
        continue
      fi
      # Only elide an invocation after checking every discovered oracle.
      # The skip expression contains exact names from the race execution plan.
      listed=$(cd "$BACKEND" && "$GO_COMMAND" test -list '^(Test|Fuzz|Example)' "$package")
      remaining=0
      while IFS= read -r name; do
        if [[ "$name" =~ ^(Test|Fuzz|Example)[A-Za-z0-9_]*$ && ! "$name" =~ $skip ]]; then
          remaining=1
        fi
      done <<<"$listed"
      (( remaining )) || continue
      if [[ "$package" == "$MODULE/agentmode" ]]; then
        [[ -z "$skip" ]] || skip+='|'
        skip+='^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$'
      fi
      run_normal "$package" -skip "$skip"
    done
    if (( ${#unfiltered[@]} > 0 )); then
      # Preserve Go's package concurrency for ordinary reverse dependents.
      run_normal -skip '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$' "${unfiltered[@]}"
    fi
    if [[ " ${normal[*]-} " == *" $MODULE/agentmode "* ]] &&
       ! grep -Fxq $'./agentmode\t^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$/(subscribe before high-water|permission grant and revoke)$' <<<"$RACE_COVERAGE"; then
      run_normal ./agentmode -run '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$/(subscribe before high-water|permission grant and revoke)$'
    fi
    ;;
  db)
    if has_package "$MODULE/db"; then
      load_race_coverage "$MODULE/db"
      run_shards ./db '^(Test|Fuzz)' "$DB_SHARDS" db
    fi
    ;;
  handlers)
    if has_package "$MODULE/handlers"; then
      load_race_coverage "$MODULE/handlers"
      run_shards ./handlers '^(Test|Fuzz)' "$HANDLER_SHARDS" handlers
    fi
    ;;
  performance)
    if has_package "$MODULE/agentmode"; then
      run_normal ./agentmode -run '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$/^overflow lost wake coalescing and restart$'
    fi
    ;;
esac
