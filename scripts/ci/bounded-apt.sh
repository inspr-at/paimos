#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# AEON-933: bound apt and Playwright system-dependency installs.
# timeout must be root. An unprivileged timeout cannot signal the root apt-get
# that sudo or Playwright starts, so the lock survives and the next attempt
# fails with exit 100 (PR 377, web-shard 3 and 11). Browser downloads stay
# unprivileged so they land in the runner's cache, not root's.
# AEON-1652: share one image-specific package bundle; retain mirror fallback.
set -euo pipefail

usage() {
  echo "usage: bounded-apt.sh bundle <directory> | apt <command...> | playwright install-deps <browser...> | playwright install --with-deps [--only-shell] <browser...>" >&2
  exit 2
}

run_root_timeout() {
  local seconds=$1
  shift
  run_timeout root "$seconds" "$@"
}

run_timeout() {
  # Reserve the final SIGKILL grace inside the shared wall-clock budget.
  local user=$1 seconds=$2 remaining effective_deadline=$((deadline - 20))
  shift 2
  if [[ $user == root && $attempt_deadline -lt $effective_deadline ]]; then
    effective_deadline=$attempt_deadline
  fi
  remaining=$((effective_deadline - $(date +%s)))
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
  # Refresh indexes through the override before Playwright resolves packages.
  if [[ $retry -eq 1 ]]; then
    run_root_timeout 150 apt-get update -qq || return "$?"
  fi
  run_root_timeout 240 npx playwright install-deps "${targets[@]}"
}

resolve_bundle_packages() {
  # Since Playwright 1.60, dry-run prints only MISSING packages. Capture the
  # complete argument list of its simulated apt install, including libraries
  # already on the image. This adapter cannot update or install anything.
  mkdir "$temporary/dry-run"
  cat > "$temporary/dry-run/apt-get" <<'PY'
#!/usr/bin/env python3
import os
from pathlib import Path
import re
import sys

args = sys.argv[1:]
if args[:3] != ['install', '-s', '--no-install-recommends']:
    sys.exit('bounded-apt: unexpected Playwright dry-run command')
packages = args[3:]
if not 1 <= len(packages) <= 256 or not all(re.fullmatch(r'[a-z0-9][a-z0-9+.-]*(?<!-)', p) for p in packages):
    sys.exit('bounded-apt: invalid Playwright package list')
Path(os.environ['AEON_APT_PACKAGE_LIST']).write_text('\n'.join(sorted(set(packages + ['fish', 'zsh']))) + '\n')
PY
  chmod +x "$temporary/dry-run/apt-get"
  if ! run_timeout user 90 env "PATH=$temporary/dry-run:$PATH" \
    "AEON_APT_PACKAGE_LIST=$bundle/packages.txt" \
    npx --no-install playwright install-deps --dry-run chromium > "$temporary/playwright-dry-run.log" 2>&1; then
    cat "$temporary/playwright-dry-run.log" >&2
    return 1
  fi
  [[ -s $bundle/packages.txt ]] || return 1
  while IFS= read -r package; do packages+=("$package"); done < "$bundle/packages.txt"
  echo "bounded-apt: resolved ${#packages[@]} Chromium and shell packages via Playwright dry-run"
}

attempt_bundle() {
  run_root_timeout 150 apt-get update -qq \
    && run_root_timeout 360 apt-get install --download-only --reinstall -y -qq --no-install-recommends "${packages[@]}"
}

bundle_config() {
  # Package indexes and archives never use the image's mutable shared cache.
  cat >> "$apt_final_config" <<EOF
Dir::State::lists "$bundle/lists";
Dir::Cache::archives "$bundle/archives";
Dir::Cache::pkgcache "";
Dir::Cache::srcpkgcache "";
EOF
}

