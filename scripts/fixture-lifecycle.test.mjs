// SPDX-License-Identifier: AGPL-3.0-only
// Process-control risk: a dead test worker must not leave resistant fixtures
// running, or let teardown signal a reused/unrelated process identity.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { randomInt } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as pause } from 'node:timers/promises'
import { boundFixture, fixtureOwner, maxFixtureMs } from './testdata/fixture-lifecycle.mjs'
import { groupProcesses, trackGroups } from './playwright-processes.mjs'

const fixtures = fixtureOwner()
afterEach(() => fixtures.cleanup())
async function until(check) {
  const deadline = Date.now() + 15000
  while (!check()) { assert.ok(Date.now() < deadline, 'fixture cleanup timed out'); await pause(10) }
}

test('AEON-1064: interrupted fixture runners leave no owned processes in 20 random interrupt rounds', { timeout: 120000 }, async () => {
  // Inject time: the ten-minute cap and parent watchdog are proved without
  // waiting for wall-clock time or weakening the fixtures' signal resistance.
  const timers = [], exits = [], cleared = []
  let parentPID = 42
  const clock = {
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return 1 },
    setInterval: (fn, ms) => { timers.push({ fn, ms }); return 2 },
    clearTimeout: id => cleared.push(id), clearInterval: id => cleared.push(id),
  }
  const stop = boundFixture({ parent: 42, clock, parentPID: () => parentPID, exit: code => exits.push(code) })
  assert.equal(timers[0].ms, maxFixtureMs)
  assert.equal(maxFixtureMs, 600000)
  timers[1].fn(); assert.deepEqual(exits, [])
  parentPID = 1; timers[1].fn(); timers[0].fn()
  assert.deepEqual(exits, [125, 124])
  stop(); assert.deepEqual(cleared, [1, 2])
  assert.throws(() => boundFixture({ timeoutMs: maxFixtureMs + 1 }), /Invalid fixture lifetime/)

  const rounds = Array.from({ length: 20 }, (_, index) => ({ phase: index % 2 ? 'recovery' : 'running', signal: ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGKILL'][Math.floor(index / 2) % 4] }))
  for (let index = rounds.length - 1; index > 0; index--) {
    const other = randomInt(index + 1)
    ;[rounds[index], rounds[other]] = [rounds[other], rounds[index]]
  }
  for (const { phase, signal } of [...rounds, { phase: 'apt', signal: 'parent-death' }, ...['return', 'failure', 'timeout'].map(signal => ({ phase: 'running', signal }))]) {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-fixture-interrupt-'))
    const sentinel = fixtures.spawn(process.execPath, [new URL('./testdata/playwright/hang.mjs', import.meta.url).pathname], { stdio: 'ignore' })
    const child = fixtures.spawn(process.execPath, ['--test', new URL('./testdata/interrupted-fixture-runner.mjs', import.meta.url).pathname], {
      env: { ...process.env, AEON_FIXTURE_SCENARIO_DIR: directory, AEON_FIXTURE_PHASE: phase, AEON_FIXTURE_ENDING: signal, ...(signal === 'SIGKILL' ? { AEON_FIXTURE_TIMEOUT_MS: '5000' } : {}) },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let output = '', exited = false
    child.stdout.on('data', data => { output += data }); child.stderr.on('data', data => { output += data })
    const completion = new Promise(resolve => child.once('close', code => { exited = true; resolve(code) }))
    let tracker
    try {
      await until(() => { assert.equal(exited, false, output); return existsSync(join(directory, 'ready')) })
      const ready = JSON.parse(readFileSync(join(directory, 'ready'), 'utf8'))
      assert.notEqual(ready.worker, process.pid)
      assert.ok(ready.groups.length >= (phase === 'apt' ? 1 : 2), 'a real fixture must be alive at the interruption barrier')
      const log = join(directory, 'groups')
      writeFileSync(log, ready.groups.map(row => JSON.stringify({ pid: row.pid, started: row.started })).join('\n') + '\n')
      tracker = trackGroups(log); tracker.snapshot()
      if (signal === 'parent-death') {
        process.kill(ready.parent, 'SIGKILL')
        await until(() => { try { process.kill(ready.stub, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true } })
        process.kill(ready.worker, 'SIGTERM')
      } else if (['return', 'failure', 'timeout'].includes(signal)) writeFileSync(join(directory, 'release'), 'ready')
      else process.kill(ready.worker, signal)
      const code = await completion
      if (signal === 'return') assert.equal(code, 0, output)
      else assert.notEqual(code, 0, `${phase} ${signal} must report an interrupted test`)
      if (signal === 'failure') assert.match(output, /Intentional fixture failure/)
      if (signal === 'timeout') assert.match(output, /timed out/)
      await until(() => ready.groups.every(row => groupProcesses(row.pid).length === 0))
      process.kill(sentinel.pid, 0)
    } finally {
      tracker?.signal('SIGKILL')
      fixtures.cleanup()
      await completion
    }
  }
})
