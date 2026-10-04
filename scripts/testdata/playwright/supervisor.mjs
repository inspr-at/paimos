// SPDX-License-Identifier: AGPL-3.0-only
import { writeFileSync } from 'node:fs'
import { runOwnedCommand } from '../../playwright-safe.mjs'
const [mode, lockPath, ready, pidFile, output] = process.argv.slice(2)
try {
  const result = await runOwnedCommand(process.execPath, [new URL('./owned-child.mjs', import.meta.url).pathname, mode, ready, pidFile], { lockPath, graceMs: 500 })
  writeFileSync(output, JSON.stringify(result))
  process.exitCode = result.code
} catch (error) { console.error(error.message); process.exitCode = ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[error.signal]) ?? 1 }
