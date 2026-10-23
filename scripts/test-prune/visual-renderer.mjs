// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, realpathSync, symlinkSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(new URL('../../web/package.json', import.meta.url))
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b)
const need = (ok, detail) => { if (!ok) throw new Error(`renderer differs from reference: ${detail}`) }

// Clock policy lives per state: Hours intentionally uses the business fixture's
// date, while Capacity and its account/catalog fixtures share their own instant.
// Keep it outside renderer identity so old immutable archives remain verifiable.
export function visualClock(visual, stateId) {
  const clock = visual.states.find(state => state.id === stateId)?.clock
  need(clock?.mode === 'fixed' && typeof clock.now === 'string' && Number.isFinite(Date.parse(clock.now)) &&
    new Date(clock.now).toISOString() === clock.now && typeof clock.audit === 'string' && clock.audit.trim().length > 0,
  `missing fixed fixture clock: ${stateId}`)
  return clock
}

export function assertClockEvidence(evidence, clock) {
  need(evidence?.mode === 'fixed' && evidence.now === clock.now && Array.isArray(evidence.samples) &&
    same(evidence.samples.map(sample => sample.phase), ['after-fixture', 'before-capture', 'before-comparison']) &&
    evidence.samples.every(sample => sample.now === Date.parse(clock.now) && sample.iso === clock.now),
  `fixture clock changed or missing: ${clock.now}`)
}

// A hosted image rollout is diagnostic metadata, never container identity.
export function assertVisualContainer(visual, { env = process.env, platform = process.platform, arch = process.arch } = {}) {
  if (platform !== 'linux' || arch !== 'x64' || !visual.renderer.container?.digest ||
      env.AEON_VISUAL_CONTAINER_DIGEST !== visual.renderer.container.digest)
    throw new Error('visual = CI only: use the digest-pinned Playwright container; missing/wrong AEON_VISUAL_CONTAINER_DIGEST or platform (agm2 native Chromium is not comparable)')
}

export function rendererIdentity(visual, browserVersion, { env = process.env, platform = process.platform, arch = process.arch,
  fontconfigFile = env.FONTCONFIG_FILE,
  browserOptions = { browserName: 'chromium', headless: true, launchOptions: { args: ['--disable-gpu'] } } } = {}) {
  const browsers = JSON.parse(readFileSync(join(dirname(require.resolve('playwright-core/package.json')), 'browsers.json'), 'utf8')).browsers
  const browser = browsers.find(row => row.name === 'chromium-headless-shell')
  return { ...structuredClone(visual.renderer),
    runner: { label: env.AEON_VISUAL_RUNNER ?? 'unidentified', imageOS: env.ImageOS ?? 'unidentified',
      imageVersion: env.ImageVersion ?? 'unidentified', platform, arch },
    container: { ...visual.renderer.container, digest: env.AEON_VISUAL_CONTAINER_DIGEST ?? 'unidentified', platform, arch },
    playwright: require('@playwright/test/package.json').version,
    browser: { name: browserOptions.browserName === 'chromium' && !browserOptions.channel && browserOptions.headless !== false ? 'chromium-headless-shell' : 'different-browser',
      revision: browser.revision, version: browserVersion, headless: browserOptions.headless !== false, args: browserOptions.launchOptions?.args ?? [] },
    fonts: visual.renderer.fonts.map(font => ({ ...font, sha256: createHash('sha256')
      .update(readFileSync(new URL(`../../${font.asset}`, import.meta.url))).digest('hex') })),
    fontconfig: { ...visual.renderer.fontconfig, sha256: fontconfigFile ? createHash('sha256').update(readFileSync(fontconfigFile)).digest('hex') : 'unidentified' },
  }
}

export function assertRenderer(actual, expected, { capture = false } = {}) {
  need(actual && expected, 'missing renderer identity')
  // Immutable historical archives retain their v1 identity checks. New captures
  // and comparisons use v2; a v1 archive can never match a v2 renderer.
  if (expected.version === 1 && capture) {
    need(typeof actual.runner?.imageVersion === 'string' && actual.runner.imageVersion !== 'unidentified', 'missing hosted runner ImageVersion')
    expected = { ...expected, runner: { ...expected.runner, imageVersion: actual.runner.imageVersion } }
  } else if (expected.version === 1) need(typeof expected.runner?.imageVersion === 'string' && expected.runner.imageVersion !== 'unidentified', 'reference has no pinned runner ImageVersion')
  else {
    need(/^sha256:[a-f0-9]{64}$/.test(expected.container?.digest ?? ''), 'reference has no pinned container digest')
    if (actual.container?.digest !== expected.container.digest)
      throw new Error('visual = CI only: renderer differs from reference: container digest (missing or wrong pinned container)')
  }
  for (const key of Object.keys(expected)) {
    if (expected.version === 2 && key === 'runner') continue
    need(same(actual[key], expected[key]), key)
  }
  need(same(Object.keys(actual).sort(), Object.keys(expected).sort()), 'identity fields')
}