bundle_manifest() {
  run_timeout user 45 python3 - "$1" "$bundle" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import sys

mode, directory = sys.argv[1:]
root = Path(directory)
manifest = root / 'manifest.json'
image = [os.environ.get('ImageOS'), os.environ.get('ImageVersion'), os.environ.get('RUNNER_ARCH', platform.machine())]
try:
    if not all(image):
        raise ValueError('runner image identity unavailable')
    if mode == 'write':
        packages = (root / 'packages.txt').read_text().splitlines()
        files = [p for folder in ('archives', 'lists', 'etc') for p in (root / folder).rglob('*')
                 if p.is_file() and p.name != 'lock' and 'partial' not in p.parts]
        files.append(root / 'packages.txt')
        data = {'version': 1, 'image': image, 'packages': packages, 'files': {str(p.relative_to(root)): '' for p in files}}
    else:
        if manifest.stat().st_size > 1024 * 1024:
            raise ValueError('oversize manifest')
        data = json.loads(manifest.read_text())
        if data['version'] != 1 or data['image'] != image:
            raise ValueError('runner image differs')
        packages = data['packages']
    if not 2 <= len(packages) <= 258 or not {'fish', 'zsh'} <= set(packages) or not all(
            re.fullmatch(r'[a-z0-9][a-z0-9+.-]*(?<!-)', p) for p in packages):
        raise ValueError('invalid package set')
    files = data['files']
    if not 3 <= len(files) <= 10000 or not any(p.startswith('archives/') and p.endswith('.deb') for p in files) or not any(p.startswith('lists/') for p in files):
        raise ValueError('empty package bundle')
    total = 0
    for name, expected in files.items():
        path = root / name
        if path.is_symlink() or not path.resolve().is_relative_to(root.resolve()) or not path.is_file():
            raise ValueError('missing or invalid bundle file')
        total += path.stat().st_size
        if total > 2 * 1024**3:
            raise ValueError('oversize bundle')
        digest = hashlib.sha256()
        with path.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        if mode == 'write':
            files[name] = digest.hexdigest()
        elif digest.hexdigest() != expected:
            raise ValueError('bundle checksum mismatch')
    if (root / 'packages.txt').read_text().splitlines() != packages:
        raise ValueError('package set differs')
    if mode == 'write':
        manifest.write_text(json.dumps(data) + '\n')
    else:
        print('\n'.join(packages))
except (OSError, ValueError, KeyError, TypeError) as error:
    sys.exit(f'bounded-apt: bundle unavailable: {error}')
PY
}

