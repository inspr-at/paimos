// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { defineConfig } from '@playwright/test'

// Each checkout gets its own stable port (derived from its path), and a run never
// reuses a server it did not start: parallel worktrees sharing 5175 once ran one
// worktree's specs against another worktree's code (2026-09-25).
const derived = 5200 + (parseInt(createHash('sha256').update(process.cwd()).digest('hex').slice(0, 4), 16) % 700)
const port = process.env.PLAYWRIGHT_PORT ?? String(derived)

// Four workers is the default for a targeted local run. CI shards pass --workers=2:
// four browsers plus the Vite dev server saturated a 4-vCPU runner (AEON-410).
// Specs mock the API inside the page, and fixture factories return fresh objects
// (AEON-373). scripts/check-fixture-mutation.mjs rejects a spec that mutates an
// imported fixture, which is what leaked across files that shared one worker.
const nightly = process.env.PW_NIGHTLY === '1'

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
    // The route audit is its own project so a shard does not also carry that
    // serial-sized file. CI runs it as one job with several workers.
    { name: 'ui', grepInvert: /@quarantine/, testIgnore: '**/ui-audit.spec.ts', retries: 0 },
    { name: 'audit', testMatch: '**/ui-audit.spec.ts', retries: 0 },
    {
      // Known flakes only. Review weekly. Nightly sets PW_NIGHTLY=1 (and passes
      // --retries=0) so these tests fail instead of hiding behind a retry.
      // - releases.spec.ts compare (AEON-387)
      // - palette.spec.ts actions, around line 153
      // - ticket-workers.spec.ts narrow list at 800px
      name: 'quarantine',
      grep: /@quarantine/,
      retries: nightly ? 0 : 2,
    },
  ],
  webServer: {
    // Mode test keeps the dev server (DEV stays true) and is the only build that
    // includes the header-glimpse pin. Production builds leave that hook out.
    command: `npm run dev -- --host 127.0.0.1 --port ${port} --strictPort --mode test`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: process.env.PLAYWRIGHT_REUSE === '1',
  },
})
