// SPDX-License-Identifier: AGPL-3.0-only
// AEON-410 owns the shard planner and compiled UI graph. Supervise that
// existing runner as one command, including its --list and validation passes.
import { accessSync, constants } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { runOwnedCommand } from './playwright-safe.mjs'

export async function runUIShards(args, { runner = fileURLToPath(new URL('./playwright-ui-shards.mjs', import.meta.url)), env = process.env, ...options } = {}) {
  try { accessSync(runner, constants.R_OK) }
  catch { throw new Error('AEON-410 shard runner is not integrated in this checkout; use draft PR CI and retain the supervised entry point when merging PR #29') }
  return runOwnedCommand(process.execPath, [runner, ...args], {
    cwd: fileURLToPath(new URL('../web/', import.meta.url)),
    ...options,
    env: { ...env, AEON_PW_SHARD: '1', PW_WORKERS: '1' },
  })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = (await runUIShards(process.argv.slice(2))).code }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
