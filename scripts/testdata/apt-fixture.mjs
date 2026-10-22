// SPDX-License-Identifier: AGPL-3.0-only
import { mkdtempSync, writeFileSync, mkdirSync, chmodSync } from 'node:fs'
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
  write('timeout', `#!/usr/bin/env bash
state=\${AEON_STUB_STATE:?}
while [[ "\${1:-}" == --* ]]; do
  if [[ "\$1" == "--kill-after" ]]; then shift 2; else shift; fi
done
shift
# A previous attempt's hang marker must not make this attempt look hung
# before it can take the lock or report that the lock is still held.
rm -f "\$state/hung"
"\$@" &
child=\$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
  if ! kill -0 "\$child" 2>/dev/null; then wait "\$child"; exit \$?; fi
  [[ -f "\$state/hung" ]] && break
  sleep 0.05
done
if ! kill -0 "\$child" 2>/dev/null; then wait "\$child"; exit \$?; fi
if [[ "\${AEON_AS_ROOT:-}" == 1 ]]; then
  if [[ -f "\$state/apt.pid" ]]; then kill -KILL "\$(cat "\$state/apt.pid")" 2>/dev/null || true; fi
  rm -f "\$state/hung" "\$state/lock" "\$state/apt.pid"
fi
kill -KILL "\$child" 2>/dev/null || true
for _ in 1 2 3 4 5 6 7 8 9 10; do
  kill -0 "\$child" 2>/dev/null || break
  sleep 0.05
done
exit 124
`)
  const hold = `#!/usr/bin/env bash
state=\${AEON_STUB_STATE:?}
printf '%s\\n' "\$*" >> "\$state/calls"
if [[ -f "\$state/lock" ]]; then
  echo "E: Could not get lock /var/lib/apt/lists/lock. It is held by process apt" >&2
  echo "Error: Installation process exited with code: 100" >&2
  exit 100
fi
mode=\$(cat "\$state/mode")
deps=0
lines=0
if [[ -f "\$state/calls" ]]; then
  deps=\$(grep -c install-deps "\$state/calls" || true)
  lines=\$(grep -c . "\$state/calls" || true)
fi
browser=0
case "\$0 \$*" in
  *install-deps*|*apt-get*) ;;
  *) browser=1 ;;
esac
if [[ "\$browser" -eq 1 ]]; then
  if [[ "\${AEON_AS_ROOT:-}" == 1 ]]; then echo root-browser >> "\$state/errors"; exit 1; fi
  exit 0
fi
if [[ "\$mode" == always-fail ]]; then echo "Error: Installation process exited with code: 100" >&2; exit 100; fi
if [[ "\$mode" == ok ]]; then exit 0; fi
if [[ "\$mode" == hung && "\$deps" -eq 0 && "\$lines" -ge 2 ]]; then exit 0; fi
if [[ "\$mode" == hung && "\$deps" -ge 2 ]]; then exit 0; fi
# The lock file is the hold. Write it here, before this process can be killed,
# so an unprivileged SIGKILL leaves it for the next attempt. Only the root
# timeout removes it. Ignore the removal and stay up until that timeout's
# SIGKILL: exiting 0 would look like a successful install.
echo \$\$ > "\$state/apt.pid"
touch "\$state/hung" "\$state/lock"
# exec retains the pid; parent death and a hard deadline bound this hang too.
exec "\$AEON_FIXTURE_NODE" "\$AEON_FIXTURE_APT_HOLD" "\$PPID"
`
  write('npx', hold)
  write('apt-get', hold)
  return bin
}

export function aptFixture({ mode, shells = false }) {
  const state = mkdtempSync(join(tmpdir(), 'aeon-apt-'))
  writeFileSync(join(state, 'mode'), mode)
  writeFileSync(join(state, 'calls'), '')
  const bin = stubBin(state)
  if (shells) {
    for (const name of ['fish', 'zsh']) writeFileSync(join(bin, name), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 })
  }
  return { state, env: {
    // The stubs stay first; Node's ownership preload also needs ps on Nix.
    PATH: `${bin}:/usr/bin:/bin:${process.env.PATH}`, HOME: process.env.HOME, TMPDIR: process.env.TMPDIR,
    AEON_STUB_STATE: state, AEON_AS_ROOT: '', GITHUB_WORKSPACE: fileURLToPath(new URL('../..', import.meta.url)),
    AEON_FIXTURE_NODE: process.execPath, AEON_FIXTURE_APT_HOLD: fileURLToPath(new URL('./apt-hold.mjs', import.meta.url)),
  } }
}
