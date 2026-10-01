// SPDX-License-Identifier: AGPL-3.0-only
// Shared by every config; keep CI parallelism owned by its config/shard runner.
export function browserPolicy(env: NodeJS.ProcessEnv, ciWorkers?: number) {
  const ci = !!env.CI && env.CI !== '0' && env.CI !== 'false'
  const override = env.PW_WORKERS
  if (override !== undefined && (!/^[1-9]\d*$/.test(override) || !Number.isSafeInteger(Number(override)))) {
    throw new Error('PW_WORKERS must be a positive integer')
  }
  return {
    workers: override === undefined ? (ci ? ciWorkers : 1) : Number(override),
    fullyParallel: ci,
    globalSetup: '../scripts/playwright-global-setup.mjs',
  }
}

// No channel selects Playwright's bundled Chromium headless shell. Setting
// channel: 'chromium' would opt into the full browser's newer headless mode.
export const headlessChromium = {
  browserName: 'chromium' as const,
  headless: true,
  launchOptions: { args: ['--disable-gpu'] },
}
