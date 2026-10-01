// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

// A fixed host directory prevents different worktree TMPDIR values from
// accidentally creating independent locks on the same machine.
export const suiteLockPath = join('/tmp', `aeon-playwright-${process.getuid?.() ?? 'user'}.lock`)

export default function requireSupervisor() {
  // CI sharding (AEON-410) also supports direct Playwright invocations. Hosted
  // jobs are isolated; local and remote Mac invocations must be supervised.
  if (process.env.CI && !['0', 'false'].includes(process.env.CI)) return
  try {
    const lock = JSON.parse(readFileSync(suiteLockPath, 'utf8'))
    if (!process.env.AEON_PW_RUN || lock.token !== process.env.AEON_PW_RUN) throw new Error('unowned lock')
    process.kill(lock.pid, 0)
  } catch {
    throw new Error('Run browsers through npm test, npm run e2e, or npm run audit:ui so the host lock and interrupt cleanup are active. Full suites belong in CI or npm run test:remote.')
  }
}
