// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import childProcess, { spawn, spawnSync } from 'node:child_process'
import { syncBuiltinESMExports } from 'node:module'
import fs, { appendFileSync, existsSync, fstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { acquireLock, localWorkerArgs, runOwnedCommand } from './playwright-safe.mjs'
import { runUIShards } from './playwright-ui-shards-safe.mjs'
import { processStart, recordRootGroup, trackGroups, validStart } from './playwright-processes.mjs'
import { recordDetachedChild } from './playwright-owned-groups.mjs'
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
async function waitFor(check) {
  const deadline = Date.now() + 15000
  while (!check()) {
    assert.ok(Date.now() < deadline, 'condition timed out')
    await sleep(25)
  }
}
// A root slower than the supervisor's whole attempt budget. The budget is
// counted in attempts, so a load-dependent start must never decide these tests.
const SLOW_ROOT_MS = 1000
const slowRoot = `Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ${SLOW_ROOT_MS})`
// Tests that keep the supervisor blind to its root (every ps answer a miss) hold
// each blind answer until the root has really finished (zombie or reaped). The
// retry loop then observes the exit itself instead of racing the root's
// start-up under load. The deadline only guards against a hung root.
function holdUntilRootExit(spawnSyncReal, pid) {
  const guard = Date.now() + 60_000
  for (;;) {
    const state = spawnSyncReal('ps', ['-p', String(pid), '-o', 'stat='], { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' } })
    if (state.status !== 0 || /^Z/.test(state.stdout.trim())) return
    assert.ok(Date.now() < guard, 'root did not exit (hang guard)')
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10)
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

test('stale recovery skips a reused detached PID and releases the stale lock', async () => {
  const sentinel = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore' })
  const sentinelExit = new Promise(resolve => sentinel.once('exit', resolve))
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-stale-reuse-')), 'suite.lock'), owner = expiredOwner()
  const log = `${path}.${owner.token}.groups`
  writeFileSync(path, JSON.stringify(owner))
  writeFileSync(log, JSON.stringify({ pid: sentinel.pid, started: 'Mon Jan 1 00:00:00 2001' }) + '\n')
  let lock
  try {
    lock = await acquireLock(path, { graceMs: 50 })
    process.kill(sentinel.pid, 0)
    assert.equal(existsSync(log), false)
    assert.notEqual(lock.token, owner.token)
    lock.release(); lock = undefined
    assert.equal(existsSync(path), false)
    process.kill(sentinel.pid, 0)
  } finally { lock?.release(); sentinel.kill('SIGTERM'); await sentinelExit }
})

function detachedProbe() {
  const child = spawn(process.execPath, ['-e', 'process.on("SIGTERM", () => {}); setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore' })
  const completion = new Promise(resolve => child.once('exit', resolve))
  return { child, completion }
}

test('stale recovery reaps verified siblings and ignores reused group PIDs', async () => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-mixed-reused-')), 'suite.lock'), owner = expiredOwner()
  const sibling = detachedProbe(), sentinel = detachedProbe(), log = `${path}.${owner.token}.groups`
  let lock
  try {
    writeFileSync(path, JSON.stringify(owner))
    writeFileSync(log, [
      { pid: sibling.child.pid, started: processStart(sibling.child.pid) },
      { pid: sentinel.child.pid, started: 'Mon Jan 1 00:00:00 2001' },
    ].map(entry => JSON.stringify(entry)).join('\n') + '\n')
    lock = await acquireLock(path, { graceMs: 50 })
    await sibling.completion
    assert.throws(() => process.kill(sibling.child.pid, 0), { code: 'ESRCH' })
    process.kill(sentinel.child.pid, 0)
    assert.equal(existsSync(log), false)
    lock.release(); lock = undefined
    assert.equal(existsSync(path), false)
    assert.equal(existsSync(`${path}.guard`), false)
    process.kill(sentinel.child.pid, 0)
  } finally {
    lock?.release()
    sibling.child.kill('SIGKILL'); sentinel.child.kill('SIGKILL')
    await Promise.all([sibling.completion, sentinel.completion])
  }
})

test('stale recovery reaps verified siblings before retaining unverified or malformed rows', async () => {
  for (const mode of ['unverified', 'invalid', 'torn']) {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-mixed-recovery-'))
    const path = join(directory, 'suite.lock'), owner = expiredOwner(), sibling = detachedProbe()
    let orphan
    const log = `${path}.${owner.token}.groups`
    try {
      writeFileSync(path, JSON.stringify(owner))
      let bad
      if (mode === 'unverified') {
        const helper = spawnSync(process.execPath, [new URL('./testdata/playwright/orphan-group.mjs', import.meta.url).pathname], { encoding: 'utf8' })
        assert.equal(helper.status, 0, helper.stderr)
        orphan = JSON.parse(helper.stdout)
        bad = JSON.stringify({ pid: orphan.pid, started: orphan.started }) + '\n'
      } else bad = mode === 'invalid' ? '{"pid":42,"started":""}\n' : '{"pid":'
      writeFileSync(log, JSON.stringify({ pid: sibling.child.pid, started: processStart(sibling.child.pid) }) + '\n' + bad)
      await assert.rejects(acquireLock(path, { graceMs: 50 }), /Stale browser lock retained/)
      await sibling.completion
      assert.throws(() => process.kill(sibling.child.pid, 0), { code: 'ESRCH' }, `${mode} must not block sibling reaping`)
      if (orphan) process.kill(orphan.helper, 0)
      assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), owner)
      assert.equal(existsSync(log), true)
      assert.equal(existsSync(`${path}.guard`), false)
    } finally {
      sibling.child.kill('SIGKILL'); await sibling.completion
      if (orphan) { try { process.kill(-orphan.pid, 'SIGKILL') } catch (error) { if (error.code !== 'ESRCH') throw error } }
    }
  }
})

test('partial stale-recovery signalling reaps siblings and leaves failed identities retryable', async t => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-recovery-signal-')), 'suite.lock'), owner = expiredOwner()
  const first = detachedProbe(), second = detachedProbe(), kill = process.kill.bind(process)
  const log = `${path}.${owner.token}.groups`
  writeFileSync(path, JSON.stringify(owner))
  writeFileSync(log, [first, second].map(probe => JSON.stringify({ pid: probe.child.pid, started: processStart(probe.child.pid) })).join('\n') + '\n')
  const injected = t.mock.method(process, 'kill', (pid, signal) => {
    if (pid === -first.child.pid) throw Object.assign(new Error('injected signal denial'), { code: 'EPERM' })
    return kill(pid, signal)
  })
  let lock
  try {
    await assert.rejects(acquireLock(path, { graceMs: 10 }), /Stale browser lock retained/)
    await second.completion
    kill(first.child.pid, 0)
    assert.throws(() => kill(second.child.pid, 0), { code: 'ESRCH' })
    assert.equal(existsSync(log), true)
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')), owner)
    injected.mock.restore()
    lock = await acquireLock(path, { graceMs: 10 })
    await first.completion
    assert.equal(existsSync(log), false)
  } finally {
    injected.mock.restore()
    first.child.kill('SIGKILL'); second.child.kill('SIGKILL')
    await Promise.all([first.completion, second.completion])
    lock?.release()
  }
})

