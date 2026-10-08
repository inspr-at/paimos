// SPDX-License-Identifier: AGPL-3.0-only
// AEON-933: an unprivileged timeout cannot reap root apt-get. The 5d155476
// retry then collides on the lock and the step fails. This fails on that
// commit because its workflow commands are the historical ones below.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, chmodSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { auditWorkflows, auditWorkflowText } from './check-apt-bounds.mjs'

const root = fileURLToPath(new URL('..', import.meta.url))
const script = join(root, 'scripts/ci/bounded-apt.sh')
const historical = `for i in 1 2 3; do
  timeout 240 npx playwright install-deps chromium && break
done`

function stubBin(state) {
  const bin = join(state, 'bin')
  mkdirSync(bin)
  const write = (name, body) => {
    const path = join(bin, name)
    writeFileSync(path, body)
    chmodSync(path, 0o755)
  }
  write('sudo', `#!/bin/bash
[[ "\${1:-}" == "--" ]] && shift
export AEON_AS_ROOT=1
exec "$@"
`)
  write('timeout', `#!/bin/bash
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
  const hold = `#!/bin/bash
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
# exec keeps this pid, so the timeout's SIGKILL reaps the sleeper with the stub.
exec sleep 30
`
  write('npx', hold)
  write('apt-get', hold)
  return bin
}

function run(command, { mode, shells = false }) {
  const state = mkdtempSync(join(tmpdir(), 'aeon-apt-'))
  writeFileSync(join(state, 'mode'), mode)
  writeFileSync(join(state, 'calls'), '')
  const bin = stubBin(state)
  if (shells) {
    for (const name of ['fish', 'zsh']) writeFileSync(join(bin, name), '#!/bin/bash\nexit 0\n', { mode: 0o755 })
  }
  const result = spawnSync('bash', ['-eo', 'pipefail', '-c', command], {
    cwd: root,
    env: {
      PATH: `${bin}:/usr/bin:/bin`,
      HOME: process.env.HOME,
      TMPDIR: process.env.TMPDIR,
      AEON_STUB_STATE: state,
      AEON_AS_ROOT: '',
      GITHUB_WORKSPACE: root,
    },
    encoding: 'utf8',
    timeout: 8000,
  })
  const pidFile = join(state, 'apt.pid')
  if (existsSync(pidFile)) {
    const pid = Number(readFileSync(pidFile, 'utf8'))
    if (Number.isInteger(pid)) { try { process.kill(pid, 'SIGKILL') } catch { /* already reaped */ } }
  }
  const calls = existsSync(join(state, 'calls')) ? readFileSync(join(state, 'calls'), 'utf8').split('\n').filter(Boolean) : []
  return { status: result.status, stderr: result.stderr ?? '', stdout: result.stdout ?? '', error: result.error?.message ?? '', calls, state }
}

test('workflow installs delegate to the root-timeout helper', () => {
  assert.deepEqual(auditWorkflows(), [])
  const old = `jobs:\n  demo:\n    steps:\n      - name: Install Chromium system dependencies\n        timeout-minutes: 13\n        run: |\n          for i in 1 2 3; do\n            timeout 240 npx playwright install-deps chromium && break\n          done\n`
  const failures = auditWorkflowText('old.yml', old)
  assert.ok(failures.some(line => line.includes('must go through')), failures.join('\n'))
})

test('a hung root apt is reaped before the retry, which the 5d155476 command cannot do', () => {
  const current = run(`bash "${script}" playwright install-deps chromium`, { mode: 'hung' })
  assert.equal(current.status, 0, `${current.error}\n${current.stdout}\n${current.stderr}`)
  assert.equal(current.calls.filter(call => call.includes('install-deps')).length, 2)
  const legacy = run(historical, { mode: 'hung' })
  assert.notEqual(legacy.status, 0, 'unprivileged timeout must leave the apt lock held')
  assert.match(legacy.stderr, /Could not get lock/, `${legacy.error}\n${legacy.calls.join('\n')}\n${legacy.stderr}`)
})

test('browser downloads stay unprivileged while dependency installs run as root', () => {
  const result = run(`bash "${script}" playwright install --with-deps --only-shell chromium`, { mode: 'hung' })
  assert.equal(result.status, 0, result.stderr)
  assert.ok(result.calls.some(call => call.includes('--only-shell')))
  assert.ok(result.calls.some(call => call.includes('install-deps')))
  assert.equal(existsSync(join(result.state, 'errors')), false)
})

test('three failed attempts fail the step and installed shells skip apt', () => {
  const exhausted = run(`bash "${script}" playwright install-deps chromium`, { mode: 'always-fail' })
  assert.notEqual(exhausted.status, 0)
  assert.equal(exhausted.calls.length, 3)
  const skipped = run(`bash "${script}" apt fish zsh`, { mode: 'always-fail', shells: true })
  assert.equal(skipped.status, 0, skipped.stderr)
  assert.deepEqual(skipped.calls, [])
  const recovered = run(`bash "${script}" apt zsh fish`, { mode: 'hung' })
  assert.equal(recovered.status, 0, recovered.stderr)
})
