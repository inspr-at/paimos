// SPDX-License-Identifier: AGPL-3.0-only
// Shared by every config; keep CI parallelism owned by its config/shard runner.
export function browserPolicy(env: NodeJS.ProcessEnv, ciWorkers?: number, ciParallel = false) {
  const ci = !!env.CI && env.CI !== '0' && env.CI !== 'false'
  const override = env.PW_WORKERS
  if (override !== undefined && (!/^[1-9]\d*$/.test(override) || !Number.isSafeInteger(Number(override)))) {
    throw new Error('PW_WORKERS must be a positive integer')
  }
  return {
    workers: override === undefined ? (ci ? ciWorkers : 1) : Number(override),
    fullyParallel: ci && ciParallel,
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

// Spread after browserPolicy in the UI config; CLI args cannot undo this
// budget because the existing shard runner also supplies --workers=1.
export function uiShardPolicy(env: NodeJS.ProcessEnv) {
  return env.AEON_PW_SHARD === '1' ? { workers: 1, fullyParallel: false } : {}
}