test('a transient ps miss retries and records the verified detached child', async () => {
  const probe = detachedProbe(), entries = []
  let calls = 0
  try {
    assert.equal(recordDetachedChild(probe.child, { detached: true }, entry => entries.push(entry), {
      start: pid => ++calls === 1 ? '' : processStart(pid),
    }), probe.child)
    assert.ok(calls >= 2)
    assert.deepEqual(entries, [{ pid: probe.child.pid, started: processStart(probe.child.pid) }])
  } finally { probe.child.kill('SIGKILL'); await probe.completion }
})

test('persistent ps misses kill the spawned detached child without journalling an invalid start', async () => {
  const probe = detachedProbe(), entries = []
  let calls = 0
  try {
    assert.throws(() => recordDetachedChild(probe.child, { detached: true }, entry => entries.push(entry), {
      start: () => { calls++; return '' }, attempts: 3,
    }), /spawned group terminated/)
    await probe.completion
    assert.equal(calls, 3)
    assert.deepEqual(entries, [])
    assert.throws(() => process.kill(probe.child.pid, 0), { code: 'ESRCH' })
  } finally { probe.child.kill('SIGKILL'); await probe.completion }
})

test('an already-exited detached child creates no journal entry after a ps miss', async () => {
  const probe = detachedProbe(), entries = []
  probe.child.kill('SIGKILL'); await probe.completion
  assert.equal(recordDetachedChild(probe.child, { detached: true }, entry => entries.push(entry), { start: () => '' }), probe.child)
  assert.deepEqual(entries, [])
})

