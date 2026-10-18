// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig } from '@playwright/test'
import { browserPolicy, headlessChromium } from './playwright.policy.ts'

export default defineConfig({
  testDir: './tests',
  testMatch: '**/performance.spec.ts',
  ...browserPolicy(process.env, 1),
  fullyParallel: false,
  use: { ...headlessChromium, baseURL: 'http://127.0.0.1:5177', reducedMotion: 'reduce' },
  webServer: {
    // A guarded remote run starts from a fresh checkout. Build this head,
    // rather than throttling the dev source graph or serving an older dist.
    command: 'npm run build && npm run preview -- --host 127.0.0.1 --port 5177 --strictPort',
    url: 'http://127.0.0.1:5177',
    reuseExistingServer: false,
  },
})
