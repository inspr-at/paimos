// SPDX-License-Identifier: AGPL-3.0-only
// Imported before the ownership preload to simulate root-only ps misses.
import childProcess from 'node:child_process'
import { appendFileSync, existsSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { setTimeout as delay } from 'node:timers/promises'

// Keep the preload behind the supervisor's identity check. Readiness, rather
// than child startup speed, determines when this fixture may proceed.
// Only the transient verified path publishes this file. Persistent and
// owner-change passes set the same variable and must not wait on it.
if (process.env.AEON_PW_TEST_MISS === 'transient' && process.env.AEON_PW_TEST_ROOT_VERIFIED) {
  const deadline = Date.now() + 15000
  while (!existsSync(process.env.AEON_PW_TEST_ROOT_VERIFIED)) {
    if (Date.now() >= deadline) throw new Error('Supervisor root verification barrier timed out')
    await delay(10)
  }
}

// A slow root: the supervisor must not give up on a start-up that outlasts
// its attempt budget while the preload is still establishing identity.
const startDelay = Number(process.env.AEON_PW_TEST_START_DELAY_MS ?? 0)
if (startDelay > 0) Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, startDelay)

const original = childProcess.spawnSync
// Keep the transient root alive until the supervisor verifies it. This is a
// barrier, not a startup-speed assumption; the deadline only guards a hang.
if (process.env.AEON_PW_TEST_MISS === 'transient' && process.env.AEON_PW_TEST_ROOT_VERIFIED) {
  const deadline = Date.now() + 15000
  const pause = new Int32Array(new SharedArrayBuffer(4))
  while (!existsSync(process.env.AEON_PW_TEST_ROOT_VERIFIED)) {
    if (Date.now() >= deadline) throw new Error('Supervisor root verification barrier timed out')
    Atomics.wait(pause, 0, 0, 10)
  }
}
let attempts = 0
childProcess.spawnSync = function (command, args, options) {
  if (process.env.AEON_PW_TEST_MISS === 'owner-change' && attempts > 0 && command === 'ps' && args[0] === '-p' && args[1] === process.env.AEON_PW_OWNER) return { status: 0, stdout: 'Mon Jan 1 00:00:00 2001' }
  if (command === 'ps' && args[0] === '-p' && args[1] === String(process.pid)) {
    appendFileSync(process.env.AEON_PW_TEST_ATTEMPTS, `${++attempts}\n`)
    if (attempts === 1 || process.env.AEON_PW_TEST_MISS === 'persistent') return { status: 1, stdout: '' }
  }
  return original.call(this, command, args, options)
}
syncBuiltinESMExports()
