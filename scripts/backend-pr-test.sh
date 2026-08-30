#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
HANDLER_SHARDS=4
DRY_RUN=0

if [[ "${1:-}" == '--dry-run' ]]; then
  DRY_RUN=1
  shift
fi
[[ $# -gt 0 ]] || {
  echo "usage: $0 [--dry-run] <changed-package>..." >&2
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

run_handler_shards() {
  local names=() shard index pattern pid failed=0
  local pids=()
  cd "$BACKEND"
  while IFS= read -r name; do
    [[ "$name" =~ ^(Test|Fuzz)[A-Za-z0-9_]+$ ]] || {
      echo "backend-pr-test: unsafe handler test name: $name" >&2
      exit 2
    }
    names+=("$name")
  done < <("$GO_COMMAND" test -list '^(Test|Fuzz)' ./handlers | awk '/^(Test|Fuzz)[A-Za-z0-9_]+$/')
  [[ "${#names[@]}" -gt 0 ]] || {
    echo 'backend-pr-test: handlers package has no tests to shard' >&2
    exit 1
  }

  for ((shard = 0; shard < HANDLER_SHARDS; shard++)); do
    pattern='^('
    for ((index = shard; index < ${#names[@]}; index += HANDLER_SHARDS)); do
      [[ "$pattern" == '^(' ]] || pattern+='|'
      pattern+="${names[$index]}"
    done
    pattern+=')$'
    if [[ "$DRY_RUN" -eq 1 ]]; then
      printf 'go test -count=1 -timeout=8m ./handlers -run %q\n' "$pattern"
    else
      "$GO_COMMAND" test -count=1 -timeout=8m ./handlers -run "$pattern" &
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
run_handlers=0
for package in "${packages[@]}"; do
  validate_package "$package"
  if [[ "$package" == "$MODULE/handlers" ]]; then
    run_handlers=1
  else
    normal+=("$package")
  fi
done

if [[ "${#normal[@]}" -gt 0 ]]; then
  run_normal "${normal[@]}"
fi
if [[ "$run_handlers" -eq 1 ]]; then
  run_handler_shards
fi