export function captureEnabled(visual, env = process.env) {
  if (env.AEON_VISUAL_CAPTURE !== '1') return false
  if (env.GITHUB_EVENT_NAME !== 'workflow_dispatch' || env.GITHUB_WORKFLOW !== 'CI' || env.GITHUB_JOB !== 'visual-reference-capture')
    throw new Error('Visual capture is allowed only by the manual CI visual-reference-capture job')
  if (visual.capture?.event !== 'workflow_dispatch') throw new Error('Missing manual capture policy')
  return true
}

export function verifyFontAssets(visual) {
  for (const font of visual.renderer.fonts) {
    const bytes = readFileSync(new URL(`../../${font.asset}`, import.meta.url))
    need(createHash('sha256').update(bytes).digest('hex') === font.sha256, `font asset SHA-256: ${font.asset}`)
  }
}

// A private fontconfig, selected only for the test process, leaves the host's
// fonts and the product's CSS stacks intact. No download occurs on a runner.
export function installRendererFonts(visual, directory, { run = execFileSync } = {}) {
  verifyFontAssets(visual)
  const output = resolve(directory)
  mkdirSync(join(output, 'cache'), { recursive: true })
  const fontDirectory = fileURLToPath(new URL('../../web/tests/visual/fonts', import.meta.url))
  const link = join(output, 'fonts')
  if (!existsSync(link)) symlinkSync(fontDirectory, link, 'dir')
  need(realpathSync(link) === realpathSync(fontDirectory), 'private font directory')
  const path = join(output, 'fonts.conf')
  const bytes = readFileSync(new URL('../../' + visual.renderer.fontconfig.asset, import.meta.url))
  need(createHash('sha256').update(bytes).digest('hex') === visual.renderer.fontconfig.sha256, 'fontconfig file SHA-256')
  writeFileSync(path, bytes)
  const env = { ...process.env, FONTCONFIG_FILE: path }
  run('fc-cache', ['-f'], { env, timeout: 30_000, stdio: ['ignore', 'pipe', 'pipe'] })
  for (const [alias, family] of Object.entries(visual.renderer.fontconfig.aliases)) {
    // A bare pattern parses the hyphen in system-ui as a size separator.
    const actual = run('fc-match', ['--format=%{family}', `:family=${alias}`], { env, encoding: 'utf8', timeout: 10_000 }).trim()
    need(actual.split(',').includes(family), `fontconfig ${alias} resolved to ${actual}, expected ${family}`)
    console.log(`Fontconfig ${alias} -> ${actual}`)
  }
  return path
}

export function assertFontEvidence(evidence, state, { platform = 'linux' } = {}) {
  const fail = detail => { throw new Error(`Visual font guard: ${detail}`) }
  if (!evidence || typeof evidence.body?.computedFamily !== 'string' ||
      !/(Inter|system-ui|-apple-system|sans-serif)/.test(evidence.body.computedFamily) ||
      /JetBrains|monospace/i.test(evidence.body.computedFamily) ||
      !same(evidence.body.renderedFamilies, ['Inter Variable'])) fail(`body text must resolve to Inter sans: ${JSON.stringify(evidence?.body)}`)
  const allowed = new Set(['Inter Variable', 'JetBrains Mono', 'Source Sans 3', 'Source Sans 3 Semibold', 'Source Sans 3 Bold', 'Source Serif 4 Display',
    // Only the remote Mac quality lane has OS-rendered date-input segments.
    // Linux candidate packaging always uses the default and rejects these.
    ...(platform === 'darwin' ? ['.SF NS'] : [])])
  if (!Array.isArray(evidence.painted) || !evidence.painted.length ||
      evidence.painted.some(font => !allowed.has(font.family))) fail('unexpected painted font family')
  if (['public-quote', 'quote-editor'].includes(state)) {
    const faces = evidence.profile
    if (!Array.isArray(faces) || faces.length !== 7 || faces.some(face => face.status !== 'loaded' || face.checked !== true) ||
        !same(faces.map(face => `${face.role}:${face.weight}:${face.style}`).sort(),
          ['body:400:normal', 'body:400:italic', 'body:600:normal', 'body:600:italic', 'body:700:normal', 'body:700:italic', 'display:400:normal'].sort()))
      fail('quote profile faces must all be loaded')
    if (evidence.document?.body?.computedFamily !== faces.find(face => face.role === 'body').family ||
        !same(evidence.document?.body?.renderedFamilies, ['Source Sans 3']) ||
        evidence.document?.display?.computedFamily !== faces.find(face => face.role === 'display').family ||
        !same(evidence.document?.display?.renderedFamilies, ['Source Serif 4 Display']))
      fail('quote document must use its profile body and display fonts')
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv[2] !== 'install-fonts' || process.platform !== 'linux') throw new Error('Usage (Linux only): visual-renderer.mjs install-fonts')
  const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
  const path = installRendererFonts(visual, 'tmp/visual-fontconfig')
  console.log(`FONTCONFIG_FILE=${path}`)
}
