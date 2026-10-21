// SPDX-License-Identifier: AGPL-3.0-only
// Test helpers deliberately outlive their immediate parents. Keep an independent
// journal so test teardown still owns them after a supervisor/worker is killed.
import childProcess from 'node:child_process'
import { appendFileSync, mkdtempSync, rmSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { groupProcesses, recordSpawnedGroup, trackGroups } from '../playwright-processes.mjs'

export const maxFixtureMs = 10 * 60_000
export function boundFixture({ timeoutMs = Number(process.env.AEON_FIXTURE_TIMEOUT_MS ?? maxFixtureMs), parent,
  clock = globalThis, exit = code => process.exit(code), parentPID = () => process.ppid } = {}) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > maxFixtureMs) throw new Error('Invalid fixture lifetime')
  const deadline = clock.setTimeout(() => exit(124), timeoutMs)
  deadline.unref?.()
  const watcher = parent === undefined ? undefined : clock.setInterval(() => { if (parentPID() !== parent) exit(125) }, 50)
  watcher?.unref?.()
  return () => { clock.clearTimeout(deadline); if (watcher !== undefined) clock.clearInterval(watcher) }
}

// NODE_OPTIONS propagates this test-only preload through detached Node helpers.
// Production ownership journals and their PID-reuse checks remain independent.
if (process.env.AEON_FIXTURE_GROUP_LOG) {
  boundFixture()
  const append = row => appendFileSync(process.env.AEON_FIXTURE_GROUP_LOG, JSON.stringify(row) + '\n')
  // Publish before work even if the spawning worker died between spawn and
  // recording. A removed journal directory makes this preload fail closed.
  if (groupProcesses(process.pid).some(row => row.pid === process.pid)) recordSpawnedGroup({ pid: process.pid }, append)
  const spawn = childProcess.spawn, fork = childProcess.fork
  const record = (child, options) => options?.detached ? recordSpawnedGroup(child, append) : child
  childProcess.spawn = function (command, args, options) { return record(spawn.call(this, command, args, options), Array.isArray(args) ? options : args) }
  childProcess.fork = function (module, args, options) { return record(fork.call(this, module, args, options), Array.isArray(args) ? options : args) }
  syncBuiltinESMExports()
}

export function fixtureOwner() {
  const owned = new Set()
  const cleanup = () => {
    const errors = []
    for (const entry of owned) {
      const failures = entry.tracker.signal('SIGKILL')
      // Re-read after stopping the roots to include a concurrent spawn that
      // published while the first snapshot was being signalled.
      failures.push(...entry.tracker.signal('SIGKILL'))
      const state = entry.tracker.snapshot()
      if (state.unverified.length) failures.push(new Error(`Unverified fixture groups retained: ${state.unverified.join(',')}`))
      errors.push(...failures)
      if (!failures.length) { owned.delete(entry); rmSync(entry.directory, { recursive: true, force: true }) }
    }
    if (errors.length) throw new AggregateError(errors, 'Could not clean up owned test fixtures')
  }
  const onExit = () => { try { cleanup() } catch (error) { console.error(error.message); process.exitCode = 1 } }
  process.on('exit', onExit)
  for (const [signal, code] of [['SIGINT', 130], ['SIGTERM', 143], ['SIGHUP', 129]]) process.on(signal, () => {
    onExit()
    process.exit(code)
  })
  return {
    cleanup,
    snapshot: () => [...owned].flatMap(entry => entry.tracker.snapshot().groups),
    spawn(command, args, options = {}) {
      const directory = mkdtempSync(join(tmpdir(), 'aeon-fixture-')), log = join(directory, 'groups')
      appendFileSync(log, '')
      const env = { ...(options.env ?? process.env), AEON_FIXTURE_GROUP_LOG: log }
      // A nested --test invocation must start its own runner, rather than
      // inheriting the current worker's internal node:test context.
      delete env.NODE_TEST_CONTEXT
      env.NODE_OPTIONS = `${env.NODE_OPTIONS ?? ''} --import=${new URL('./fixture-lifecycle.mjs', import.meta.url).href}`
      const child = childProcess.spawn(command, args, { ...options, env, detached: true })
      const entry = { directory, tracker: trackGroups(log) }
      owned.add(entry)
      recordSpawnedGroup(child, row => appendFileSync(log, JSON.stringify(row) + '\n'))
      entry.tracker.snapshot()
      // A deadline kills the whole group even if the non-Node command hangs.
      const deadline = setTimeout(cleanup, maxFixtureMs)
      deadline.unref()
      child.once('close', () => clearTimeout(deadline))
      return child
    },
  }
}
