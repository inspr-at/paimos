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

test('AEON-1082: stalled mirror requests are bounded inside Playwright and the outer retry recovers', async () => {
  for (const args of ['install-deps chromium', 'install --with-deps --only-shell chromium']) {
    const result = await run(`bash "${script}" playwright ${args}`, { mode: 'slow-mirror' })
    assert.equal(result.status, 0, `${result.error}\n${result.stdout}\n${result.stderr}`)
    assert.match(result.stderr, /mirror fetch timed out/)
    assert.equal(result.calls.filter(call => call.includes('install-deps')).length, 2)
    assert.equal(existsSync(join(result.state, 'errors')), false, 'browser downloads keep their unprivileged environment')
    const configs = readFileSync(join(result.state, 'apt-configs'), 'utf8').trim().split('\n')
    assert.equal(configs.length, 2)
    for (const config of configs) assert.equal(existsSync(config), false, 'attempt configuration is removed after use')
  }
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