test('journal write failure kills the newly spawned detached child', async () => {
  const probe = detachedProbe()
  try {
    assert.throws(() => recordDetachedChild(probe.child, { detached: true }, () => { throw new Error('journal unavailable') }), /journal unavailable/)
    await probe.completion
    assert.throws(() => process.kill(probe.child.pid, 0), { code: 'ESRCH' })
  } finally { probe.child.kill('SIGKILL'); await probe.completion }
})

test('root identity retries a first-ps miss and records only its verified start', async () => {
  const probe = detachedProbe(), entries = []
  let calls = 0
  try {
    assert.equal(await recordRootGroup(probe.child, entry => entries.push(entry), {
      start: pid => ++calls === 1 ? '' : processStart(pid),
    }), probe.child)
    assert.ok(calls >= 2)
    assert.deepEqual(entries, [{ pid: probe.child.pid, started: processStart(probe.child.pid) }])
  } finally { probe.child.kill('SIGKILL'); await probe.completion }
})

test('unverifiable live roots terminate their group without an invalid journal entry', async () => {
  const probe = detachedProbe(), entries = []
  let calls = 0
  try {
    await assert.rejects(recordRootGroup(probe.child, entry => entries.push(entry), {
      start: () => { calls++; return '' }, attempts: 3,
    }), /spawned group terminated/)
    await probe.completion
    assert.equal(calls, 3)
    assert.deepEqual(entries, [])
    assert.throws(() => process.kill(-probe.child.pid, 0), { code: 'ESRCH' })
  } finally { probe.child.kill('SIGKILL'); await probe.completion }
})

test('root retries allow a fast child to exit normally instead of failing verification', async () => {
  const child = spawn(process.execPath, ['-e', 'process.exit(7)'], { detached: true, stdio: 'ignore' })
  const exited = new Promise(resolve => child.once('exit', resolve)), entries = []
  try {
    assert.equal(await recordRootGroup(child, entry => entries.push(entry), { start: () => '', attempts: 200 }), child)
    assert.equal(await exited, 7)
    assert.deepEqual(entries, [])
    assert.throws(() => process.kill(-child.pid, 0), { code: 'ESRCH' })
  } finally { child.kill('SIGKILL'); await exited }
})

test('supervisor retries a root ps miss without overriding a successful suite exit', async t => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-root-retry-')), 'suite.lock')
  const original = childProcess.spawnSync
  let calls = 0
  const injected = t.mock.method(childProcess, 'spawnSync', (command, args, options) => {
    if (command === 'ps' && args[0] === '-p' && args[1] !== String(process.pid) && ++calls === 1) return { status: 1, stdout: '' }
    return original(command, args, options)
  })
  syncBuiltinESMExports()
  try {
    const result = await runOwnedCommand(process.execPath, ['-e', 'setTimeout(() => process.exit(0), 100)'], { lockPath: path, graceMs: 50 })
    assert.equal(result.code, 0)
    assert.ok(calls >= 2)
    assert.equal(result.metrics.remaining_processes, 0)
    assert.equal(existsSync(path), false)
  } finally { injected.mock.restore(); syncBuiltinESMExports() }
})

test('supervisor leaves an already-exited root exit code alone after repeated ps misses', async t => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-root-exited-')), 'suite.lock')
  const original = childProcess.spawnSync
  let calls = 0
  const injected = t.mock.method(childProcess, 'spawnSync', (command, args, options) => {
    if (command === 'ps' && args[0] === '-p' && args[1] !== String(process.pid)) { calls++; holdUntilRootExit(original, args[1]); return { status: 1, stdout: '' } }
    return original(command, args, options)
  })
  syncBuiltinESMExports()
  try {
    const result = await runOwnedCommand(process.execPath, ['-e', `${slowRoot}; process.exit(7)`], { lockPath: path, graceMs: 50 })
    assert.equal(result.code, 7)
    assert.ok(calls > 0)
    assert.equal(result.metrics.remaining_processes, 0)
    assert.equal(existsSync(path), false)
  } finally { injected.mock.restore(); syncBuiltinESMExports() }
})

