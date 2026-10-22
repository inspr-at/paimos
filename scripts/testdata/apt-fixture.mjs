// SPDX-License-Identifier: AGPL-3.0-only
import { mkdtempSync, writeFileSync, mkdirSync, chmodSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

function stubBin(state) {
  const bin = join(state, 'bin')
  mkdirSync(bin)
  const write = (name, body) => {
    const path = join(bin, name)
    writeFileSync(path, body)
    chmodSync(path, 0o755)
  }
  write('sudo', `#!/usr/bin/env bash
[[ "\${1:-}" == "--" ]] && shift
export AEON_AS_ROOT=1
exec "$@"
`)
  // sudo is simulated throughout this fixture. Track the ownership handoff
  // explicitly; restrictive modes below are real and need a real chmod.
  write('chown', `#!/usr/bin/env bash
state=\${AEON_STUB_STATE:?}
[[ \${AEON_AS_ROOT:-} == 1 && \$# -eq 4 && \$1 == -hR && \$2 == -- && \$3 =~ ^[0-9]+:[0-9]+$ && -d \$4 ]] || exit 1
printf '%s\\n' "\$3" "\$4" > "\$state/ownership-handoff"
if [[ -f \$state/root-owned-bundle ]]; then
  [[ \$(cat "\$state/root-owned-bundle") == "\$4" ]] || exit 1
  mv "\$state/root-owned-bundle" "\$state/runner-owned-bundle"
fi
`)
  if (existsSync(join(state, 'clock'))) write('date', `#!/usr/bin/env bash
cat "\${AEON_STUB_STATE:?}/clock"
`)
  write('timeout', `#!/usr/bin/env bash
state=\${AEON_STUB_STATE:?}
grace=0
while [[ "\${1:-}" == --* ]]; do
  case "\$1" in
    --kill-after) grace=\${2%s}; shift 2 ;;
    --kill-after=*) grace=\${1#*=}; grace=\${grace%s}; shift ;;
    *) shift ;;
  esac
done
limit=\$1
shift
printf '%s|%s|%s\\n' "\${AEON_AS_ROOT:-0}" "\$limit" "\$*" >> "\$state/timeouts"
printf '%s\\n' "\$grace" >> "\$state/kill-graces"
# Advance the budget regression by each requested timeout plus root cleanup,
# without sleeps or elapsed-time assertions. Keep main's common journal format.
if [[ "\$(cat "\$state/mode")" == apt-budget ]]; then
  printf '%s\\n' "\$*" >> "\$state/calls"
  echo \$((\$(cat "\$state/clock") + limit + grace)) > "\$state/clock"
  exit 124
fi
if [[ \$1 == python3 && \${2:-} == - && \${3:-} == write && -f \$state/root-owned-bundle ]]; then
  echo 'fixture: bundle still owned by root at manifest write' >&2
  exit 1
fi
advance_clock() {
  [[ -f "\$state/clock" ]] || return 0
  local index elapsed
  index=\$(wc -l < "\$state/timeouts" | tr -d ' ')
  elapsed=\$(sed -n "\${index}p" "\$state/elapsed")
  echo \$((\$(cat "\$state/clock") + \${elapsed:-0})) > "\$state/clock"
  if [[ -f "\$state/enforce-timeouts" && \${elapsed:-0} -gt \$limit ]]; then return 124; fi
  return 0
}
# Finite adapters return their status directly. Only the hang/reaping cases
# need a background process and polling; fake deadlines advance identically.
case "\$(cat "\$state/mode")" in
  ok|bundle-*|always-fail|fetch-fail|fetch-always-fail|dependency-fail|install-fetch-always-fail|mirror-stall|offline-missing|offline-simulation-fail|offline-install-fail|browser-fail)
    "\$@"; status=\$?; advance_clock; clock_status=\$?
    [[ \$status -ne 0 || \$clock_status -eq 0 ]] || status=\$clock_status
    exit \$status ;;
esac
# A previous attempt's hang marker must not make this attempt look hung
# before it can take the lock or report that the lock is still held.
rm -f "\$state/hung"
# Keep explicit stdin for bounded Python here-docs, just as GNU timeout does.
"\$@" <&0 &
child=\$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
  if ! kill -0 "\$child" 2>/dev/null; then wait "\$child"; status=\$?; advance_clock; exit \$status; fi
  [[ -f "\$state/hung" ]] && break
  sleep 0.05
done
if ! kill -0 "\$child" 2>/dev/null; then wait "\$child"; status=\$?; advance_clock; exit \$status; fi
if [[ "\${AEON_AS_ROOT:-}" == 1 ]]; then
  if [[ -f "\$state/apt.pid" ]]; then kill -KILL "\$(cat "\$state/apt.pid")" 2>/dev/null || true; fi
  rm -f "\$state/hung" "\$state/lock" "\$state/apt.pid"
fi
kill -KILL "\$child" 2>/dev/null || true
for _ in 1 2 3 4 5 6 7 8 9 10; do
  kill -0 "\$child" 2>/dev/null || break
  sleep 0.05
done
advance_clock
exit "\$(cat "\$state/timeout-status")"
`)
  const hold = `#!/usr/bin/env bash
state=\${AEON_STUB_STATE:?}
printf '%s\\n' "\$*" >> "\$state/calls"
if [[ -f "\$state/lock" ]]; then
  echo "E: Could not get lock /var/lib/apt/lists/lock. It is held by process apt" >&2
  echo "Error: Installation process exited with code: 100" >&2
  exit 100
fi
mode=\$(< "\$state/mode")
lines=0
# Keep snapshots identical without spawning cp/sed/basename for every source
# file on every apt call. The full matrix also runs on shared macOS hosts.
while IFS= read -r call; do [[ -z "\$call" ]] || lines=\$((lines + 1)); done < "\$state/calls"
snapshot="\$state/attempt-\$lines"
config=\$(python3 - "\$state" "\$snapshot" "\${AEON_AS_ROOT:-0}" "\${APT_CONFIG:-}" "\$AEON_APT_SOURCES_DIR" <<'PY'
from pathlib import Path
import re
import sys

state, snapshot, root, bootstrap, sources = sys.argv[1:]
snapshot = Path(snapshot)
snapshot.mkdir()
(snapshot / 'root').write_text(root)
main, parts = Path(sources) / 'sources.list', Path(sources) / 'sources.list.d'
config = ''
if bootstrap:
    text = Path(bootstrap).read_text()
    (snapshot / 'bootstrap').write_text(text)
    def field(text, name):
        match = re.search(r'^' + re.escape(name) + r' "([^"]*)";$', text, re.M)
        return match[1] if match else ''
    config = field(text, 'Dir::Etc::main')
    with (Path(state) / 'config-paths').open('a') as paths:
        paths.write(bootstrap + '\\n')
        if config:
            paths.write(config + '\\n')
    config = config or bootstrap
    text = Path(config).read_text()
    (snapshot / 'config').write_text(text)
    main, parts = Path(field(text, 'Dir::Etc::sourcelist')), Path(field(text, 'Dir::Etc::sourceparts'))
if main.is_file():
    (snapshot / 'sources.list').write_bytes(main.read_bytes())
for pattern in ('*.list', '*.sources'):
    for file in parts.glob(pattern):
        if file.is_file():
            (snapshot / file.name).write_bytes(file.read_bytes())
print(config)
PY
) || exit 1
if [[ "\$*" == *'install-deps --dry-run chromium' ]]; then
  # Playwright 1.63 passes ALL packages to apt's simulation but prints only
  # missing ones. Both fixture libraries appear installed to the dry run.
  apt-get install -s --no-install-recommends libtest fonts-test || exit \$?
  echo 'All system dependencies are installed.'
  exit 0
fi
if [[ "\$*" == *--no-download* ]]; then
  if [[ "\${AEON_AS_ROOT:-}" != 1 ]]; then echo offline-not-root >&2; exit 1; fi
  if [[ "\$mode" == offline-simulation-fail && "\$*" == *--simulate* ]]; then
    echo 'E: Unable to correct problems, you have held broken packages.' >&2
    exit 100
  fi
  if [[ "\$mode" == offline-missing && "\$*" == *--download-only* ]]; then
    echo 'E: Unable to fetch some archives, --no-download is set' >&2
    exit 100
  fi
  if [[ "\$*" != *--download-only* && "\$*" != *--simulate* ]]; then touch "\$state/offline-dpkg-started"; fi
  if [[ "\$mode" == offline-install-fail && "\$*" != *--download-only* && "\$*" != *--simulate* ]]; then
    echo 'Unpacking libtest (1.0) ...' >&2
    echo 'dpkg: error processing archive libtest.deb' >&2
    exit 100
  fi
  exit 0
fi
if [[ "\$mode" == bundle-* ]]; then
  lists=\$(sed -n '/^Dir::State::lists "/{s/^Dir::State::lists "//;s/";$//;p;}' "\$config")
  archives=\$(sed -n '/^Dir::Cache::archives "/{s/^Dir::Cache::archives "//;s/";$//;p;}' "\$config")
  [[ -n "\$lists" && -n "\$archives" ]] || { echo shared-apt-cache >&2; exit 1; }
  if [[ "\$*" == 'update -qq' ]]; then
    echo 'fixture package indexes' > "\$lists/noble_Packages"
    touch "\$lists/lock" "\$archives/lock"
    echo 'partial index' > "\$lists/partial/unneeded"
    if [[ "\$mode" == bundle-interrupted ]]; then
      python3 - "\$state" "\$lists" "\$AEON_APT_SOURCES_DIR" <<'PY'
from pathlib import Path
import sys

state, lists, sources = map(Path, sys.argv[1:])
if not (state / 'interrupted-tree').exists():
    # pkgAcqAuxFile uses lists/auxfiles; pkgAcqFile::Done links file: URIs.
    # A killed fetch leaves that link and auxiliary metadata for the retry.
    (lists / 'auxfiles').mkdir()
    (lists / 'auxfiles/_etc_apt_apt-mirrors.txt').symlink_to(sources / 'apt-mirrors.txt')
    (lists / 'auxfiles/stale').write_text('interrupted metadata')
    (lists / 'auxfiles/stale').chmod(0o000)
    (lists / 'mirror+file_old_binary-amd64_Packages').write_text('first attempt index')
else:
    (lists / 'archive.ubuntu.com_ubuntu_dists_noble_main_binary-amd64_Packages.lz4').write_text('fallback index')
    (lists / 'archive.ubuntu.com_ubuntu_dists_noble_InRelease').write_text('fallback release')
PY
    fi
    exit 0
  fi
  [[ "\$*" == *--download-only* ]] || { echo unexpected-package-mutation >&2; exit 1; }
  printf '%s\\n' "\$*" >> "\$state/downloads"
  if [[ "\$mode" == bundle-interrupted && ! -f "\$state/interrupted-tree" ]]; then
    echo fish > "\$archives/fish_1_amd64.deb"
    echo 'unfinished archive' > "\$archives/partial/libtest.deb"
    echo 'killed first fetch with cached archive, indexes, mirror links and partial files' > "\$state/interrupted-tree"
    echo 'Get:2 mirror+file:apt-mirrors.txt noble/main libtest [123 kB]' >&2
    exit "\$(cat "\$state/timeout-status")"
  fi
  if [[ "\$mode" == bundle-fetch-fail || "\$mode" == bundle-fetch-retry && \$(wc -l < "\$state/downloads") -le 2 ]]; then
    echo 'E: Failed to fetch http://azure.archive.ubuntu.com/ubuntu/pool/libtest.deb Connection timed out' >&2
    echo 'E: Some files failed to download' >&2
    exit 100
  fi
  for name in fish zsh libtest fonts-test; do echo "\$name" > "\$archives/\${name}_1_amd64.deb"; done
  echo 'partial download' > "\$archives/partial/unneeded"
  if [[ -f \$state/root-only ]]; then
    python3 - "\$state" "\$lists" "\$archives" <<'PY'
import json
from pathlib import Path
import sys

state, lists, archives = map(Path, sys.argv[1:])
(lists / 'auxfiles').mkdir(exist_ok=True)
(lists / 'auxfiles' / 'metadata').write_text('root-only index metadata')
restricted = [lists / 'partial', archives / 'partial', lists / 'auxfiles', lists / 'noble_Packages', archives / 'fish_1_amd64.deb']
for path in restricted:
    path.chmod(0o700 if path.is_dir() else 0o600)
(state / 'root-only-modes').write_text(json.dumps({str(p.relative_to(lists.parent)): p.stat().st_mode & 0o777 for p in restricted}))
(state / 'root-owned-bundle').write_text(str(lists.parent))
PY
  fi
  exit 0
fi
browser=0
case "\$0 \$*" in
  *install-deps*|*apt-get*) ;;
  *) browser=1 ;;
esac
if [[ "\$browser" -eq 1 ]]; then
  if [[ "\${AEON_AS_ROOT:-}" == 1 ]]; then echo root-browser >> "\$state/errors"; exit 1; fi
  if [[ "\$mode" == browser-fail ]]; then echo 'browser download failed' >&2; exit 1; fi
  exit 0
fi
if [[ "\$mode" == dependency-fail ]]; then
  echo 'Ign:1 http://azure.archive.ubuntu.com/ubuntu noble InRelease' >&2
  echo 'E: Unable to correct problems, you have held broken packages.' >&2
  exit 100
fi
if [[ "\$mode" == install-fetch-always-fail && "\$*" == 'update -qq' ]]; then exit 0; fi
if [[ "\$mode" == fetch-fail || "\$mode" == fetch-always-fail || "\$mode" == install-fetch-always-fail || "\$mode" == mirror-stall ]]; then
  if [[ "\$mode" == fetch-always-fail || "\$mode" == install-fetch-always-fail ]] || grep -Eq '://azure.archive.ubuntu.com/|mirror[+]file:[^[:space:]]*/apt-mirrors(-security)?[.]txt' "\$snapshot/"*.sources "\$snapshot/"*.list 2>/dev/null; then
    if [[ "\$mode" == mirror-stall ]]; then
      echo "Get:1 file:\$AEON_APT_SOURCES_DIR/apt-mirrors.txt Mirrorlist" >&2
      echo "Ign:2 mirror+file:\$AEON_APT_SOURCES_DIR/apt-mirrors.txt noble/main amd64 libtest" >&2
      exit 124
    fi
    echo 'E: Failed to fetch http://azure.archive.ubuntu.com/ubuntu/pool/main/libtest.deb Connection timed out' >&2
    echo 'E: Unable to fetch some archives, maybe run apt-get update or try with --fix-missing?' >&2
    exit 100
  fi
  exit 0
fi
if [[ "\$mode" == always-fail ]]; then echo "Error: Installation process exited with code: 100" >&2; exit 100; fi
if [[ "\$mode" == ok || "\$mode" == offline-missing || "\$mode" == offline-simulation-fail ]]; then exit 0; fi
if [[ "\$mode" == hung && "\$lines" -ge 2 ]]; then exit 0; fi
# The lock file is the hold. Write it here, before this process can be killed,
# so an unprivileged SIGKILL leaves it for the next attempt. Only the root
# timeout removes it. Ignore the removal and stay up until that timeout's
# SIGKILL: exiting 0 would look like a successful install.
if [[ "\$mode" == slow-fetch || "\$mode" == dpkg-timeout ]]; then
  # Index refreshes succeed; stall while downloading packages for install.
  if [[ "\$*" == 'update -qq' ]]; then exit 0; fi
  # Vendor mirror lists survive fallback, so check Ubuntu's selected source.
  if ! grep -Eq '://azure.archive.ubuntu.com/|mirror[+]file:' "\$snapshot/sources.list" "\$snapshot/ubuntu.sources" 2>/dev/null; then exit 0; fi
  echo 'Get:2 http://azure.archive.ubuntu.com/ubuntu noble/main amd64 fonts-ipafont-gothic [3513 kB]' >&2
  if [[ "\$mode" == dpkg-timeout ]]; then cat "\$state/dpkg-activity" >&2; fi
else
  echo 'Ign:1 http://azure.archive.ubuntu.com/ubuntu noble InRelease' >&2
fi
echo \$\$ > "\$state/apt.pid"
touch "\$state/hung" "\$state/lock"
# exec retains the pid; parent death and a hard deadline bound this hang too.
exec "\$AEON_FIXTURE_NODE" "\$AEON_FIXTURE_APT_HOLD" "\$PPID"
`
  write('npx', hold)
  write('apt-get', hold)
  return bin
}

export function aptFixture({ mode, shells = false, format = 'sources', elapsed, enforceTimeouts = false, mirrorList = false, timeoutStatus = 124, dpkgActivity = 'Unpacking libtest (1.0) ...', bundleDir, rootOnly = false, imageOS = 'ubuntu24', imageVersion = '20261021.1', runnerArch = 'X64' }) {
  const state = mkdtempSync(join(tmpdir(), 'aeon-apt-'))
  writeFileSync(join(state, 'mode'), mode)
  writeFileSync(join(state, 'calls'), '')
  writeFileSync(join(state, 'timeout-status'), String(timeoutStatus))
  writeFileSync(join(state, 'dpkg-activity'), dpkgActivity + '\n')
  if (rootOnly) writeFileSync(join(state, 'root-only'), '')
  if (enforceTimeouts) writeFileSync(join(state, 'enforce-timeouts'), '')
  const sourceRoot = join(state, 'apt')
  mkdirSync(join(sourceRoot, 'sources.list.d'), { recursive: true })
  const sources = format === 'list' ? {
    'sources.list': `# Keep comments and source options.
deb [arch=amd64 signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg] http://azure.archive.ubuntu.com/ubuntu/ noble main restricted # noble-security is separate
deb-src http://azure.archive.ubuntu.com/ubuntu/ noble-security main restricted
`,
  } : {
    'sources.list.d/ubuntu.sources': `Types: deb deb-src
URIs: http://azure.archive.ubuntu.com/ubuntu/
Suites: noble noble-updates
 noble-backports noble-security
Components: main restricted universe multiverse
Architectures: amd64
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
`,
  }
  sources['sources.list.d/vendor.list'] = 'deb [signed-by=/usr/share/keyrings/vendor.gpg] https://packages.example.test/ubuntu stable main\n'
  sources['sources.list.d/vendor.sources'] = 'Types: deb\nURIs: https://repo.example.test/linux\nSuites: stable\nComponents: main\nSigned-By: /usr/share/keyrings/other-vendor.gpg\n'
  if (mirrorList) {
    const mirror = `mirror+file:${join(sourceRoot, 'apt-mirrors.txt')}`
    const security = `mirror+file:${join(sourceRoot, 'apt-mirrors-security.txt')}`
    const ubuntu = format === 'list' ? 'sources.list' : 'sources.list.d/ubuntu.sources'
    sources[ubuntu] = sources[ubuntu].replaceAll('http://azure.archive.ubuntu.com/ubuntu/', mirror)
    if (format === 'list') sources[ubuntu] = sources[ubuntu].replace(`deb-src ${mirror}`, `deb-src ${security}`)
    sources['apt-mirrors.txt'] = 'http://azure.archive.ubuntu.com/ubuntu/\nhttps://archive.ubuntu.com/ubuntu/\n'
    sources['apt-mirrors-security.txt'] = 'http://azure.archive.ubuntu.com/ubuntu/\nhttps://security.ubuntu.com/ubuntu/\n'
    if (mirrorList === 'hosted') {
      // ubuntu-24.04 hosted layout: separate deb822 stanzas sharing a list
      // whose first entry is Azure. Keep all four Noble suites/components.
      sources[ubuntu] = `Types: deb
URIs: ${mirror}
Suites: noble noble-updates noble-backports
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

Types: deb
URIs: ${mirror}
Suites: noble-security
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
`
      mkdirSync(join(sourceRoot, 'apt.conf.d'))
      sources['apt.conf.d/99runner-retries'] = 'Acquire::Retries "10";\nAcquire::http::Timeout "120";\nAcquire::https::Timeout "120";\n'
      sources['apt.conf'] = 'APT::Get::Assume-Yes "true";\n'
    }
    const vendor = `mirror+file:${join(sourceRoot, 'vendor-mirrors.txt')}`
    sources['vendor-mirrors.txt'] = 'https://packages.example.test/ubuntu/\n'
    sources['sources.list.d/vendor.list'] += `deb ${vendor} stable main\n`
    sources['sources.list.d/vendor.sources'] += `\nTypes: deb\nURIs: ${vendor}\nSuites: stable\nComponents: main\nSigned-By: /usr/share/keyrings/other-vendor.gpg\n`
  }
  for (const [file, text] of Object.entries(sources)) writeFileSync(join(sourceRoot, file), text)
  if (mode === 'apt-budget') writeFileSync(join(state, 'clock'), '1000\n')
  if (elapsed) {
    writeFileSync(join(state, 'clock'), '1000\n')
    writeFileSync(join(state, 'elapsed'), elapsed.join('\n') + '\n')
  }
  const bin = stubBin(state)
  if (shells) {
    for (const name of ['fish', 'zsh']) writeFileSync(join(bin, name), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 })
  }
  return { state, sourceRoot, sources, env: {
    // The stubs stay first; Node's ownership preload also needs ps on Nix.
    PATH: `${bin}:/usr/bin:/bin:${process.env.PATH}`, HOME: process.env.HOME, TMPDIR: process.env.TMPDIR,
    AEON_STUB_STATE: state, AEON_AS_ROOT: '', GITHUB_WORKSPACE: fileURLToPath(new URL('../..', import.meta.url)),
    AEON_APT_SOURCES_DIR: sourceRoot,
    AEON_APT_BUNDLE_DIR: bundleDir ?? join(state, 'absent-bundle'), ImageOS: imageOS, ImageVersion: imageVersion, RUNNER_ARCH: runnerArch,
    GITHUB_STEP_SUMMARY: join(state, 'summary'),
    AEON_FIXTURE_NODE: process.execPath, AEON_FIXTURE_APT_HOLD: fileURLToPath(new URL('./apt-hold.mjs', import.meta.url)),
  } }
}
