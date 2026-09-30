// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from '@playwright/test'

// Each checkout gets its own stable port (derived from its path), and a run never
// reuses a server it did not start: parallel worktrees sharing 5175 once ran one
// worktree's specs against another worktree's code (2026-09-25).
const derived = 5200 + (parseInt(createHash('sha256').update(process.cwd()).digest('hex').slice(0, 4), 16) % 700)
const port = process.env.PLAYWRIGHT_PORT ?? String(derived)

// Four workers is the default for a targeted local run. CI passes --workers=2
// against a static test build: four browsers plus a Vite dev server saturated a
// 4-vCPU runner (AEON-410). Specs mock the API inside the page, and fixture
// factories return fresh objects (AEON-373). scripts/check-fixture-mutation.mjs
// rejects a spec that mutates an imported fixture, which is what leaked across
// files that shared one worker.
const nightly = process.env.PW_NIGHTLY === '1'

// Playwright's --shard slices the expanded test list in order, so every graph
// spec landed in one shard and that shard ran long after the others had finished.
// PW_FILE_SHARD=3/6 packs spec files by expanded test count instead. The slow
// files below are placed on different shards first; the rest fill the lightest
// shard. The route audit stays out of this split (one file, sharded on its own).
// Counts live in playwright.ui.weights.json. A spec missing from that map still
// runs: its weight is the number of test() declarations, so a new file is never
// dropped. Do not shell out to `playwright test --list` here; that re-enters
// this config.
const SLOW_SPECS = [
  'ticket-workers.spec.ts',
  'header-glimpse.spec.ts',
  'polish-round.spec.ts',
  'project-sections.spec.ts',
  'knowledge-graph.spec.ts',
  'header-glimpse-context.spec.ts',
]

function specFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const abs = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...specFiles(abs))
    else if (entry.name.endsWith('.spec.ts') && entry.name !== 'ui-audit.spec.ts') out.push(abs)
  }
  return out
}

function declarationWeight(abs: string): number {
  const body = readFileSync(abs, 'utf8')
  const found = body.match(/^\s*test(?:\.(?:only|skip|fixme))?\(/gm)
  return Math.max(1, found?.length ?? 0)
}

function fileShard(spec: string | undefined): string[] | undefined {
  if (!spec) return undefined
  const [current, all] = spec.split('/').map(Number)
  if (!Number.isInteger(current) || !Number.isInteger(all) || current < 1 || all < 1 || current > all) {
    throw new Error(`PW_FILE_SHARD must be current/all, got ${spec}`)
  }
  const here = dirname(fileURLToPath(import.meta.url))
  const testsDir = join(here, 'tests')
  const weights = (JSON.parse(readFileSync(join(here, 'playwright.ui.weights.json'), 'utf8')) as { counts: Record<string, number> }).counts
  const slowNames = new Set(SLOW_SPECS)
  const items = specFiles(testsDir).map(abs => {
    const rel = relative(testsDir, abs).split(sep).join('/')
    return { rel, weight: weights[rel] ?? declarationWeight(abs) }
  })
  const bins: { rel: string }[][] = Array.from({ length: all }, () => [])
  const load = Array.from({ length: all }, () => 0)
  const place = (item: { rel: string; weight: number }, emptyOfSlow: boolean) => {
    let target = emptyOfSlow ? bins.findIndex(bin => !bin.some(other => slowNames.has(other.rel.split('/').pop()!))) : -1
    if (target < 0) {
      target = 0
      for (let i = 1; i < all; i++) if (load[i] < load[target]) target = i
    }
    bins[target].push(item)
    load[target] += item.weight
  }
  for (const name of SLOW_SPECS) {
    const item = items.find(candidate => candidate.rel.split('/').pop() === name)
    if (item) place(item, true)
  }
  const rest = items.filter(item => !slowNames.has(item.rel.split('/').pop()!))
  rest.sort((a, b) => b.weight - a.weight || a.rel.localeCompare(b.rel))
  for (const item of rest) place(item, false)
  const chosen = bins[current - 1]
  if (chosen.length === 0) throw new Error(`PW_FILE_SHARD ${spec} produced an empty shard`)
  return chosen.map(item => `**/${item.rel}`)
}

const shardMatch = fileShard(process.env.PW_FILE_SHARD)

export default defineConfig({
  testDir: './tests',
  testMatch: '**/*.spec.ts',
  fullyParallel: true,
  workers: 4,
  // Stable tests do not retry. The quarantine project overrides this. Playwright
  // applies a CLI --retries before the project value, so a nightly --retries=0
  // clears the quarantine override on purpose.
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['github'], ['line']] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    browserName: 'chromium',
    reducedMotion: 'reduce',
    screenshot: 'only-on-failure',
    trace: 'on-first-retry',
  },
  projects: [
    // The route audit is its own project so a file shard does not also carry that
    // serial-sized file. CI shards the audit on its own.
    { name: 'ui', grepInvert: /@quarantine/, testIgnore: '**/ui-audit.spec.ts', ...(shardMatch ? { testMatch: shardMatch } : {}), retries: 0 },
    { name: 'audit', testMatch: '**/ui-audit.spec.ts', retries: 0 },
    {
      // Known flakes only. Review weekly. Nightly sets PW_NIGHTLY=1 (and passes
      // --retries=0) so these tests fail instead of hiding behind a retry.
      // - releases.spec.ts compare (AEON-387)
      // - palette.spec.ts actions, around line 153
      // - ticket-workers.spec.ts narrow list at 800px
      name: 'quarantine',
      grep: /@quarantine/,
      ...(shardMatch ? { testMatch: shardMatch } : {}),
      retries: nightly ? 0 : 2,
    },
  ],
  webServer: {
    // Mode test keeps the header-glimpse pin (production builds leave it out).
    // A static preview server replaces the dev server, which was transforming
    // modules for every worker on the runner (AEON-410).
    command: `npx vite build --mode test && npx vite preview --host 127.0.0.1 --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: process.env.PLAYWRIGHT_REUSE === '1',
    timeout: 180_000,
  },
})
