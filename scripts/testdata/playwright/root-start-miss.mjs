// SPDX-License-Identifier: AGPL-3.0-only
// Imported before the ownership preload to simulate root-only ps misses.
import childProcess from 'node:child_process'
import { appendFileSync, existsSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { setTimeout as delay } from 'node:timers/promises'

// Keep the preload behind the supervisor's identity check. Readiness, rather
// than child startup speed, determines when this fixture may proceed.
if (process.env.AEON_PW_TEST_ROOT_VERIFIED) {
  const deadline = Date.now() + 15000
  while (!existsSync(process.env.AEON_PW_TEST_ROOT_VERIFIED)) {
    if (Date.now() >= deadline) throw new Error('Supervisor root verification barrier timed out')
    await delay(10)
  }
}

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