test('persistent supervisor root verification failure kills the group before releasing its lock', async t => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-root-supervisor-fail-')), 'suite.lock')
  const original = childProcess.spawnSync
  let calls = 0, rootPid
  const injected = t.mock.method(childProcess, 'spawnSync', (command, args, options) => {
    if (command === 'ps' && args[0] === '-p' && args[1] !== String(process.pid)) {
      calls++; rootPid = Number(args[1]); return { status: 1, stdout: '' }
    }
    return original(command, args, options)
  })
  syncBuiltinESMExports()
  try {
    await assert.rejects(runOwnedCommand(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { lockPath: path, graceMs: 50 }), /spawned group terminated/)
    assert.equal(calls, 20)
    assert.throws(() => process.kill(-rootPid, 0), { code: 'ESRCH' })
    assert.equal(existsSync(path), false)
    assert.deepEqual(readdirSync(join(path, '..')), [], 'terminated root leaves neither lock nor journal')
  } finally { injected.mock.restore(); syncBuiltinESMExports() }
})

for (const { mode, blind } of [
  { mode: 'transient', blind: false },
  { mode: 'transient', blind: true },
  { mode: 'persistent', blind: false },
  { mode: 'owner-change', blind: false },
]) {
  test(`root preload handles ${mode} ps misses${blind ? ' with a blind supervisor' : ''} before suite code executes`, async t => {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-root-preload-')), lockPath = join(directory, 'suite.lock')
    const attempts = join(directory, 'attempts'), ready = join(directory, 'ready')
    const verified = join(directory, 'root-verified')
    const preload = new URL('./testdata/playwright/root-start-miss.mjs', import.meta.url).href
    const script = `const fs = require('node:fs'); fs.writeFileSync(${JSON.stringify(ready)}, fs.readFileSync(process.env.AEON_PW_GROUP_LOG));`
    // A verified supervisor releases the preload through a journal barrier.
    // A blind supervisor waits for root exit, so bounded identity retries never
    // race the slow preload. Both cases isolate the preload's journal entry.
    const originalAppend = fs.appendFileSync, originalSpawn = childProcess.spawnSync, supervisorEntries = []
    const injected = mode !== 'transient' ? undefined : blind
      ? t.mock.method(childProcess, 'spawnSync', (command, args, options) => {
        if (command === 'ps' && args[0] === '-p' && args[1] !== String(process.pid)) {
          holdUntilRootExit(originalSpawn, args[1]); return { status: 1, stdout: '' }
        }
        return originalSpawn(command, args, options)
      })
      : t.mock.method(fs, 'appendFileSync', (file, data, ...options) => {
        if (typeof file === 'string' && file.startsWith(`${lockPath}.`) && file.endsWith('.groups')) {
          supervisorEntries.push(JSON.parse(data))
          writeFileSync(verified, '')
          return
        }
        return originalAppend(file, data, ...options)
      })
    syncBuiltinESMExports()
    let result
    try {
      result = await runOwnedCommand(process.execPath, ['-e', script], {
        lockPath, graceMs: 50,
        env: { ...process.env, AEON_PW_TEST_ATTEMPTS: attempts, AEON_PW_TEST_MISS: mode,
          ...(mode === 'transient' ? blind ? { AEON_PW_TEST_START_DELAY_MS: String(SLOW_ROOT_MS) } : { AEON_PW_TEST_ROOT_VERIFIED: verified } : {}),
          NODE_OPTIONS: `${process.env.NODE_OPTIONS ?? ''} --import=${preload}` },
      })
    } finally { injected?.mock.restore(); syncBuiltinESMExports() }
    const calls = readFileSync(attempts, 'utf8').trim().split('\n').length
    assert.ok(calls >= 2)
    assert.equal(result.metrics.remaining_processes, 0)
    assert.equal(existsSync(lockPath), false)
    if (mode === 'transient') {
      assert.equal(result.code, 0)
      const entries = readFileSync(ready, 'utf8').trim().split('\n').map(line => JSON.parse(line))
      assert.equal(entries.length, 1, 'preload must publish the verified root before suite code')
      if (blind) {
        assert.equal(supervisorEntries.length, 0, 'blind supervisor must not publish an unverified root')
        assert.equal(existsSync(verified), false, 'blind preload must finish without supervisor verification')
      } else {
        assert.equal(supervisorEntries.length, 1, 'supervisor verifies the root before releasing the preload')
        assert.deepEqual(entries, supervisorEntries, 'preload independently records the same verified root')
      }
      assert.ok(entries.every(entry => validStart(entry.started)))
      assert.equal(new Set(entries.map(entry => entry.pid)).size, 1)
    } else if (mode === 'persistent') {
      assert.equal(result.code, 1)
      assert.equal(existsSync(ready), false, 'unverifiable root must not execute suite code')
      assert.equal(calls, 20)
      assert.equal(result.metrics.signal, 'SIGKILL')
    } else {
      assert.equal(result.code, 1)
      assert.equal(existsSync(ready), false, 'supervisor identity must be checked after recording the root')
      assert.equal(calls, 2)
    }
  })
}

