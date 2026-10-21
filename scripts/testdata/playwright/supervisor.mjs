// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync, writeFileSync } from 'node:fs'
import { setTimeout as pause } from 'node:timers/promises'
import { runOwnedCommand } from '../../playwright-safe.mjs'
import { boundFixture } from '../fixture-lifecycle.mjs'
boundFixture()
const [mode, lockPath, ready, pidFile, output] = process.argv.slice(2)
const barrier = process.env.AEON_FIXTURE_RECOVERY_BARRIER
const lockOptions = barrier ? { pause: async () => {
  writeFileSync(barrier, 'ready')
  const deadline = Date.now() + 15000
  while (!existsSync(`${barrier}.release`)) {
    if (Date.now() >= deadline) throw new Error('Fixture recovery barrier timed out')
    await pause(10)
  }
} } : {}
try {
  const result = await runOwnedCommand(process.execPath, [new URL('./owned-child.mjs', import.meta.url).pathname, mode, ready, pidFile], { lockPath, graceMs: 500, lockOptions })
  writeFileSync(output, JSON.stringify(result))
  process.exitCode = result.code
} catch (error) { console.error(error.message); process.exitCode = ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[error.signal]) ?? 1 }
