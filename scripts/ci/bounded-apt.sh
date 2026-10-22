#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# AEON-933: bound apt and Playwright system-dependency installs.
# timeout must be root. An unprivileged timeout cannot signal the root apt-get
# that sudo or Playwright starts, so the lock survives and the next attempt
# fails with exit 100 (PR 377, web-shard 3 and 11). Browser downloads stay
# unprivileged so they land in the runner's cache, not root's.
# AEON-1652: retry fetch errors once via temporary Ubuntu mirror overrides.
set -euo pipefail

usage() {
  echo "usage: bounded-apt.sh apt <command...> | playwright install-deps <browser...> | playwright install --with-deps [--only-shell] <browser...>" >&2
  exit 2
}

run_root_timeout() {
  local seconds=$1
  shift
  run_timeout root "$seconds" "$@"
}

run_timeout() {
  local user=$1 seconds=$2 remaining
  shift 2
  remaining=$((deadline - $(date +%s)))
  [[ $remaining -gt 0 ]] || return 124
  [[ $seconds -le $remaining ]] || seconds=$remaining
  # Put timeout after sudo/env so it can reap apt's root process group.
  if [[ $user == root ]]; then
    sudo -- env "PATH=$PATH" "APT_CONFIG=$apt_config" LC_ALL=C timeout --kill-after=20s "$seconds" "$@"
  else
    timeout --kill-after=20s "$seconds" "$@"
  fi
}

attempt_apt() {
  run_root_timeout 150 apt-get update -qq \
    && run_root_timeout 150 apt-get install -y -qq "$@"
}

