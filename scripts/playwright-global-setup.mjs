// SPDX-License-Identifier: AGPL-3.0-only
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { processStart, validStart } from './playwright-processes.mjs'

// A fixed host directory prevents different worktree TMPDIR values from
// accidentally creating independent locks on the same machine.
export const suiteLockPath = join('/tmp', `aeon-playwright-${process.getuid?.() ?? 'user'}.lock`)

export function verifySupervisor(path = suiteLockPath, env = process.env) {
  // CI sharding (AEON-410) also supports direct Playwright invocations. Hosted
  // jobs are isolated; local and remote Mac invocations must be supervised.
  if (env.CI && !['0', 'false'].includes(env.CI)) return
  try {
    const lock = JSON.parse(readFileSync(path, 'utf8'))
    if (!env.AEON_PW_RUN || lock.token !== env.AEON_PW_RUN) throw new Error('unowned lock')
    process.kill(lock.pid, 0)
    if (!validStart(lock.started) || processStart(lock.pid) !== lock.started) throw new Error('reused supervisor PID')
  } catch {
    throw new Error('Run browsers through npm test, npm run e2e, or npm run audit:ui so the host lock and interrupt cleanup are active. Full suites belong in CI or npm run test:remote.')
  }
}

// All browser configs call this same preflight: PR/merge-queue shards, nightly,
// e2e and performance. Reuse P02's assets and aliases without changing CSS or
// installing fonts on the host. Workers inherit this run's private fontconfig.
export async function setupBrowserFonts() {
  if (process.platform !== 'linux') return
  const { installRendererFonts, sampleRenderedFont, assertBodyFontEvidence } = await import('./test-prune/visual-renderer.mjs')
  const visual = JSON.parse(readFileSync(new URL('./test-prune/visual-manifest.json', import.meta.url), 'utf8'))
  const directory = mkdtempSync(join(tmpdir(), 'aeon-playwright-fonts-'))
  const prior = process.env.FONTCONFIG_FILE
  let fontconfig
  const cleanup = () => {
    if (process.env.FONTCONFIG_FILE === fontconfig) {
      if (prior === undefined) delete process.env.FONTCONFIG_FILE
      else process.env.FONTCONFIG_FILE = prior
    }
    rmSync(directory, { recursive: true, force: true })
  }
  try {
    fontconfig = installRendererFonts(visual, directory)
    // Use the browser API's ESM entry, as the owned-browser fixture does.
    // Nightly redirects @playwright/test to its ESM coverage fixture; requiring
    // that fixture through Playwright's transform fails before any test starts.
    const { chromium } = await import('../web/node_modules/playwright/index.mjs')
    const browser = await chromium.launch({ headless: true, args: ['--disable-gpu'], timeout: 30_000,
      env: { ...process.env, FONTCONFIG_FILE: fontconfig } })
    try {
      const page = await browser.newPage()
      page.setDefaultTimeout(10_000)
      // Probe the product's actual body stack, rather than trusting the CSS
      // family name: system-ui can resolve to JetBrains Mono on a Linux image.
      const tokens = readFileSync(new URL('../web/src/styles/tokens.css', import.meta.url), 'utf8')
      await page.setContent(`<style>${tokens}\nbody { font-family: var(--font); }</style><p>Proportional body Wiim</p>`)
      const body = await sampleRenderedFont(page, 'body')
      assertBodyFontEvidence(body)
      console.log(`Browser font guard ok; computed body: ${body.computedFamily}; resolved: ${body.renderedFamilies.join(', ')}`)
    } finally { await browser.close() }
    process.env.FONTCONFIG_FILE = fontconfig
    // Forward this teardown from each global setup so the private file remains
    // available until every worker browser has exited.
    return cleanup
  } catch (error) { cleanup(); throw error }
}

export default function requireSupervisor() {
  verifySupervisor()
  return setupBrowserFonts()
}
