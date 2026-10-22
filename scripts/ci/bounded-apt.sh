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

timeout_seconds() {
  local seconds=$1 remaining
  # Reserve the kill grace inside the workflow's deadline, across all retries.
  remaining=$((deadline - $(date +%s) - 20))
  if [[ $remaining -le 0 ]]; then
    echo 'dependency installation exhausted its total time budget' >&2
    return 124
  fi
  [[ $seconds -le $remaining ]] || seconds=$remaining
  printf '%s\n' "$seconds"
}

run_root_timeout() {
  local seconds
  seconds=$(timeout_seconds "$1") || return $?
  shift
  # Playwright invokes apt itself. Explicitly pass the same acquisition limits
  # through sudo, without changing the runner's global apt configuration.
  sudo -- env "PATH=$PATH" "APT_CONFIG=$apt_config" DEBIAN_FRONTEND=noninteractive \
    timeout --kill-after=20s "$seconds" "$@"
}

run_user_timeout() {
  local seconds
  seconds=$(timeout_seconds "$1") || return $?
  shift
  timeout --kill-after=20s "$seconds" "$@"
}

attempt_apt() {
  run_root_timeout 150 apt-get update -qq \
    && run_root_timeout 150 apt-get install -y -qq "$@"
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
  run_user_timeout 240 npx playwright "${install[@]}" \
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
    # Leave 30 seconds for workflow overhead before its four-minute timeout.
    budget=210
    ;;
  playwright)
    [[ $# -gt 0 ]] || usage
    budget=720
    ;;
  *) usage ;;
esac

deadline=$(($(date +%s) + budget))
apt_config=$(mktemp)
trap 'unlink "$apt_config"' EXIT
# APT_CONFIG is read before the runner's fragments. Re-read our limits as the
# main config afterwards, retaining any original main config via its include.
if [[ -f /etc/apt/apt.conf ]]; then
  printf '#include "/etc/apt/apt.conf";\n' >> "$apt_config"
fi
printf 'Dir::Etc::main "%s";\n' "$apt_config" >> "$apt_config"
cat >> "$apt_config" <<'APT'
Acquire::Retries "1";
Acquire::http::Timeout "10";
Acquire::https::Timeout "10";
DPkg::Lock::Timeout "10";
APT::Update::Error-Mode "any";
APT

for attempt in 1 2 3; do
  if "attempt_$mode" "$@"; then ok=1; break; fi
  [[ $attempt -lt 3 ]] || break
  # A killed install can leave dpkg half configured. Retrying apt alone then
  # fails permanently with exit 100, even after the mirror has recovered.
  run_root_timeout 60 dpkg --configure -a || break
done
test "$ok" = 1
