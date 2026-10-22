#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# AEON-933: bound apt and Playwright system-dependency installs.
# timeout must be root. An unprivileged timeout cannot signal the root apt-get
# that sudo or Playwright starts, so the lock survives and the next attempt
# fails with exit 100 (PR 377, web-shard 3 and 11). Browser downloads stay
# unprivileged so they land in the runner's cache, not root's.
# AEON-1652: share this run's package bundle across image rollouts safely.
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
  if [[ ${decision_deadline:-$effective_deadline} -lt $effective_deadline ]]; then
    effective_deadline=$decision_deadline
  fi
  remaining=$((effective_deadline - $(date +%s)))
  [[ $remaining -gt 0 ]] || return 124
  [[ $seconds -le $remaining ]] || seconds=$remaining
  start_timeout "$user" "$seconds" "$@"
}

start_timeout() {
  local user=$1 seconds=$2
  shift 2
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
import shutil
import stat
import sys

mode, directory = sys.argv[1:]
root = Path(directory)
manifest = root / 'manifest.json'
image = [os.environ.get('ImageOS'), os.environ.get('ImageVersion'), os.environ.get('RUNNER_ARCH', platform.machine())]

def unavailable(path, reason):
    try:
        info = path.lstat()
        details = f'st_mode={oct(info.st_mode)}, owner={info.st_uid}:{info.st_gid}'
    except OSError as error:
        details = f'st_mode=unavailable, owner=unavailable, stat={error}'
    raise ValueError(f'{path}: {reason} ({details})')

def regular(path):
    try:
        relative = path.relative_to(root)
    except ValueError:
        unavailable(path, 'outside root')
    if '..' in relative.parts:
        unavailable(path, 'outside root (parent traversal)')
    for part in [root, *list(path.parents)[:len(relative.parts) - 1], path]:
        if part.is_symlink():
            unavailable(part, 'symlink')
    if not path.resolve().is_relative_to(root.resolve()):
        unavailable(path, 'outside root')
    try:
        info = path.lstat()
    except FileNotFoundError:
        unavailable(path, 'not a regular file (missing)')
    if not stat.S_ISREG(info.st_mode):
        unavailable(path, 'not a regular file')
    if not info.st_mode & 0o444:
        unavailable(path, 'unreadable (no read permission bits)')
    return info

def read_text(path, limit):
    if regular(path).st_size > limit:
        unavailable(path, f'oversize file (limit {limit} bytes)')
    return path.read_text()

def index_name(name):
    # Apt's local mirror lists, translations, auxfiles and interrupted
    # downloads are not package indexes used by --no-download.
    return re.fullmatch(r'[A-Za-z0-9_%:+.~-]+_(?:InRelease|Release(?:\.gpg)?|Packages(?:\.(?:lz4|xz|gz|bz2|zst))?)', name)

def source_name(name):
    return re.fullmatch(r'etc/(?:sources\.list|sources\.list\.d/[A-Za-z0-9_.-]+\.(?:list|sources))', name)

def entries(folder):
    for count, entry in enumerate(folder.iterdir()):
        if count >= 10000:
            unavailable(folder, 'too many directory entries (limit 10000)')
        yield entry

path = manifest
try:
    if not all(image):
        raise ValueError('runner image identity unavailable')
    if mode == 'write':
        for name in ('archives', 'lists', 'etc', 'etc/sources.list.d'):
            path = root / name
            if path.is_symlink():
                unavailable(path, 'symlink')
            if not path.is_dir():
                unavailable(path, 'not a directory')
        path = root / 'packages.txt'
        packages = read_text(path, 65536).splitlines()
        debs = {str(p.relative_to(root)): '' for p in entries(root / 'archives') if p.name.endswith('.deb')}
        lists = [str(p.relative_to(root)) for p in entries(root / 'lists') if index_name(p.name)]
        sources = [str(p.relative_to(root)) for p in (root / 'etc').glob('sources.list')]
        sources += [str(p.relative_to(root)) for p in entries(root / 'etc/sources.list.d') if source_name(str(p.relative_to(root)))]
        data = {'version': 2, 'image': image, 'packages': packages, 'debs': debs, 'lists': lists, 'sources': sources}
    else:
        data = json.loads(read_text(manifest, 1024 * 1024))
        producer = data['image']
        if data['version'] != 2 or not isinstance(producer, list) or len(producer) != 3 or not all(isinstance(p, str) and p for p in producer):
            raise ValueError('invalid runner image')
        if producer[0] != image[0] or producer[2] != image[2]:
            raise ValueError('runner OS or architecture differs')
        packages = data['packages']
    path = manifest
    if not isinstance(packages, list) or not 2 <= len(packages) <= 258 or not all(
            isinstance(p, str) and re.fullmatch(r'[a-z0-9][a-z0-9+.-]*(?<!-)', p) for p in packages) or not {'fish', 'zsh'} <= set(packages):
        raise ValueError('invalid package set')
    debs, lists, sources = data['debs'], data['lists'], data['sources']
    if not isinstance(debs, dict) or not isinstance(lists, list) or not isinstance(sources, list) or not debs or not lists or not sources or not 3 <= len(debs) + len(lists) + len(sources) <= 10000:
        raise ValueError('empty or oversize package bundle')
    files = [*debs, *lists, *sources, 'packages.txt']
    if not all(isinstance(name, str) for name in files):
        raise ValueError('invalid bundle path')
    total = 0
    for name in files:
        path = root / name
        total += regular(path).st_size
        if name in debs and not re.fullmatch(r'archives/[A-Za-z0-9_%:+.~-]+\.deb', name):
            unavailable(path, 'invalid archive name')
        if name in lists and (not name.startswith('lists/') or not index_name(name.removeprefix('lists/'))):
            unavailable(path, 'invalid package index name')
        if name in sources and not source_name(name):
            unavailable(path, 'invalid source name')
        if total > 2 * 1024**3:
            raise ValueError('oversize bundle')
        with path.open('rb') as stream:
            # GitHub verifies artifact integrity. Hash only the archives apt
            # will install; the other inputs need existence/readability checks.
            if name in debs:
                digest = hashlib.sha256()
                for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                    digest.update(chunk)
                if mode == 'write':
                    debs[name] = digest.hexdigest()
                elif digest.hexdigest() != debs[name]:
                    raise ValueError('bundle checksum mismatch')
            else:
                stream.read(1)
    path = root / 'packages.txt'
    if read_text(path, 65536).splitlines() != packages:
        raise ValueError('package set differs')
    if mode == 'write':
        # Exclude auxiliary files from the artifact itself as well. In
        # particular, never let the uploader follow mirror-list symlinks.
        for folder in ('archives', 'lists'):
            for path in entries(root / folder):
                if str(path.relative_to(root)) in files:
                    continue
                if path.is_symlink() or not path.is_dir():
                    path.unlink()
                else:
                    shutil.rmtree(path)
        path = manifest
        manifest.write_text(json.dumps(data) + '\n')
    else:
        print('exact' if producer == image else 'cross-image')
        print('\n'.join(packages))
except (OSError, ValueError, KeyError, TypeError) as error:
    if isinstance(error, OSError):
        try:
            unavailable(Path(error.filename) if error.filename else path, f'unreadable: {error}')
        except ValueError as diagnostic:
            error = diagnostic
    elif 'st_mode=' not in str(error):
        try:
            unavailable(path, str(error))
        except ValueError as diagnostic:
            error = diagnostic
    sys.exit(f'bounded-apt: bundle unavailable: {error}')
PY
}

