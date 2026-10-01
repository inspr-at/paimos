// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { defineConfig } from '@playwright/test'
import { browserPolicy, headlessChromium, uiShardPolicy } from './playwright.policy.ts'

// Each checkout gets its own stable port (derived from its path), and a run never
// reuses a server it did not start: parallel worktrees sharing 5175 once ran one
// worktree's specs against another worktree's code (2026-09-25).
const derived = 5200 + (parseInt(createHash('sha256').update(process.cwd()).digest('hex').slice(0, 4), 16) % 700)
const port = process.env.PLAYWRIGHT_PORT ?? String(derived)

export default defineConfig({
  testDir: './tests',
  testMatch: '**/*.spec.ts',
  ...browserPolicy(process.env, 4, true),
  // AEON-410's one-worker shards must override policy after the spread.
  ...uiShardPolicy(process.env),
  use: { ...headlessChromium, baseURL: `http://127.0.0.1:${port}`, reducedMotion: 'reduce' },
  webServer: {
    // Mode test keeps the dev server (DEV stays true) and is the only build that
    // includes the header-glimpse pin. Production builds leave that hook out.
    command: `npm run dev -- --host 127.0.0.1 --port ${port} --strictPort --mode test`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: process.env.PLAYWRIGHT_REUSE === '1',
  },
})
