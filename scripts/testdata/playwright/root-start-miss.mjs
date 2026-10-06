// SPDX-License-Identifier: AGPL-3.0-only
// Imported before the ownership preload to simulate root-only ps misses.
import childProcess from 'node:child_process'
import { appendFileSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'

// A slow root start (a loaded host) must never decide the supervisor's verdict.
const slowStart = Number(process.env.AEON_PW_TEST_SLOW_START_MS)
if (slowStart > 0) Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, slowStart)

const original = childProcess.spawnSync
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
