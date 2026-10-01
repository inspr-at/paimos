// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { appendFileSync, existsSync, fstatSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { acquireLock, localWorkerArgs, runOwnedCommand } from './playwright-safe.mjs'
import { runUIShards } from './playwright-ui-shards-safe.mjs'
import { processStart, trackGroups } from './playwright-processes.mjs'
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
function fixture(mode, directory, env = process.env) {
  const lock = join(directory, 'suite.lock'), ready = join(directory, `${mode}.ready`)
  const pid = join(directory, `${mode}.pid`), output = join(directory, `${mode}.json`)
  const child = spawn(process.execPath, [new URL('./testdata/playwright/supervisor.mjs', import.meta.url).pathname, mode, lock, ready, pid, output], { stdio: ['ignore', 'pipe', 'pipe'], env })
  let log = '', done = false
  child.stdout.on('data', data => { log += data })
  child.stderr.on('data', data => { log += data })
  const completion = new Promise(resolve => child.on('close', code => { done = true; resolve(code) }))
  return { child, lock, ready, pid, output, completion, exited: () => done, log: () => log }
}

test('host lock keeps its descriptor open, refuses live/unknown owners, and releases only its token', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-lock-test-'))
  const path = join(directory, 'suite.lock')
  const lock = await acquireLock(path)
  assert.ok(fstatSync(lock.fd).isFile())
  await assert.rejects(acquireLock(path), /Another Aeon browser suite/)
  lock.release()
  assert.equal(existsSync(path), false)
  assert.throws(() => fstatSync(lock.fd), { code: 'EBADF' })
  writeFileSync(path, '{}')
  await assert.rejects(acquireLock(path), /Another Aeon browser suite/)
  assert.equal(existsSync(path), true)
  const secondPath = join(directory, 'second.lock')
  const replaced = await acquireLock(secondPath)
  writeFileSync(secondPath, JSON.stringify({ token: 'replacement' }))
  replaced.release()
  assert.equal(existsSync(secondPath), true)
})

test('a live owner with a mismatched lstart is retained without signalling', async () => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-owner-reuse-')), 'suite.lock')
  const owner = { pid: process.pid, token: randomUUID(), started: 'Mon Jan 1 00:00:00 2001' }
  writeFileSync(path, JSON.stringify(owner))
  await assert.rejects(acquireLock(path), /Live, unknown or reused ownership/)
  assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), owner)
})

function expiredOwner() {
  const script = `import { processStart } from ${JSON.stringify(new URL('./playwright-processes.mjs', import.meta.url).href)}; console.log(JSON.stringify({pid:process.pid, token:${JSON.stringify(randomUUID())}, started:processStart(process.pid)}))`
  const child = spawnSync(process.execPath, ['--input-type=module', '-e', script], { encoding: 'utf8' })
  assert.equal(child.status, 0, child.stderr)
  return JSON.parse(child.stdout)
}

test('dead owner with an empty verified journal is recovered and replaced', async () => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-stale-')), 'suite.lock'), owner = expiredOwner()
  writeFileSync(path, JSON.stringify(owner))
  const log = `${path}.${owner.token}.groups`
  writeFileSync(log, '')
  const lock = await acquireLock(path, { graceMs: 50 })
  assert.notEqual(lock.token, owner.token)
  assert.equal(existsSync(log), false)
  lock.release()
})

test('unknown start times, missing journals and partial journals fail closed', async () => {
  for (const mode of ['missing-start', 'missing-log', 'partial-log']) {
    const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-stale-unknown-')), 'suite.lock'), owner = expiredOwner()
    if (mode === 'missing-start') owner.started = ''
    writeFileSync(path, JSON.stringify(owner))
    if (mode !== 'missing-log') writeFileSync(`${path}.${owner.token}.groups`, mode === 'partial-log' ? '{"pid":' : '')
    await assert.rejects(acquireLock(path, { graceMs: 50 }))
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), owner)
    assert.equal(existsSync(`${path}.guard`), false)
  }
})

test('stale recovery refuses a reused detached PID and preserves its process', async () => {
  const sentinel = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore' })
  const sentinelExit = new Promise(resolve => sentinel.once('exit', resolve))
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-stale-reuse-')), 'suite.lock'), owner = expiredOwner()
  const log = `${path}.${owner.token}.groups`
  writeFileSync(path, JSON.stringify(owner))
  writeFileSync(log, JSON.stringify({ pid: sentinel.pid, started: 'Mon Jan 1 00:00:00 2001' }) + '\n')
  try {
    await assert.rejects(acquireLock(path, { graceMs: 50 }), /Stale browser lock retained/)
    process.kill(sentinel.pid, 0)
    assert.equal(existsSync(log), true)
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), owner)
  } finally { sentinel.kill('SIGTERM'); await sentinelExit }
})

