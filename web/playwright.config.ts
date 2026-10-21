// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig } from '@playwright/test'
import { browserPolicy, headlessChromium } from './playwright.policy.ts'

export default defineConfig({
  ...browserPolicy(process.env),
  globalSetup: '../scripts/ci/e2e-stack-setup.mjs',
  testDir: './e2e',
  use: {
    ...headlessChromium,
    baseURL: process.env.BASE_URL ?? 'http://127.0.0.1:8080',
  },
})
