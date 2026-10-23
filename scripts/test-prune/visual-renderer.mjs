// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

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
  const escape = value => value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;')
  const fontDirectory = fileURLToPath(new URL('../../web/tests/visual/fonts', import.meta.url))
  const aliases = Object.entries(visual.renderer.fontconfig.aliases)
    .map(([from, to]) => `<alias binding="strong"><family>${escape(from)}</family><prefer><family>${escape(to)}</family></prefer></alias>`).join('\n')
  const path = join(output, 'fonts.conf')
  writeFileSync(path, `<?xml version="1.0"?>\n<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">\n<fontconfig>\n<dir>${escape(fontDirectory)}</dir>\n<cachedir>${escape(join(output, 'cache'))}</cachedir>\n${aliases}\n</fontconfig>\n`)
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

export function assertFontEvidence(evidence, state) {
  const fail = detail => { throw new Error(`Visual font guard: ${detail}`) }
  if (!evidence || typeof evidence.body?.computedFamily !== 'string' ||
      !/(Inter|system-ui|-apple-system|sans-serif)/.test(evidence.body.computedFamily) ||
      /JetBrains|monospace/i.test(evidence.body.computedFamily) ||
      !same(evidence.body.renderedFamilies, ['Inter Variable'])) fail('body text must resolve to Inter sans')
  const allowed = new Set(['Inter Variable', 'JetBrains Mono', 'Source Sans 3', 'Source Serif 4 Display'])
  if (!Array.isArray(evidence.painted) || !evidence.painted.length ||
      evidence.painted.some(font => !allowed.has(font.family))) fail('unexpected painted font family')
  if (['public-quote', 'quote-editor'].includes(state)) {
    const faces = evidence.profile
    if (!Array.isArray(faces) || faces.length !== 7 || faces.some(face => face.status !== 'loaded' || face.checked !== true) ||
        faces.filter(face => face.role === 'body').length !== 6 || faces.filter(face => face.role === 'display').length !== 1)
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
  if (process.env.GITHUB_ENV) appendFileSync(process.env.GITHUB_ENV, `FONTCONFIG_FILE=${path}\n`)
  console.log(`FONTCONFIG_FILE=${path}`)
}