finish() {
  local status=$?
  if [[ $mode == bundle && $status -ne 0 && -d $bundle ]]; then
    echo "bounded-apt: failed bundle tree (at most 200 entries, depth 4): $bundle" >&2
    # Ten seconds were reserved outside the fetch deadline for diagnostics,
    # including this short kill grace, even when apt exhausted its budget.
    sudo -- env "PATH=$PATH" LC_ALL=C timeout --kill-after=2s 5 python3 - "$bundle" >&2 <<'PY' || true
import os
from pathlib import Path
import stat
import sys

remaining = 200
def listing(path, depth):
    global remaining
    if remaining <= 0:
        return
    remaining -= 1
    try:
        info = path.lstat()
        print(f'{stat.filemode(info.st_mode)} {info.st_uid}:{info.st_gid} {info.st_size} {path}', flush=True)
        if depth < 4 and stat.S_ISDIR(info.st_mode):
            with os.scandir(path) as entries:
                for entry in entries:
                    listing(Path(entry.path), depth + 1)
                    if remaining <= 0:
                        break
    except OSError as error:
        print(f'{path}: {error}', flush=True)
listing(Path(sys.argv[1]), 0)
PY
  fi
  rm -rf "$temporary"
  return "$status"
}

try_offline() {
  local bundle_root=${AEON_APT_BUNDLE_DIR:-${RUNNER_TEMP:-/nonexistent}/apt-bundle}
  local candidate kind='' package install_seconds=150 remaining
  local decision_deadline=$(($(date +%s) + 60))
  # Selection, validation and archive-only preflights share a short decision
  # window on both image paths. Reaping still fits in the overall step budget.
  if [[ $mode == playwright && ( ${#targets[@]} -ne 1 || ${targets[0]} != chromium ) ]]; then return 1; fi
  # Pattern downloads preserve artifact directories. Prefer the exact image,
  # then accept this run's other image only within the same OS/architecture.
  for candidate in "$bundle_root/apt-bundle-${ImageOS:-}-${ImageVersion:-}-${RUNNER_ARCH:-}" "$bundle_root" "$bundle_root"/apt-bundle-*; do
    [[ -f $candidate/manifest.json ]] || continue
    bundle=$candidate
    if bundle_manifest check > "$temporary/offline-packages"; then
      {
        IFS= read -r kind
        packages=()
        while IFS= read -r package; do packages+=("$package"); done
      } < "$temporary/offline-packages"
      break
    fi
  done
  [[ -n $kind ]] || return 1
  # Finish validation and an archive-only preflight BEFORE dpkg can mutate.
  if [[ $mode == apt ]]; then
    for package in "$@"; do printf '%s\n' "${packages[@]}" | grep -Fxq -- "$package" || return 1; done
  fi
  write_config "$bundle/etc"
  bundle_config
  if [[ $kind == cross-image ]] && ! run_root_timeout 15 apt-get install --simulate --no-download -y -qq --no-install-recommends "${packages[@]}"; then
    echo 'bounded-apt: cross-image simulation failed before dpkg; using network fallback'
    write_config "$source_root"
    return 1
  fi
  if ! run_root_timeout 30 apt-get install --download-only --no-download -y -qq --no-install-recommends "${packages[@]}"; then
    echo 'bounded-apt: incomplete offline resolution; using network fallback'
    write_config "$source_root"
    return 1
  fi
  # Admit dpkg only with its FULL install allowance plus SIGKILL grace left.
  # Once admitted, start a fresh timeout without the decision deadline or a
  # second clock check that could silently shorten the promised allowance.
  remaining=$((deadline - 20 - $(date +%s)))
  if [[ $remaining -lt $install_seconds ]]; then
    echo 'bounded-apt: insufficient offline install budget before dpkg; using network fallback'
    write_config "$source_root"
    return 1
  fi
  report_path "$kind bundle"
  echo "bounded-apt: installing offline bundle from $bundle for ${ImageOS} / ${ImageVersion}"
  # A failure after this point is final: never retry after package mutation.
  if start_timeout root "$install_seconds" apt-get install --no-download -y -qq --no-install-recommends "${packages[@]}"; then
    echo 'bounded-apt: dependencies installed offline'
    exit 0
  else
    exit "$?"
  fi
}

report_path() {
  echo "bounded-apt: path=$1"
  if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
    printf 'bounded-apt: path=%s\n' "$1" >> "$GITHUB_STEP_SUMMARY"
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
  if grep -E '^E:|^dpkg: error' "$log" | grep -Ev '^E: (Failed to fetch (https?://|mirror\+file:)|Unable to fetch some archives|Some index files failed to download|Some files failed to download$)' >/dev/null; then
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
    budget=1070 # 18 minutes, with 10 seconds reserved for failure diagnostics.
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
trap finish EXIT
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
if [[ $mode != bundle ]]; then report_path network; fi
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
      # Apt can leave root-only files (0600) and _apt-owned directories
      # (0700). Hand this private tree back to the runner before either the
      # unprivileged manifest or upload traverses it. Apt has exited here.
      attempt_deadline=$deadline
      run_root_timeout 30 chown -hR -- "$(id -u):$(id -g)" "$bundle"
      run_root_timeout 30 chmod -R -- u=rwX,go=rX "$bundle"
      # Working files are not bundle inputs, even after the ownership handoff.
      run_root_timeout 30 rm -rf -- "$bundle/archives/partial" "$bundle/archives/lock" "$bundle/lists/partial" "$bundle/lists/lock"
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
