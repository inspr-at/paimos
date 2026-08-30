#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
DB_SHARDS=4
HANDLER_SHARDS=5
LANE=affected
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
if (( SELECTED_SHARD >= 0 )) && [[ "$LANE" != handlers ]]; then
  echo "backend-pr-test: --shard is supported only for the handlers lane" >&2
  exit 2
fi
if [[ "$LANE" == handlers ]] && (( SELECTED_SHARD < 0 )); then
  echo "backend-pr-test: handlers lane requires exactly one --shard=INDEX/COUNT" >&2
  exit 2
fi
[[ $# -gt 0 ]] || {
  echo "usage: $0 [--dry-run] [--lane=affected|db|handlers|performance] [--shard=INDEX/COUNT] <affected-package>..." >&2
  exit 2
}

validate_package() {
  local package="$1"
  [[ "$package" == "$MODULE" || "$package" == "$MODULE/"* ]] || {
    echo "backend-pr-test: refusing package outside $MODULE: $package" >&2
    exit 2
  }
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
  local names=() shard index pattern pid failed=0 shard_start=0 shard_end="$shard_count"
  local pids=()
  cd "$BACKEND"
  while IFS= read -r name; do
    [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] || {
      echo "backend-pr-test: unsafe $label test name: $name" >&2
      exit 2
    }
    names+=("$name")
  done < <("$GO_COMMAND" test -list "$match" "$package" | awk '/^(Test|Fuzz)[A-Za-z0-9_]+$/')
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
    pattern+=')$'
    if [[ "$DRY_RUN" -eq 1 ]]; then
      printf 'go test -count=1 -timeout=8m %q -run %q\n' "$package" "$pattern"
    else
      "$GO_COMMAND" test -count=1 -timeout=8m "$package" -run "$pattern" &
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
  packages=()
  while IFS= read -r package; do
    packages+=("$package")
  done < <(cd "$BACKEND" && "$GO_COMMAND" list ./...)
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
    for package in "${packages[@]}"; do
      case "$package" in
        "$MODULE/db"|"$MODULE/handlers") ;;
        *) normal+=("$package") ;;
      esac
    done
    if [[ "${#normal[@]}" -gt 0 ]]; then
      # The overflow subtest owns a five-second performance SLO. It gets an
      # otherwise-idle required runner below; package contention must neither
      # relax that budget nor turn it into a false failure here.
      run_normal -skip '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$' "${normal[@]}"
    fi
    if has_package "$MODULE/agentmode"; then
      run_normal ./agentmode -run '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$/(subscribe before high-water|permission grant and revoke)$'
    fi
    ;;
  db)
    if has_package "$MODULE/db"; then
      run_shards ./db '^(Test|Fuzz)' "$DB_SHARDS" db
    fi
    ;;
  handlers)
    if has_package "$MODULE/handlers"; then
      run_shards ./handlers '^(Test|Fuzz)' "$HANDLER_SHARDS" handlers
    fi
    ;;
  performance)
    if has_package "$MODULE/agentmode"; then
      run_normal ./agentmode -run '^TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges$/^overflow lost wake coalescing and restart$'
    fi
    ;;
esac
