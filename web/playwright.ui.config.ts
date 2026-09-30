// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { defineConfig } from '@playwright/test'

// Each checkout gets its own stable port (derived from its path), and a run never
// reuses a server it did not start: parallel worktrees sharing 5175 once ran one
// worktree's specs against another worktree's code (2026-09-25).
const derived = 5200 + (parseInt(createHash('sha256').update(process.cwd()).digest('hex').slice(0, 4), 16) % 700)
const port = process.env.PLAYWRIGHT_PORT ?? String(derived)

// Parallelism is across CI jobs. One worker preserves the proven serial setup
// and teardown behavior within a shard. The fixture-mutation guard still rejects
// imported objects changed by a spec (AEON-373).
const nightly = process.env.PW_NIGHTLY === '1'
const compiled = process.env.PW_COMPILED_UI === '1'

export default defineConfig({
  testDir: './tests',
  testMatch: '**/*.spec.ts',
  fullyParallel: false,
  workers: 1,
  // Stable tests do not retry. The quarantine project overrides this. Playwright
  // applies a CLI --retries before the project value, so a nightly --retries=0
  // clears the quarantine override on purpose.
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['github'], ['line'], ['./playwright.ui.timings.ts']] : [['list'], ['./playwright.ui.timings.ts']],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    browserName: 'chromium',
    reducedMotion: 'reduce',
    screenshot: 'only-on-failure',
    trace: 'on-first-retry',
  },
  projects: [
    // The CI selector balances audit states with ordinary files in eight jobs.
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
    // The test build includes browser imports in the app's own module graph.
    // Local dev runs preserve the same source URLs and test-only galleries.
    command: compiled
      ? `npx vite build -c vite.ui.config.ts --mode test && node playwright.ui.server.mjs ${port}`
      : `npm run dev -- --host 127.0.0.1 --port ${port} --strictPort --mode test`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: process.env.PLAYWRIGHT_REUSE === '1',
    timeout: 180_000,
  },
})