function seedGuard(path, owner, legacy = false) {
  if (legacy) writeFileSync(`${path}.guard`, JSON.stringify(owner))
  else {
    mkdirSync(`${path}.guard`)
    writeFileSync(`${path}.guard/${owner.token}`, JSON.stringify(owner))
  }
}

test('dead and reused recovery guards are reclaimed without signalling their PIDs', async () => {
  for (const legacy of [false, true]) {
    for (const reused of [false, true]) {
      const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-reaper-')), 'suite.lock'), owner = expiredOwner()
      writeFileSync(path, JSON.stringify(owner))
      writeFileSync(`${path}.${owner.token}.groups`, '')
      const reaper = reused ? { pid: process.pid, token: randomUUID(), started: 'Mon Jan 1 00:00:00 2001' } : owner
      seedGuard(path, reaper, legacy)
      const lock = await acquireLock(path, { graceMs: 50 })
      assert.notEqual(lock.token, owner.token)
      assert.equal(existsSync(`${path}.guard`), false)
      lock.release()
      assert.equal(existsSync(path), false)
    }
  }
})

test('live matching and malformed recovery guards stay held with their exact path in the error', async () => {
  for (const legacy of [false, true]) {
    for (const malformed of [false, true]) {
      const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-reaper-live-')), 'suite.lock')
      const owner = { pid: process.pid, token: randomUUID(), started: malformed ? '' : processStart(process.pid) }
      seedGuard(path, owner, legacy)
      await assert.rejects(acquireLock(path), error => error.message.includes(`${path}.guard`))
      assert.equal(existsSync(path), false)
      assert.deepEqual(JSON.parse(readFileSync(legacy ? `${path}.guard` : `${path}.guard/${owner.token}`, 'utf8')), owner)
      assert.deepEqual(readdirSync(join(path, '..')), ['suite.lock.guard'], 'failed contenders leave no prepared claims')
    }
  }
})

test('a failed ps cannot turn a live recovery guard into stale ownership', async t => {
  const path = join(mkdtempSync(join(tmpdir(), 'aeon-pw-guard-ps-')), 'suite.lock'), sentinel = detachedProbe()
  const owner = { pid: sentinel.child.pid, token: randomUUID(), started: processStart(sentinel.child.pid) }
  seedGuard(path, owner)
  const original = childProcess.spawnSync
  const injected = t.mock.method(childProcess, 'spawnSync', (command, args, options) => {
    if (command === 'ps' && args[0] === '-p' && args[1] === String(sentinel.child.pid)) return { status: 1, stdout: '' }
    return original(command, args, options)
  })
  syncBuiltinESMExports()
  try {
    await assert.rejects(acquireLock(path), /recovery guard retained/)
    assert.equal(statSync(`${path}.guard`).isDirectory(), true)
    assert.deepEqual(JSON.parse(readFileSync(`${path}.guard/${owner.token}`, 'utf8')), owner)
    process.kill(sentinel.child.pid, 0)
    assert.equal(existsSync(path), false)
  } finally {
    injected.mock.restore(); syncBuiltinESMExports()
    sentinel.child.kill('SIGKILL'); await sentinel.completion
  }
})

