// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { setTimeout as pause } from 'node:timers/promises'
import { aptFixture } from './apt-fixture.mjs'
import { fixtureOwner } from './fixture-lifecycle.mjs'
import { processStart } from '../playwright-processes.mjs'

const fixtures = fixtureOwner()
afterEach(() => fixtures.cleanup())
const directory = process.env.AEON_FIXTURE_SCENARIO_DIR
const phase = process.env.AEON_FIXTURE_PHASE
const ending = process.env.AEON_FIXTURE_ENDING
async function until(check) {
  const deadline = Date.now() + 15000
  while (!check()) { assert.ok(Date.now() < deadline, 'fixture readiness timed out'); await pause(10) }
}

test('fixture worker interrupted by its owning test', { timeout: 30000 }, async t => {
  const worker = { worker: process.pid, workerStarted: processStart(process.pid) }
  try {
    if (phase === 'apt') {
      const { state, env } = aptFixture({ mode: 'hung' })
      const child = fixtures.spawn('bash', ['-c', 'npx playwright install-deps chromium & wait'], { env, stdio: 'ignore' })
      await until(() => { assert.equal(child.exitCode, null, 'apt fixture exited before hanging'); return existsSync(join(state, 'hung')) })
      // The stub must finish exec and install its parent watchdog first.
      await until(() => { assert.equal(child.exitCode, null, 'apt fixture exited before its watchdog'); return existsSync(join(state, 'watching')) })
      writeFileSync(join(directory, 'ready'), JSON.stringify({ ...worker, parent: child.pid, stub: Number(readFileSync(join(state, 'apt.pid'), 'utf8')), groups: fixtures.snapshot() }))
    } else {
      const lock = join(directory, 'suite.lock'), ready = join(directory, 'hang.ready')
      const args = mode => [new URL('./playwright/supervisor.mjs', import.meta.url).pathname, mode, lock, mode === 'hang' ? ready : join(directory, 'normal.ready'), join(directory, 'hang.pid'), join(directory, 'result.json')]
      const child = fixtures.spawn(process.execPath, args('hang'), { stdio: 'ignore' })
      await until(() => existsSync(ready))
      if (phase === 'recovery') {
        const exited = new Promise(resolve => child.once('exit', resolve))
        child.kill('SIGKILL'); await exited
        const reaper = fixtures.spawn(process.execPath, args('normal'), { stdio: 'ignore' })
        await until(() => {
          if (!existsSync(`${lock}.guard`)) return false
          const entries = readdirSync(`${lock}.guard`)
          return entries.length === 1 && JSON.parse(readFileSync(join(`${lock}.guard`, entries[0]), 'utf8')).pid === reaper.pid
        })
      }
      writeFileSync(join(directory, 'ready'), JSON.stringify({ ...worker, groups: fixtures.snapshot() }))
    }
    // An isolated negative control proves the outer PID/group assertion sees
    // survivors when this worker's signal cleanup is absent. The outer test
    // retains its own verified tracker and kills these fixtures afterwards.
    if (ending === 'negative-control') process.removeAllListeners('SIGTERM')
    if (['return', 'failure', 'timeout'].includes(ending)) {
      await until(() => existsSync(join(directory, 'release')))
      if (ending === 'failure') throw new Error('Intentional fixture failure')
      if (ending === 'timeout') await t.test('intentional fixture timeout', { timeout: 1 }, async subtest => {
        await new Promise(resolve => subtest.signal.addEventListener('abort', resolve, { once: true }))
      })
    } else await new Promise(resolve => t.signal.addEventListener('abort', resolve, { once: true }))
  } finally { fixtures.cleanup() }
})