test('interrupted recovery claim refuses new suites without removing ownership', async () => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-reaper-')), 'suite.lock'), owner = expiredOwner()
  writeFileSync(path, JSON.stringify(owner))
  writeFileSync(`${path}.guard`, JSON.stringify(owner))
  await assert.rejects(acquireLock(path), /recovery guard retained/)
  assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), owner)
  assert.equal(existsSync(`${path}.guard`), true)
})

test('SIGKILL recovery reaps only journalled groups before allowing one new owner', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-crash-')), probe = fixture('hang', directory)
  let recoveryLock, tracker
  try {
    await waitReady(probe.ready, probe.exited)
    const owner = JSON.parse(readFileSync(probe.lock, 'utf8'))
    const log = `${probe.lock}.${owner.token}.groups`
    const entries = readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line))
    const detachedPid = Number(readFileSync(probe.pid, 'utf8'))
    assert.ok(entries.some(row => row.pid === detachedPid && row.started === processStart(detachedPid)), 'detached child must have a start identity')
    assert.ok(entries.some(row => row.pid !== detachedPid && row.started === processStart(row.pid)), 'root must have a start identity')
    tracker = trackGroups(log)
    tracker.snapshot()
    const killed = new Promise(resolve => probe.child.once('exit', resolve))
    probe.child.kill('SIGKILL')
    await killed
    const attempts = await Promise.allSettled([acquireLock(probe.lock, { graceMs: 100 }), acquireLock(probe.lock, { graceMs: 100 })])
    const winners = attempts.filter(result => result.status === 'fulfilled')
    assert.equal(winners.length, 1)
    recoveryLock = winners[0].value
    assert.equal(existsSync(log), false)
    assert.throws(() => process.kill(Number(readFileSync(probe.pid, 'utf8')), 0), { code: 'ESRCH' })
    assert.ok(attempts.some(result => result.status === 'rejected' && /acquiring or recovering/.test(result.reason.message)))
    await probe.completion
  } finally {
    if (!probe.exited()) {
      if (tracker) { try { tracker.signal('SIGKILL') } catch { /* already reaped */ } }
      probe.child.kill('SIGTERM')
      await probe.completion
    }
    recoveryLock?.release()
  }
})

test('local CLI worker overrides require PW_WORKERS while hosted CI keeps its budget', () => {
  const args = ['tests/one.spec.ts', '--workers=12', '-j', '8', '--workers', '9', '-j=6', '--reporter=json']
  for (const CI of [undefined, '', '0', 'false']) assert.deepEqual(localWorkerArgs(args, { CI }), ['tests/one.spec.ts', '--reporter=json', '--workers=1'])
  assert.deepEqual(localWorkerArgs(args, { CI: '1' }), args)
  assert.deepEqual(localWorkerArgs(args, { PW_WORKERS: '2' }), args)
})

test('shard adapter supervises its existing runner, keeps JSON output valid and fixes the shard budget', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-shard-')), output = join(directory, 'report.json'), lockPath = join(directory, 'suite.lock')
  const args = [output, lockPath, '--shard=1/8', '--plan']
  const result = await runUIShards(args, {
    runner: new URL('./testdata/playwright/shard-probe.mjs', import.meta.url).pathname,
    lockPath, capture: true, graceMs: 100, env: { ...process.env, CI: '1', PW_WORKERS: '4' },
  })
  assert.equal(result.code, 0, result.stderr)
  const report = JSON.parse(readFileSync(output, 'utf8'))
  assert.deepEqual(report.report, { suites: [] })
  assert.deepEqual(report.args, ['--shard=1/8', '--plan'])
  assert.equal(report.policy.workers, 1)
  assert.equal(report.policy.fullyParallel, false)
  assert.equal(existsSync(lockPath), false)
  await assert.rejects(runUIShards([], { runner: join(directory, 'missing-runner.mjs'), lockPath }), /AEON-410 shard runner is not integrated/)
  assert.equal(existsSync(lockPath), false)
})

test('supervised capture keeps JSON reporter output parseable', async () => {
  const lockPath = join(mkdtempSync(join(tmpdir(), 'aeon-pw-json-')), 'suite.lock')
  const result = await runOwnedCommand(process.execPath, ['-e', 'console.log(JSON.stringify({suites: []})); console.error("child diagnostic")'], { lockPath, capture: true, graceMs: 100 })
  assert.equal(result.code, 0)
  assert.deepEqual(JSON.parse(result.stdout), { suites: [] })
  assert.equal(result.stderr.trim(), 'child diagnostic')
  assert.equal(existsSync(lockPath), false)
})

