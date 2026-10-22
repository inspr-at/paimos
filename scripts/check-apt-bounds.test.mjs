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

// AEON-1420: merge queues stalled on Ubuntu downloads, exceeded the four-minute
// shell step and then retried apt against an interrupted dpkg database.
test('AEON-1420 all dependency installs inherit apt acquisition limits through sudo', async () => {
  for (const args of ['apt aeon-fixture-shell', 'playwright install-deps chromium', 'playwright install --with-deps --only-shell chromium']) {
    const result = await run(`bash "${script}" ${args}`, { mode: 'bounded-config' })
    assert.equal(result.status, 0, `${args}: ${result.error}\n${result.stderr}`)
    const config = readFileSync(join(result.state, 'configs'), 'utf8')
    for (const limit of ['Acquire::Retries "1";', 'Acquire::http::Timeout "10";', 'Acquire::https::Timeout "10";', 'DPkg::Lock::Timeout "10";', 'APT::Update::Error-Mode "any";']) {
      assert.ok(config.includes(limit), `${args}: missing ${limit}`)
    }
    for (const path of readFileSync(join(result.state, 'config-paths'), 'utf8').trim().split('\n')) {
      assert.ok(config.includes(`Dir::Etc::main "${path}";`), 'runner config fragments must not override our limits')
      assert.equal(existsSync(path), false, 'the invocation must clean up its private apt configuration')
    }
  }
})

test('AEON-1420 retries repair interrupted dpkg and preserve a failed repair', async () => {
  for (const args of ['apt aeon-fixture-shell', 'playwright install-deps chromium']) {
    const recovered = await run(`bash "${script}" ${args}`, { mode: 'interrupted' })
    assert.match(recovered.stderr, /dpkg was interrupted/, 'the first attempt must reach the interrupted database')
    assert.equal(recovered.status, 0, `${args}: ${recovered.error}\n${recovered.stderr}`)
    assert.deepEqual(readFileSync(join(recovered.state, 'repairs'), 'utf8').trim().split('\n'), ['--configure -a'])
    assert.ok(recovered.calls.length >= 2, 'a repaired install must actually retry apt')
    const refused = await run(`bash "${script}" ${args}`, { mode: 'repair-fails' })
    assert.notEqual(refused.status, 0, 'a failed dpkg repair cannot report success')
    assert.equal(refused.calls.length, 1, 'do not continue with a broken package database')
  }
})

test('AEON-1420 retry and kill budgets finish before the enclosing workflow deadline', async () => {
  for (const [args, budget, workflowSeconds] of [
    ['apt aeon-fixture-shell', 210, 240],
    ['playwright install-deps chromium', 720, 780],
    ['playwright install --with-deps --only-shell chromium', 720, 780],
  ]) {
    const result = await run(`bash "${script}" ${args}`, { mode: 'budget' })
    assert.notEqual(result.status, 0, 'exhausting the budget must fail the install')
    assert.equal(result.error, '', 'the fixture deadline is only a hang guard')
    const elapsed = Number(readFileSync(join(result.state, 'clock'), 'utf8'))
    assert.ok(elapsed > 0 && elapsed <= budget, `${args}: ${elapsed}s exceeds the ${budget}s total budget`)
    assert.ok(elapsed < workflowSeconds, `${args}: workflow cancellation would interrupt cleanup`)
    const timers = readFileSync(join(result.state, 'timers'), 'utf8').trim().split('\n')
    assert.ok(timers.some(line => line.startsWith('1|')), 'apt and its kill deadline must still run as root')
    assert.ok(timers.every(line => Number(line.split('|')[1]) > 0), 'never start a command after its budget is exhausted')
  }
})