test('competing stale-guard reapers cannot remove a replacement claim or admit two suites', async () => {
  for (const legacy of [false, true]) {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-guard-race-')), path = join(directory, 'suite.lock'), owner = expiredOwner()
    writeFileSync(path, JSON.stringify(owner))
    writeFileSync(`${path}.${owner.token}.groups`, '')
    seedGuard(path, owner, legacy)
    const contenders = Array.from({ length: 3 }, (_, index) => {
      const ready = join(directory, `claim-${index}.ready`)
      const child = spawn(process.execPath, [new URL('./testdata/playwright/lock-claim.mjs', import.meta.url).pathname, path, ready], { stdio: ['ignore', 'pipe', 'pipe'] })
      let done = false, log = ''
      child.stderr.on('data', data => { log += data })
      const completion = new Promise(resolve => child.once('close', code => { done = true; resolve(code) }))
      return { child, ready, completion, exited: () => done, log: () => log }
    })
    try {
      await waitFor(() => contenders.filter(probe => probe.exited() || existsSync(probe.ready)).length === contenders.length)
      const winners = contenders.filter(probe => existsSync(probe.ready))
      assert.equal(winners.length, 1, contenders.map(probe => probe.log()).join('\n'))
      const winner = winners[0], token = JSON.parse(readFileSync(path, 'utf8')).token
      assert.equal(JSON.parse(readFileSync(winner.ready, 'utf8')).token, token)
      assert.equal(existsSync(`${path}.guard`), false)
      await assert.rejects(acquireLock(path), /Another Aeon browser suite/)
      assert.equal(JSON.parse(readFileSync(path, 'utf8')).token, token)
      winner.child.kill('SIGTERM')
      await winner.completion
      assert.equal(existsSync(path), false)
    } finally {
      for (const probe of contenders) if (!probe.exited()) probe.child.kill('SIGTERM')
      await Promise.all(contenders.map(probe => probe.completion))
    }
  }
})

for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGKILL']) {
  test(`${signal} during stale recovery cannot strand the recovery guard or launch a suite`, async () => {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-reaper-interrupt-')), probe = fixture('hang', directory)
    let reaper, tracker, lock
    try {
      await waitReady(probe.ready, probe.exited)
      const owner = JSON.parse(readFileSync(probe.lock, 'utf8')), log = `${probe.lock}.${owner.token}.groups`
      tracker = trackGroups(log); tracker.snapshot()
      const killed = new Promise(resolve => probe.child.once('exit', resolve))
      probe.child.kill('SIGKILL'); await killed
      reaper = fixture('normal', directory)
      await waitFor(() => {
        const directory = `${probe.lock}.guard`
        if (!existsSync(directory)) return false
        const entries = readdirSync(directory)
        return entries.length === 1 && JSON.parse(readFileSync(`${directory}/${entries[0]}`, 'utf8')).pid === reaper.child.pid
      })
      reaper.child.kill(signal)
      if (signal === 'SIGKILL') {
        await reaper.completion
        assert.equal(existsSync(`${probe.lock}.guard`), true)
        lock = await acquireLock(probe.lock, { graceMs: 50 })
        assert.equal(existsSync(`${probe.lock}.guard`), false)
        assert.equal(existsSync(log), false)
        lock.release(); lock = undefined
      } else {
        assert.equal(await reaper.completion, { SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[signal], reaper.log())
        assert.equal(existsSync(`${probe.lock}.guard`), false)
        assert.equal(existsSync(probe.lock), false)
      }
      assert.equal(existsSync(reaper.ready), false, 'interruption must not start the requested suite')
      assert.throws(() => process.kill(Number(readFileSync(probe.pid, 'utf8')), 0), { code: 'ESRCH' })
      await probe.completion
    } finally {
      if (reaper && !reaper.exited()) { reaper.child.kill('SIGTERM'); await reaper.completion }
      if (!probe.exited()) {
        if (tracker) { try { tracker.signal('SIGKILL') } catch { /* recovery already removed the journal */ } }
        probe.child.kill('SIGTERM'); await probe.completion
      }
      lock?.release()
    }
  })
}

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
