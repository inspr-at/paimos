#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# AEON-933: bound apt and Playwright system-dependency installs.
# timeout must be root. An unprivileged timeout cannot signal the root apt-get
# that sudo or Playwright starts, so the lock survives and the next attempt
# fails with exit 100 (PR 377, web-shard 3 and 11). Browser downloads stay
# unprivileged so they land in the runner's cache, not root's.
set -euo pipefail

usage() {
  echo "usage: bounded-apt.sh apt <command...> | playwright install-deps <browser...> | playwright install --with-deps [--only-shell] <browser...>" >&2
  exit 2
}

run_root_timeout() {
  local seconds=$1
  shift
  sudo -- env "PATH=$PATH" timeout --kill-after=20s "$seconds" "$@"
}

attempt_apt() {
  run_root_timeout 150 apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=20 -o Acquire::https::Timeout=20 update -qq \
    && run_root_timeout 150 apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=20 -o Acquire::https::Timeout=20 install -y -qq "$@"
}

attempt_playwright() {
  local arg with_deps=0 only_shell=0
  local -a targets=()
  if [[ ${1:-} == install-deps ]]; then
    shift
    [[ $# -gt 0 ]] || usage
    run_root_timeout 240 npx playwright install-deps "$@"
    return
  fi
  [[ ${1:-} == install ]] || usage
  shift
  for arg in "$@"; do
    case "$arg" in
      --with-deps) with_deps=1 ;;
      --only-shell) only_shell=1 ;;
      --*) usage ;;
      *) targets+=("$arg") ;;
    esac
  done
  [[ $with_deps -eq 1 && ${#targets[@]} -gt 0 ]] || usage
  local -a install=(install)
  [[ $only_shell -eq 1 ]] && install+=(--only-shell)
  install+=("${targets[@]}")
  # Same user as the runner: timeout can reap this download, and the cache stays theirs.
  timeout --kill-after=20s 240 npx playwright "${install[@]}" \
    && run_root_timeout 240 npx playwright install-deps "${targets[@]}"
}

[[ $# -ge 1 ]] || usage
mode=$1
shift
ok=0
case "$mode" in
  apt)
    [[ $# -gt 0 ]] || usage
    missing=0
    for tool in "$@"; do
      command -v "$tool" >/dev/null 2>&1 || missing=1
    done
    [[ $missing -eq 0 ]] && exit 0
    for _ in 1 2 3; do
      if attempt_apt "$@"; then ok=1; break; fi
    done
    ;;
  playwright)
    [[ $# -gt 0 ]] || usage
    for _ in 1 2 3; do
      if attempt_playwright "$@"; then ok=1; break; fi
    done
    ;;
  *) usage ;;
esac
test "$ok" = 1