try_offline() {
  bundle=${AEON_APT_BUNDLE_DIR:-${RUNNER_TEMP:-/nonexistent}/apt-bundle}
  [[ -f $bundle/manifest.json ]] || return 1
  if [[ $mode == playwright && ( ${#targets[@]} -ne 1 || ${targets[0]} != chromium ) ]]; then return 1; fi
  # Finish validation and an archive-only preflight BEFORE dpkg can mutate.
  bundle_manifest check > "$temporary/offline-packages" || return 1
  local package
  packages=()
  while IFS= read -r package; do packages+=("$package"); done < "$temporary/offline-packages"
  if [[ $mode == apt ]]; then
    for package in "$@"; do grep -Fxq -- "$package" "$temporary/offline-packages" || return 1; done
  fi
  write_config "$bundle/etc"
  bundle_config
  if ! run_root_timeout 30 apt-get install --download-only --no-download -y -qq --no-install-recommends "${packages[@]}"; then
    echo 'bounded-apt: incomplete offline resolution; using network fallback'
    write_config "$source_root"
    return 1
  fi
  echo "bounded-apt: installing offline bundle for ${ImageOS} / ${ImageVersion}"
  # A failure after this point is final: never retry after package mutation.
  if run_root_timeout 150 apt-get install --no-download -y -qq --no-install-recommends "${packages[@]}"; then
    echo 'bounded-apt: dependencies installed offline'
    exit 0
  else
    exit "$?"
  fi
}

write_config() {
  # APT_CONFIG is read BEFORE apt.conf.d. Apply our bounds and sources again
  # as the main config, after the image's retry/timeout settings. Preserve its
  # original main config and all apt.conf.d security settings and hooks.
  cat > "$apt_config" <<EOF
Dir::Etc::main "$apt_final_config";
EOF
  : > "$apt_final_config"
  if [[ -f $source_root/apt.conf ]]; then
    printf '#include "%s/apt.conf";\n' "$source_root" >> "$apt_final_config"
  fi
  cat >> "$apt_final_config" <<EOF
Acquire::Retries "0";
Acquire::http::Timeout "15";
Acquire::https::Timeout "15";
APT::Update::Error-Mode "any";
Dir::Etc::sourcelist "$1/sources.list";
Dir::Etc::sourceparts "$1/sources.list.d";
EOF
}

fetch_failure() {
  # Never start another install after dpkg began mutating package state,
  # including when an earlier fetch printed Ign/Err before the timeout.
  if grep -Eq '^(Preparing to unpack|Unpacking |Setting up |Selecting previously unselected package)' "$log"; then
    return 1
  fi
  # Ignore Playwright's final wrapper error, but reject apt/dpkg errors even
  # when an earlier fetch in the same attempt printed Ign or Err lines.
  if grep -E '^E:|^dpkg: error' "$log" | grep -Ev '^E: (Failed to fetch (https?://|mirror\+file:)|Unable to fetch some archives|Some index files failed to download)' >/dev/null; then
    return 1
  fi
  # A slow mirror can keep delivering bytes without a single fetch error.
  # The root timeout reaped it before dpkg started, so fallback is safe.
  [[ $status -eq 124 || $status -eq 137 ]] && return 0
  grep -Eq '^((W|E): Failed to fetch (https?://|mirror\+file:)|E: Unable to fetch some archives|Ign:[0-9]+ (https?://|mirror\+file:)|Err:[0-9]+ (https?://|mirror\+file:))' "$log"
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
            return 'http://' + ('security.ubuntu.com' if security else 'archive.ubuntu.com') + '/ubuntu/'
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
  bundle)
    [[ $# -eq 1 && ! -e $1 && -n ${ImageOS:-} && -n ${ImageVersion:-} ]] || usage
    bundle=$1
    budget=1080
    first_attempt_seconds=240
    attempt=(attempt_bundle)
    ;;
  apt)
    [[ $# -gt 0 ]] || usage
    missing=0
    for tool in "$@"; do
      command -v "$tool" >/dev/null 2>&1 || missing=1
    done
    [[ $missing -eq 0 ]] && exit 0
    budget=210
    first_attempt_seconds=60
    attempt=(attempt_apt "$@")
    ;;
  playwright)
    [[ $# -gt 0 ]] || usage
    parse_playwright "$@"
    budget=720
    first_attempt_seconds=120
    attempt=(attempt_playwright)
    ;;
  *) usage ;;
esac

# Both mirrors, the optional browser download and the final 20-second kill
# grace share this budget, inside the workflow step limit.
deadline=$(($(date +%s) + budget))
attempt_deadline=$deadline
retry=0
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
chmod 755 "$temporary"
apt_config=$temporary/apt.conf
apt_final_config=$temporary/apt-final.conf
log=$temporary/attempt.log
source_root=${AEON_APT_SOURCES_DIR:-/etc/apt}
write_config "$source_root"
packages=()
if [[ $mode == bundle ]]; then
  mkdir -p "$bundle/archives/partial" "$bundle/lists/partial"
  resolve_bundle_packages
  bundle_config
fi

# Same user as the runner: browser downloads land in their cache and happen
# only once, even when the subsequent dependency fetch needs another mirror.
if [[ ${#browser[@]} -gt 0 ]]; then run_timeout user 240 "${browser[@]}"; fi
if [[ $mode != bundle ]]; then try_offline "$@" || true; fi
source_files=()
for file in "$source_root/sources.list" "$source_root/sources.list.d/"*.list "$source_root/sources.list.d/"*.sources; do
  [[ ! -f $file ]] || source_files+=("$file")
done
mirrors='configured apt sources'
if [[ ${#source_files[@]} -gt 0 ]]; then
  mirrors=$(grep -hEo 'https?://(([[:alnum:]-]+\.)?archive|security)\.ubuntu\.com|mirror\+file:[^[:space:]]+' "${source_files[@]}" | sed -E 's#https?://##' | sort -u | paste -sd, -) || true
fi
echo "bounded-apt: using ${mirrors:-configured apt sources}"
if [[ $mode == bundle ]]; then
  # Four fetch-only attempts share 18 minutes including all kill graces.
  # Keep successfully fetched archives between attempts; never run dpkg here.
  alternate=0
  selected_sources=$source_root
  for number in 1 2 3 4; do
    attempt_deadline=$(($(date +%s) + first_attempt_seconds))
    if capture_attempt; then
      mkdir -p "$bundle/etc/sources.list.d"
      [[ ! -f $selected_sources/sources.list ]] || cp "$selected_sources/sources.list" "$bundle/etc/sources.list"
      for file in "$selected_sources/sources.list.d/"*.list "$selected_sources/sources.list.d/"*.sources; do
        [[ ! -f $file ]] || cp "$file" "$bundle/etc/sources.list.d/"
      done
      bundle_manifest write
      echo "bounded-apt: package bundle ready for $ImageOS / $ImageVersion"
      exit 0
    else
      status=$?
    fi
    fetch_failure || exit "$status"
    [[ $number -lt 4 ]] || exit "$status"
    if [[ $number -eq 1 ]] && fallback_sources; then alternate=1; fi
    selected_sources=$source_root
    if [[ $alternate -eq 1 && $((number % 2)) -eq 1 ]]; then selected_sources=$temporary/apt; fi
    write_config "$selected_sources"
    bundle_config
    echo "bounded-apt: retrying bundle fetch (attempt $((number + 1))/4) via $selected_sources"
  done
fi
# Reserve time for a different mirror even if the first fetch never returns.
# This cap covers apt update + install, plus at most 20 seconds to reap it.
attempt_deadline=$(($(date +%s) + first_attempt_seconds))
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
attempt_deadline=$deadline
retry=1
echo 'bounded-apt: retrying fetch once via archive.ubuntu.com (security.ubuntu.com for -security)'
if capture_attempt; then
  echo 'bounded-apt: dependencies installed via archive.ubuntu.com / security.ubuntu.com'
else
  exit "$?"
fi