parse_playwright() {
  local arg with_deps=0 only_shell=0
  if [[ ${1:-} == install-deps ]]; then
    shift
    [[ $# -gt 0 ]] || usage
    targets=("$@")
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
  browser=(npx playwright install)
  [[ $only_shell -eq 1 ]] && browser+=(--only-shell)
  browser+=("${targets[@]}")
}

attempt_playwright() {
  run_root_timeout 240 npx playwright install-deps "${targets[@]}"
}

write_config() {
  cat > "$apt_config" <<EOF
Acquire::Retries "3";
Acquire::http::Timeout "20";
Acquire::https::Timeout "20";
APT::Update::Error-Mode "any";
Dir::Etc::sourcelist "$1/sources.list";
Dir::Etc::sourceparts "$1/sources.list.d";
EOF
}

fetch_failure() {
  # Ignore Playwright's final wrapper error, but reject apt/dpkg errors even
  # when an earlier fetch in the same attempt printed Ign or Err lines.
  if grep -E '^E:|^dpkg: error' "$log" | grep -Ev '^E: (Failed to fetch https?://|Unable to fetch some archives|Some index files failed to download)' >/dev/null; then
    return 1
  fi
  grep -Eq '^((W|E): Failed to fetch https?://|E: Unable to fetch some archives|Ign:[0-9]+ https?://|Err:[0-9]+ https?://)' "$log"
}

fallback_sources() {
  # Ubuntu 24.04+ uses deb822. Copy sources rather than editing /etc/apt;
  # split mixed Suites so only -security uses security.ubuntu.com. Keep
  # signing keys, components, options and third-party repositories intact.
  python3 - "$source_root" "$temporary/apt" <<'PY'
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit

source, target = map(Path, sys.argv[1:])
parts = target / 'sources.list.d'
parts.mkdir(parents=True)
(target / 'sources.list').touch()
host = re.compile(r'(?<=://)(?:(?:[a-zA-Z0-9-]+\.)?archive|security)\.ubuntu\.com(?=[:/\s]|$)')
changed = False

def mirror(text, security):
    # GitHub-hosted Ubuntu runners use a local apt mirror list, rather than
    # naming Azure directly in their sources. Only replace lists containing
    # Ubuntu archive hosts; vendor mirror lists remain byte-for-byte intact.
    if text.startswith('mirror+file:'):
        location = urlsplit(text[len('mirror+'):])
        if location.netloc not in ('', 'localhost'):
            return text
        try:
            with Path(unquote(location.path)).open() as listing:
                contents = listing.read(65537)
        except OSError:
            return text
        if len(contents) <= 65536 and any(host.search(line.split('#', 1)[0]) for line in contents.splitlines()):
            return 'https://' + ('security.ubuntu.com' if security else 'archive.ubuntu.com') + '/ubuntu/'
        return text
    return host.sub('security.ubuntu.com' if security else 'archive.ubuntu.com', text)

def mirror_uris(text, security):
    return re.sub(r'\S+', lambda match: mirror(match[0], security), text)

def field(block, name):
    return re.search(rf'^{name}:[^\n]*(?:\n[ \t]+[^\n]*)*', block, re.M | re.I)

def legacy(line):
    entry = re.match(r'\s*deb(?:-src)?\s+(?:\[[^\]]*\]\s+)?(\S+)\s+(\S+)', line)
    if not entry:
        return line
    return line[:entry.start(1)] + mirror(entry[1], entry[2].endswith('-security')) + line[entry.end(1):]

files = [source / 'sources.list', *sorted((source / 'sources.list.d').glob('*.list')),
         *sorted((source / 'sources.list.d').glob('*.sources'))]
for path in files:
    if not path.is_file():
        continue
    original = path.read_text()
    if path.suffix == '.sources':
        blocks = []
        for block in re.split(r'\n[ \t]*\n', original):
            uris, suites = field(block, 'URIs'), field(block, 'Suites')
            if not uris or not suites or not any(mirror_uris(uris[0], security) != uris[0] for security in (False, True)):
                blocks.append(block)
                continue
            names = suites[0].split(':', 1)[1].split()
            for security in (False, True):
                selected = [name for name in names if name.endswith('-security') == security]
                if selected:
                    copy = block[:suites.start()] + 'Suites: ' + ' '.join(selected) + block[suites.end():]
                    uri = field(copy, 'URIs')
                    blocks.append(copy[:uri.start()] + mirror_uris(uri[0], security) + copy[uri.end():])
        rewritten = '\n\n'.join(blocks)
    else:
        rewritten = ''.join(legacy(line) for line in original.splitlines(keepends=True))
    changed |= rewritten != original
    destination = target / 'sources.list' if path == source / 'sources.list' else parts / path.name
    destination.write_text(rewritten)
sys.exit(0 if changed else 1)
PY
}

capture_attempt() {
  local -a statuses
  "${attempt[@]}" 2>&1 | tee "$log"
  statuses=("${PIPESTATUS[@]}")
  [[ ${statuses[0]} -eq 0 ]] || return "${statuses[0]}"
  return "${statuses[1]}"
}

[[ $# -ge 1 ]] || usage
mode=$1
shift
targets=()
browser=()
case "$mode" in
  apt)
    [[ $# -gt 0 ]] || usage
    missing=0
    for tool in "$@"; do
      command -v "$tool" >/dev/null 2>&1 || missing=1
    done
    [[ $missing -eq 0 ]] && exit 0
    budget=210
    attempt=(attempt_apt "$@")
    ;;
  playwright)
    [[ $# -gt 0 ]] || usage
    parse_playwright "$@"
    budget=720
    attempt=(attempt_playwright)
    ;;
  *) usage ;;
esac

# Both mirrors and the optional browser download share this budget. The final
# root timeout has a 20-second kill grace, still inside the workflow step limit.
deadline=$(($(date +%s) + budget))
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
chmod 755 "$temporary"
apt_config=$temporary/apt.conf
log=$temporary/attempt.log
source_root=${AEON_APT_SOURCES_DIR:-/etc/apt}
write_config "$source_root"

# Same user as the runner: browser downloads land in their cache and happen
# only once, even when the subsequent dependency fetch needs another mirror.
if [[ ${#browser[@]} -gt 0 ]]; then run_timeout user 240 "${browser[@]}"; fi
source_files=()
for file in "$source_root/sources.list" "$source_root/sources.list.d/"*.list "$source_root/sources.list.d/"*.sources; do
  [[ ! -f $file ]] || source_files+=("$file")
done
mirrors='configured apt sources'
if [[ ${#source_files[@]} -gt 0 ]]; then
  mirrors=$(grep -hEo 'https?://(([[:alnum:]-]+\.)?archive|security)\.ubuntu\.com|mirror\+file:[^[:space:]]+' "${source_files[@]}" | sed -E 's#https?://##' | sort -u | paste -sd, -) || true
fi
echo "bounded-apt: using ${mirrors:-configured apt sources}"
if capture_attempt; then
  echo "bounded-apt: dependencies installed via ${mirrors:-configured apt sources}"
  exit 0
else
  status=$?
fi
fetch_failure || exit "$status"
if ! fallback_sources; then
  echo 'bounded-apt: no alternate Ubuntu sources available' >&2
  exit "$status"
fi
write_config "$temporary/apt"
echo 'bounded-apt: retrying fetch once via archive.ubuntu.com (security.ubuntu.com for -security)'
if capture_attempt; then
  echo 'bounded-apt: dependencies installed via archive.ubuntu.com / security.ubuntu.com'
else
  exit "$?"
fi
