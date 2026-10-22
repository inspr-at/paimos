// SPDX-License-Identifier: AGPL-3.0-only
// AEON-933: an unprivileged timeout cannot reap root apt-get. The 5d155476
// retry then collides on the lock and the step fails. This fails on that
// commit because its workflow commands are the historical ones below.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { aptFixture } from './testdata/apt-fixture.mjs'
import { fixtureOwner } from './testdata/fixture-lifecycle.mjs'
import { auditWorkflows, auditWorkflowText } from './check-apt-bounds.mjs'

const root = fileURLToPath(new URL('..', import.meta.url))
const script = join(root, 'scripts/ci/bounded-apt.sh')
const historical = `for i in 1 2 3; do
  timeout 240 npx playwright install-deps chromium && break
done`

const fixtures = fixtureOwner()
afterEach(() => fixtures.cleanup())

async function run(command, options) {
  const { state, env } = aptFixture(options)
  let stdout = '', stderr = '', error = ''
  const child = fixtures.spawn('bash', ['-eo', 'pipefail', '-c', command], { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.on('data', data => { stdout += data })
  child.stderr.on('data', data => { stderr += data })
  const deadline = setTimeout(() => { error = 'fixture timed out'; fixtures.cleanup() }, 8000)
  try {
    const status = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve) })
    const calls = existsSync(join(state, 'calls')) ? readFileSync(join(state, 'calls'), 'utf8').split('\n').filter(Boolean) : []
    return { status, stderr, stdout, error, calls, state }
  } finally { clearTimeout(deadline); fixtures.cleanup() }
}

test('workflow installs delegate to the root-timeout helper', () => {
  assert.deepEqual(auditWorkflows(), [])
  const old = `jobs:\n  demo:\n    steps:\n      - name: Install Chromium system dependencies\n        timeout-minutes: 13\n        run: |\n          for i in 1 2 3; do\n            timeout 240 npx playwright install-deps chromium && break\n          done\n`
  const failures = auditWorkflowText('old.yml', old)
  assert.ok(failures.some(line => line.includes('must go through')), failures.join('\n'))
})

test('a hung root apt is reaped before the retry, which the 5d155476 command cannot do', async () => {
  const current = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'hung' })
  assert.equal(current.status, 0, `${current.error}\n${current.stdout}\n${current.stderr}`)
  assert.equal(current.calls.filter(call => call.includes('install-deps')).length, 2)
  const legacy = await run(historical, { mode: 'hung' })
  assert.notEqual(legacy.status, 0, 'unprivileged timeout must leave the apt lock held')
  assert.match(legacy.stderr, /Could not get lock/, `${legacy.error}\n${legacy.calls.join('\n')}\n${legacy.stderr}`)
})

test('browser downloads stay unprivileged while dependency installs run as root', async () => {
  const result = await run(`bash "${script}" playwright install --with-deps --only-shell chromium`, { mode: 'hung' })
  assert.equal(result.status, 0, result.stderr)
  assert.ok(result.calls.some(call => call.includes('--only-shell')))
  assert.ok(result.calls.some(call => call.includes('install-deps')))
  assert.equal(existsSync(join(result.state, 'errors')), false)
})

test('three failed attempts fail the step and installed shells skip apt', async () => {
  const exhausted = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'always-fail' })
  assert.notEqual(exhausted.status, 0)
  assert.equal(exhausted.calls.length, 3)
  const skipped = await run(`bash "${script}" apt fish zsh`, { mode: 'always-fail', shells: true })
  assert.equal(skipped.status, 0, skipped.stderr)
  assert.deepEqual(skipped.calls, [])
  const recovered = await run(`bash "${script}" apt zsh fish`, { mode: 'hung' })
  assert.equal(recovered.status, 0, recovered.stderr)
})

test('shell installation uses cached indexes before refreshing apt', async () => {
  // Risk: an unnecessary update consumes the shell-install budget. Stub package
  // names also keep this independent of the real runner's installed shells.
  const cached = await run(`bash "${script}" apt aeon-fixture-fish aeon-fixture-zsh`, { mode: 'ok' })
  assert.equal(cached.status, 0, cached.stderr)
  assert.equal(cached.calls.length, 1, cached.calls.join('\n'))
  assert.match(cached.calls[0], /install .*aeon-fixture-fish aeon-fixture-zsh/)
})

test('root shell installation retries fit the four-minute workflow step', async () => {
  // Risk: independent update/install deadlines and retries outlive the workflow
  // deadline, which interrupts apt before the helper can reap its root group.
  const exhausted = await run(`bash "${script}" apt aeon-fixture-fish aeon-fixture-zsh`, { mode: 'apt-budget' })
  assert.notEqual(exhausted.status, 0, exhausted.stdout)
  const deadlines = readFileSync(join(exhausted.state, 'timeouts'), 'utf8').trim().split('\n').map(line => line.split(' ').map(Number))
  assert.deepEqual(deadlines, [[1, 150, 20], [1, 30, 20]], 'root cleanup must finish before the shared 220-second budget')
  assert.equal(Number(readFileSync(join(exhausted.state, 'clock'), 'utf8')), 1220)
  assert.match(exhausted.stderr, /apt retry budget exhausted/)
  assert.match(exhausted.calls[0], /install /)
  assert.match(exhausted.calls[1], /update /)
})
