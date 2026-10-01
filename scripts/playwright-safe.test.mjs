// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { acquireLock, runOwnedCommand } from './playwright-safe.mjs'
import requireSupervisor from './playwright-global-setup.mjs'

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
async function waitReady(path, exited) {
  const deadline = Date.now() + 15000
  while (!existsSync(path)) {
    assert.ok(!exited(), 'child exited before ready')
    assert.ok(Date.now() < deadline, 'child startup timed out')
    await sleep(25)
  }
}
function fixture(mode, directory) {
  const lock = join(directory, 'suite.lock'), ready = join(directory, `${mode}.ready`)
  const pid = join(directory, `${mode}.pid`), output = join(directory, `${mode}.json`)
  const child = spawn(process.execPath, [new URL('./testdata/playwright/supervisor.mjs', import.meta.url).pathname, mode, lock, ready, pid, output], { stdio: ['ignore', 'pipe', 'pipe'] })
  let log = '', done = false
  child.stdout.on('data', data => { log += data })
  child.stderr.on('data', data => { log += data })
  const completion = new Promise(resolve => child.on('close', code => { done = true; resolve(code) }))
  return { child, lock, ready, pid, output, completion, exited: () => done, log: () => log }
}

test('host lock rejects simultaneous owners, retains unknown/stale ownership, and releases only its token', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-lock-test-'))
  const path = join(directory, 'suite.lock')
  const lock = acquireLock(path)
  assert.throws(() => acquireLock(path), /Another Aeon browser suite/)
  lock.release()
  assert.equal(existsSync(path), false)
  writeFileSync(path, '{}')
  assert.throws(() => acquireLock(path), /Another Aeon browser suite/)
  assert.equal(existsSync(path), true)
  const secondPath = join(directory, 'second.lock')
  const replaced = acquireLock(secondPath)
  writeFileSync(secondPath, JSON.stringify({ token: 'replacement' }))
  replaced.release()
  assert.equal(existsSync(secondPath), true)
})

test('direct local Playwright invocation is refused before workers start', () => {
  const prior = process.env.CI
  process.env.CI = ''
  try { assert.throws(requireSupervisor, /Run browsers through npm test/) }
  finally { if (prior === undefined) delete process.env.CI; else process.env.CI = prior }
})

test('worktree TMPDIR overrides cannot split the host browser lock', () => {
  const script = `import { suiteLockPath } from ${JSON.stringify(new URL('./playwright-global-setup.mjs', import.meta.url).href)}; console.log(suiteLockPath)`
  const paths = ['first', 'second'].map(label => {
    const directory = mkdtempSync(join(tmpdir(), `aeon-pw-${label}-`))
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', script], {
      env: { ...process.env, TMPDIR: directory, TMP: directory, TEMP: directory }, encoding: 'utf8',
    })
    assert.equal(result.status, 0, result.stderr)
    return result.stdout.trim()
  })
  assert.equal(paths[0], paths[1])
})

test('failed executable startup releases its lock', async () => {
  const lockPath = join(mkdtempSync(join(tmpdir(), 'aeon-pw-start-test-')), 'suite.lock')
  await assert.rejects(runOwnedCommand('/aeon-nonexistent-executable', [], { lockPath }), /ENOENT/)
  assert.equal(existsSync(lockPath), false)
})

for (const mode of ['normal', 'fail', 'hang']) {
  test(`owned descendants are gone after ${mode === 'hang' ? 'SIGINT and SIGTERM' : mode + ' exit'}`, async () => {
    for (const signal of mode === 'hang' ? ['SIGINT', 'SIGTERM'] : [undefined]) {
      const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-group-test-'))
      const probe = fixture(mode, directory)
      try {
        await waitReady(probe.ready, probe.exited)
        if (mode === 'hang') {
          const collision = fixture('normal', directory)
          assert.equal(await collision.completion, 1)
          assert.match(collision.log(), /Another Aeon browser suite/)
          probe.child.kill(signal)
        }
        assert.equal(await probe.completion, signal === 'SIGINT' ? 130 : signal === 'SIGTERM' ? 143 : mode === 'fail' ? 7 : 0, probe.log())
        const result = JSON.parse(readFileSync(probe.output, 'utf8'))
        assert.equal(result.metrics.remaining_processes, 0)
        assert.equal(result.metrics.after, 0)
        assert.equal(existsSync(probe.lock), false)
        const pid = Number(readFileSync(probe.pid, 'utf8'))
        assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' })
      } finally { if (!probe.exited()) { probe.child.kill('SIGTERM'); await probe.completion } }
    }
  })
}

// Real Chromium is tested only in hosted CI after its pinned install. Local
// safety checks use tiny Node descendants and never open a browser.
for (const [mode, signal] of [['browser'], ['browser-fail'], ['browser-hang', 'SIGINT'], ['browser-hang', 'SIGTERM']]) {
  test(`Chromium shell lifecycle: ${mode}${signal ? ` ${signal}` : ''}`, { skip: process.env.AEON_BROWSER_LIFECYCLE !== '1' }, async () => {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-browser-test-'))
    const probe = fixture(mode, directory)
    try {
      await waitReady(probe.ready, probe.exited)
      if (signal) probe.child.kill(signal)
      assert.equal(await probe.completion, signal === 'SIGINT' ? 130 : signal === 'SIGTERM' ? 143 : mode === 'browser-fail' ? 7 : 0, probe.log())
      const result = JSON.parse(readFileSync(probe.output, 'utf8'))
      assert.ok(result.metrics.peak > 0, 'must measure actual browser processes')
      assert.equal(result.metrics.after, 0)
      assert.equal(result.metrics.remaining_processes, 0)
      assert.equal(existsSync(probe.lock), false)
      console.log(`Chromium lifecycle ${mode}: ${JSON.stringify(result.metrics)}`)
    } finally { if (!probe.exited()) { probe.child.kill('SIGTERM'); await probe.completion } }
  })
}