test('PID reuse drops only that group, including root, and preserves valid siblings', () => {
  const log = join(mkdtempSync(join(tmpdir(), 'aeon-pw-identity-')), 'groups')
  const started = 'Mon Jan 1 00:00:00 2001', reused = 'Tue Jan 2 00:00:00 2001'
  writeFileSync(log, [41, 42].map(pid => JSON.stringify({ pid, started })).join('\n') + '\n')
  const signals = [], table = [{ pid: 41, group: 41, started: reused }, { pid: 42, group: 42, started }]
  const tracker = trackGroups(log, { table: () => table, kill: (pid, signal) => signals.push([pid, signal]) })
  assert.deepEqual(tracker.snapshot().unknown, [41])
  assert.deepEqual(tracker.signal('SIGTERM'), [])
  assert.deepEqual(tracker.signal('SIGKILL'), [])
  assert.deepEqual(signals, [[42, 'SIGTERM'], [42, 'SIGKILL']])
})

test('partial signalling keeps trying other groups and retries unretired entries', () => {
  const log = join(mkdtempSync(join(tmpdir(), 'aeon-pw-partial-')), 'groups'), started = 'Mon Jan 1 00:00:00 2001'
  writeFileSync(log, [41, 42].map(pid => JSON.stringify({ pid, started })).join('\n') + '\n')
  const signals = [], table = [41, 42].map(pid => ({ pid, group: pid, started }))
  let failing = true
  const tracker = trackGroups(log, { table: () => table, kill(pid) { if (pid === 41 && failing) throw new Error('EPERM'); signals.push(pid) } })
  assert.equal(tracker.signal('SIGTERM').length, 1)
  assert.deepEqual(signals, [42])
  failing = false
  tracker.signal('SIGKILL')
  assert.deepEqual(signals, [42, 41, 42])
})

test('failed process snapshots and malformed lines do not consume valid records', () => {
  const log = join(mkdtempSync(join(tmpdir(), 'aeon-pw-snapshot-')), 'groups'), started = 'Mon Jan 1 00:00:00 2001'
  writeFileSync(log, JSON.stringify({ pid: 41, started }) + '\n')
  let failing = true
  const signals = [], tracker = trackGroups(log, { table() { if (failing) throw new Error('ps unavailable'); return [{ pid: 41, group: 41, started }] }, kill: pid => signals.push(pid) })
  assert.throws(tracker.snapshot, /ps unavailable/)
  failing = false
  appendFileSync(log, 'invalid\n')
  assert.equal(tracker.signal('SIGTERM').length, 1)
  assert.deepEqual(signals, [41])
})

test('an observed group member proves continuity after its leader exits', () => {
  const log = join(mkdtempSync(join(tmpdir(), 'aeon-pw-leader-')), 'groups'), started = 'Mon Jan 1 00:00:00 2001'
  writeFileSync(log, JSON.stringify({ pid: 41, started }) + '\n')
  let rows = [{ pid: 41, group: 41, started }, { pid: 42, group: 41, started }]
  const signals = [], tracker = trackGroups(log, { table: () => rows, kill: pid => signals.push(pid) })
  tracker.snapshot()
  rows = [rows[1]]
  tracker.signal('SIGTERM')
  assert.deepEqual(signals, [41])
  const coldTracker = trackGroups(log, { table: () => rows, kill: pid => signals.push(pid) })
  assert.deepEqual(coldTracker.snapshot().unverified, [41])
  coldTracker.signal('SIGKILL')
  assert.deepEqual(signals, [41], 'unproven orphan membership must never be signalled')
})

test('release contention retains both lock and journal for later crash recovery', async () => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-release-')), 'suite.lock'), lock = await acquireLock(path)
  const log = `${path}.${lock.token}.groups`
  writeFileSync(log, '')
  writeFileSync(`${path}.guard`, JSON.stringify({ token: 'another-claim' }))
  assert.throws(() => lock.release({ journal: true }), /recovery guard retained/)
  assert.equal(existsSync(path), true)
  assert.equal(existsSync(log), true)
  assert.throws(() => fstatSync(lock.fd), { code: 'EBADF' })
})

test('a reused journal PID cannot block actual sibling cleanup or lock release', async () => {
  const sentinel = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore' })
  const sentinelExit = new Promise(resolve => sentinel.once('exit', resolve))
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-reused-live-'))
  const probe = fixture('normal', directory, { ...process.env, AEON_PW_REUSE_PID: String(sentinel.pid) })
  try {
    assert.equal(await probe.completion, 0, probe.log())
    assert.equal(existsSync(probe.lock), false)
    process.kill(sentinel.pid, 0)
    assert.throws(() => process.kill(Number(readFileSync(probe.pid, 'utf8')), 0), { code: 'ESRCH' })
  } finally {
    if (!probe.exited()) { probe.child.kill('SIGTERM'); await probe.completion }
    sentinel.kill('SIGTERM')
    await sentinelExit
  }
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
