// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'

const require = createRequire(new URL('../../web/package.json', import.meta.url))
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b)
const need = (ok, detail) => { if (!ok) throw new Error(`renderer differs from reference: ${detail}`) }

// Read only these non-secret runner metadata fields. ImageVersion is recorded
// by capture, then pinned by the person-reviewed reference round, never guessed.
export function rendererIdentity(visual, browserVersion, { env = process.env, platform = process.platform, arch = process.arch,
  browserOptions = { browserName: 'chromium', headless: true, launchOptions: { args: ['--disable-gpu'] } } } = {}) {
  const browsers = JSON.parse(readFileSync(join(dirname(require.resolve('playwright-core/package.json')), 'browsers.json'), 'utf8')).browsers
  const browser = browsers.find(row => row.name === 'chromium-headless-shell')
  return { ...structuredClone(visual.renderer),
    runner: { label: env.AEON_VISUAL_RUNNER ?? 'unidentified', imageOS: env.ImageOS ?? 'unidentified',
      imageVersion: env.ImageVersion ?? 'unidentified', platform, arch },
    playwright: require('@playwright/test/package.json').version,
    browser: { name: browserOptions.browserName === 'chromium' && !browserOptions.channel && browserOptions.headless !== false ? 'chromium-headless-shell' : 'different-browser',
      revision: browser.revision, version: browserVersion, headless: browserOptions.headless !== false, args: browserOptions.launchOptions?.args ?? [] },
    fonts: visual.renderer.fonts.map(font => ({ ...font, sha256: createHash('sha256')
      .update(readFileSync(new URL(`../../${font.asset}`, import.meta.url))).digest('hex') })),
  }
}

export function assertRenderer(actual, expected, { capture = false } = {}) {
  need(actual && expected, 'missing renderer identity')
  // Null is allowed only on an unapproved capture policy. An approved reference
  // must pin the exact ImageVersion that the hosted capture observed.
  if (capture) {
    need(typeof actual.runner?.imageVersion === 'string' && actual.runner.imageVersion !== 'unidentified', 'missing hosted runner ImageVersion')
    expected = { ...expected, runner: { ...expected.runner, imageVersion: actual.runner.imageVersion } }
  } else need(typeof expected.runner?.imageVersion === 'string' && expected.runner.imageVersion !== 'unidentified', 'reference has no pinned runner ImageVersion')
  for (const key of Object.keys(expected)) need(same(actual[key], expected[key]), key)
  need(same(Object.keys(actual).sort(), Object.keys(expected).sort()), 'identity fields')
}

export function captureEnabled(visual, env = process.env) {
  if (env.AEON_VISUAL_CAPTURE !== '1') return false
  if (env.GITHUB_EVENT_NAME !== 'workflow_dispatch' || env.GITHUB_WORKFLOW !== 'CI' || env.GITHUB_JOB !== 'visual-reference-capture')
    throw new Error('Visual capture is allowed only by the manual CI visual-reference-capture job')
  if (visual.capture?.event !== 'workflow_dispatch') throw new Error('Missing manual capture policy')
  return true
}
