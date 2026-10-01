// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { processStart, validStart } from './playwright-processes.mjs'

// A fixed host directory prevents different worktree TMPDIR values from
// accidentally creating independent locks on the same machine.
export const suiteLockPath = join('/tmp', `aeon-playwright-${process.getuid?.() ?? 'user'}.lock`)

export function verifySupervisor(path = suiteLockPath, env = process.env) {
  // CI sharding (AEON-410) also supports direct Playwright invocations. Hosted
  // jobs are isolated; local and remote Mac invocations must be supervised.
  if (env.CI && !['0', 'false'].includes(env.CI)) return
  try {
    const lock = JSON.parse(readFileSync(path, 'utf8'))
    if (!env.AEON_PW_RUN || lock.token !== env.AEON_PW_RUN) throw new Error('unowned lock')
    process.kill(lock.pid, 0)
    if (!validStart(lock.started) || processStart(lock.pid) !== lock.started) throw new Error('reused supervisor PID')
  } catch {
    throw new Error('Run browsers through npm test, npm run e2e, or npm run audit:ui so the host lock and interrupt cleanup are active. Full suites belong in CI or npm run test:remote.')
  }
}

export default function requireSupervisor() { verifySupervisor() }
