// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { defineConfig } from '@playwright/test'
import { browserPolicy, headlessChromium } from '../playwright.policy.ts'

const port = 18000 + parseInt(createHash('sha256').update(process.cwd()).digest('hex').slice(0, 4), 16) % 500
export default defineConfig({
  ...browserPolicy(process.env, 1), globalSetup: '../../scripts/playwright-global-setup.mjs', testDir: '.', testMatch: '**/baseline.spec.mjs',
  timeout: 120000, retries: 0, fullyParallel: false,
  outputDir: '../test-results/perf1355',
  use: { ...headlessChromium, baseURL: `http://127.0.0.1:${port}`, viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' },
  webServer: { cwd: fileURLToPath(new URL('..', import.meta.url)), command: `node perf/server.mjs ${port}`, url: `http://127.0.0.1:${port + 1000}`, reuseExistingServer: false, timeout: 600000 },
})
