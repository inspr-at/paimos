// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test as base, expect, type Browser, type Page, type TestInfo } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { readBaselineBundle, referenceGate } from '../../../scripts/test-prune/visual-baselines.mjs'
import { visualReportOnly, visualCheckResult } from '../../../scripts/test-prune/check.mjs'
import { assertRenderer, assertFontEvidence, assertClockEvidence, visualClock, captureEnabled, rendererIdentity, verifyFontAssets, installRendererFonts } from '../../../scripts/test-prune/visual-renderer.mjs'
import { domAudit } from '../ui-audit-rules'

const manifest = JSON.parse(readFileSync(fileURLToPath(new URL('../../../scripts/test-prune/visual-manifest.json', import.meta.url)), 'utf8'))
// Only tests/visual imports this fixture. Fontconfig belongs to this browser's
// child environment, never the worker, dev server or another spec's browser.
export const test = base.extend({
  launchOptions: [async ({ launchOptions }, use, workerInfo) => {
    if (process.platform !== 'linux') return use(launchOptions)
    const fontconfig = installRendererFonts(manifest, join(workerInfo.project.outputDir, `visual-fontconfig-${workerInfo.workerIndex}`))
    await use({ ...launchOptions, env: { ...process.env, ...launchOptions.env, FONTCONFIG_FILE: fontconfig } })
  }, { scope: 'worker' }],
})
const captureSHA = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: fileURLToPath(new URL('../../../', import.meta.url)), encoding: 'utf8' }).trim()
export type Profile = 'phone' | 'tablet' | 'desktop'
export type Theme = 'light' | 'dark'
export type VisualSetup = (page: Page, profile: Profile, theme: Theme) => Promise<void>
export interface Checkpoint {
  id: string
  profile: Profile
  theme: Theme
  fixture: string
  owner: string
  reference: { attachmentMember: string }
}

export function visualCases(id: string): Checkpoint[] {
  const state = manifest.states.find((state: { id: string }) => state.id === id)
  if (!state) throw new Error(`Unknown visual state: ${id}`)
  return state.checkpoints.map((checkpoint: Checkpoint) => ({ ...checkpoint, owner: state.owner,
    fixture: state.fixturesByProfile?.[checkpoint.profile] ?? state.fixture }))
}

export async function checkVisual(browser: Browser, info: TestInfo, checkpoint: Checkpoint, setup: VisualSetup, { visual = manifest } = {}) {
  if (captureEnabled(visual)) return runVisual(browser, info, checkpoint, setup, visual)
  const result = await visualCheckResult(visual, () => runVisual(browser, info, checkpoint, setup, visual))
  if (result.status === 'failed') {
    const path = info.outputPath(`${checkpoint.id}.md`)
    const previous = existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')) : {}
    const report = { ...previous, id: checkpoint.id, reference: referenceGate(visual, checkpoint.id),
      requirementsRun: true, quality: { status: 'failed', error: result.error },
      comparison: previous.comparison ?? { status: 'not run: quality/setup failed' } }
    writeFileSync(path, JSON.stringify(report, null, 2))
    info.annotations.push({ type: 'report-only quality failure', description: `${checkpoint.id}: ${result.error}` })
    await info.attach(`${checkpoint.id}-quality-failure`, { body: JSON.stringify(report), contentType: 'application/json' })
    console.log(`${checkpoint.id}: REPORT-ONLY quality failed: ${result.error}`)
  }
}

async function runVisual(browser: Browser, info: TestInfo, checkpoint: Checkpoint, setup: VisualSetup, visual: typeof manifest) {
    const capture = captureEnabled(visual)
    const reportOnly = visualReportOnly(visual) && !capture
    const gate = await reportReference(info, checkpoint, visual, capture)
    // Report-only checks all states. Explicit deferrals apply again when the
    // reviewed set-3 comparison policy becomes blocking.
    if (!reportOnly && (gate.mode === 'deferred' || gate.status === 'needs-re-capture') && !(capture && visual.capture.states.includes(gate.state))) {
      writeFileSync(info.outputPath(`${checkpoint.id}.md`), JSON.stringify({ id: checkpoint.id, reference: gate,
        requirementsRun: false, baselineCompared: false, pngSHA256: null }, null, 2))
      return
    }
    if (capture && !visual.capture.states.includes(gate.state)) throw new Error(`State is outside the manual capture set: ${gate.state}`)
    const renderer = rendererIdentity(visual, browser.version(), { browserOptions: info.project.use })
    if (capture || gate.mode === 'compare') assertRenderer(renderer, visual.renderer, { capture })
    const viewport = visual.profiles[checkpoint.profile]
    const context = await browser.newContext({ viewport, deviceScaleFactor: visual.renderer.deviceScaleFactor,
      colorScheme: checkpoint.theme, reducedMotion: visual.renderer.reducedMotion,
      hasTouch: checkpoint.profile === 'phone', locale: visual.renderer.locale, timezoneId: visual.renderer.timezone })
    const page = await context.newPage()
    const errors: string[] = []
    const responses: { path: string; status: number }[] = []
    page.on('response', response => { if (response.status() >= 400) responses.push({ path: new URL(response.url()).pathname, status: response.status() }) })
    page.on('console', message => { if (message.type() === 'error') errors.push(`${message.text()} ${message.location().url}`) })
    page.on('pageerror', error => errors.push(error.message))
    // Date stays fixed; timers and animation frames still run for readiness.
    const policy = visualClock(visual, gate.state)
    const clock = { mode: policy.mode, now: policy.now, samples: [] as { phase: string; now: number; iso: string }[] }
    const sampleClock = async (phase: string) => {
      const sample = await page.evaluate(() => ({ now: Date.now(), iso: new Date().toISOString() }))
      expect(sample, `fixture clock changed: ${checkpoint.id} (${phase})`).toEqual({ now: Date.parse(policy.now), iso: policy.now })
      clock.samples.push({ phase, ...sample })
    }
    await page.clock.setFixedTime(new Date(policy.now))
    try {
      await prepareVisualFonts(page, String(info.project.use.baseURL))
      await setup(page, checkpoint.profile, checkpoint.theme)
      await sampleClock('after-fixture')
      await page.evaluate(async theme => {
        document.documentElement.dataset.theme = theme
        await document.fonts.ready
        // decode() does not start a lazy off-screen image. Load the fixture's
        // images explicitly so focusing a preview cannot leave readiness stuck.
        for (const img of document.images) img.loading = 'eager'
        await Promise.all([...document.images].map(img => img.decode().catch(() => {})))
        await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      }, checkpoint.theme)
      const emulation = await page.evaluate(() => ({ scale: devicePixelRatio, locale: navigator.language,
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, reduced: matchMedia('(prefers-reduced-motion: reduce)').matches,
        theme: matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light' }))
      expect(emulation, 'renderer differs from reference: browser context settings').toEqual({ scale: renderer.deviceScaleFactor,
        locale: renderer.locale, timezone: renderer.timezone, reduced: true, theme: checkpoint.theme })
      const fonts = await fontEvidence(page, gate.state)
      await sampleClock('before-capture')
      console.log(`${checkpoint.id}: font guard ok; computed body: ${fonts.body.computedFamily}; resolved: ${fonts.body.renderedFamilies.join(', ')}; profile faces loaded: ${fonts.profile.length}`)
      const png = capture || reportOnly || gate.mode === 'compare' ? await page.screenshot({ path: info.outputPath(`${checkpoint.id}.png`), animations: 'disabled', caret: 'hide' }) : null
      const layout = (await page.evaluate(domAudit)).filter(row => ['horizontal-overflow', 'interactive-overlap', 'clipped-text'].includes(row.kind))
      const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const violations = axe.violations.map(row => ({ id: row.id, impact: row.impact, nodes: row.nodes.map(node => node.target) }))
      const expectedResponses = visual.states.find((state: { id: string }) => state.id === gate.state)?.expectedResponses ?? []
      const expected = (path: string, status: number) => expectedResponses.some((row: { path: string; status: number }) => row.path === path && row.status === status)
      const expectedReads = responses.filter(row => expected(row.path, row.status))
      const unexpectedResponses = responses.filter(row => !expected(row.path, row.status))
      const unexpectedErrors = errors.filter(error => !expectedReads.some(row =>
        error.startsWith(`Failed to load resource: the server responded with a status of ${row.status} `) && error.endsWith(row.path)))
      const report = { id: checkpoint.id, owner: checkpoint.owner, fixture: checkpoint.fixture,
        captureSHA, pngSHA256: png ? createHash('sha256').update(png).digest('hex') : null, baselineCompared: false,
        reference: gate, viewport, theme: checkpoint.theme, renderer, fonts, clock, requirementsRun: true,
        quality: { status: 'pending' }, comparison: { status: 'not run' },
        captureMode: capture, layout, axe: violations, console: unexpectedErrors, responses: unexpectedResponses, expectedReads }
      const saveReport = () => writeFileSync(info.outputPath(`${checkpoint.id}.md`), JSON.stringify(report, null, 2))
      saveReport()
      report.quality = await visualCheckResult(capture ? { ...visual, comparison: { ...visual.comparison, mode: 'blocking' } } : visual, async () => {
        expect(expectedReads, 'declared fixture refusal must be observed exactly once').toEqual(expectedResponses.map(({ path, status }: { path: string; status: number }) => ({ path, status })))
        expect(unexpectedErrors, 'unexpected console/page failure').toEqual([])
        expect(unexpectedResponses, 'unexpected failed HTTP reads').toEqual([])
        expect(layout, 'unexpected clipping, overlap or overflow').toEqual([])
        expect(violations, 'WCAG A/AA violations').toEqual([])
        await sampleClock('before-comparison')
        assertClockEvidence(clock, policy)
      })
      if (!capture && (reportOnly || gate.mode === 'compare')) {
        await compareVisualReference(page, info, checkpoint, { renderer, visual })
        report.comparison = JSON.parse(readFileSync(info.outputPath(`${checkpoint.id}-comparison.md`), 'utf8'))
        report.baselineCompared = ['matched', 'different'].includes(report.comparison.status)
      }
      saveReport()
      if (reportOnly) {
        await info.attach(`${checkpoint.id}-result`, { body: JSON.stringify(report), contentType: 'application/json' })
        console.log(`${checkpoint.id}: REPORT-ONLY quality ${report.quality.status}; comparison ${report.comparison.status}; awaiting re-capture under the final renderer`)
      }
    } catch (error) {
      if (capture || reportOnly || gate.mode === 'compare') await page.screenshot({ path: info.outputPath(`failed-${checkpoint.id}.png`), animations: 'disabled', caret: 'hide' }).catch(() => {})
      writeFileSync(info.outputPath(`failed-${checkpoint.id}.md`), `${checkpoint.id}: ${String(error)}\n${JSON.stringify({ console: errors, responses }, null, 2)}\n`)
      throw error
    } finally { await context.close() }
}

async function reportReference(info: TestInfo, checkpoint: { id: string }, visual = manifest, capture = false) {
  const gate = referenceGate(visual, checkpoint.id)
  if (gate.mode === 'report-only') {
    const message = `${checkpoint.id}: ${gate.reason}; quality checks required; ${gate.historicalReference ? 'historical comparison is report-only' : 'no historical PNG available'}.`
    info.annotations.push({ type: 'awaiting reference', description: message })
    await info.attach(`${checkpoint.id}-awaiting-reference`, { body: message, contentType: 'text/plain' })
    console.log(message)
    return gate
  }
  if (!['compare', 'report-only'].includes(gate.mode)) {
    const pending = capture && visual.capture.states.includes(gate.state)
    const message = gate.mode === 'deferred' ? `${checkpoint.id}: ${gate.reason}; ${capture && visual.capture.states.includes(gate.state) ? 'manual candidate capture requires quality checks; comparison remains deferred.' : 'product requirement checks not run.'}` :
      `${checkpoint.id}: awaiting reference (${gate.status}); ${gate.status === 'needs-re-capture' ? pending ? 'manual candidate capture requires quality checks; comparison remains awaiting reference' : 'report only; product requirement checks not run' : 'quality checks required'}. ${gate.reason}`
    info.annotations.push({ type: gate.mode === 'deferred' ? 'deferred' : 'awaiting reference', description: message })
    await info.attach(`${checkpoint.id}-awaiting-reference`, { body: message, contentType: 'text/plain' })
    console.log(message)
  }
  return gate
}

// Kept shared with the real checkpoint runner so negative controls exercise
// the same pinned/awaiting dispatch and pixel comparator as product states.
export async function compareVisualReference(page: Page, info: TestInfo, checkpoint: { id: string; reference: { attachmentMember: string } },
  { visual = manifest, baselineFile = process.env.AEON_VISUAL_BASELINE_FILE, timeout, renderer = rendererIdentity(visual, page.context().browser()!.version(), { browserOptions: info.project.use }) }:
    { visual?: typeof manifest; baselineFile?: string; timeout?: number; renderer?: Record<string, unknown> } = {}) {
  const gate = referenceGate(visual, checkpoint.id)
  if (!['compare', 'report-only'].includes(gate.mode)) {
    await reportReference(info, checkpoint, visual)
    return false
  }
  const comparison: { id: string; status: string; error?: string; rendererMismatch?: string; diffs: string[] } = {
    id: checkpoint.id, status: 'unavailable', diffs: [],
  }
  const result = await visualCheckResult(visual, async () => {
    // Historical pixels have no renderer authority. Keep metadata mismatches
    // visible while still producing their diagnostic diff; strict mode rejects.
    if (visualReportOnly(visual)) {
      try { assertRenderer(renderer, visual.renderer) }
      catch (error) { comparison.rendererMismatch = String(error) }
    } else assertRenderer(renderer, visual.renderer)
    await fontEvidence(page, gate.state)
    materializeReference(info, checkpoint, { visual, baselineFile })
    comparison.status = 'different'
    await expect(page).toHaveScreenshot(`${checkpoint.id}.png`, { fullPage: false, animations: 'disabled', caret: 'hide',
      threshold: visual.comparison.threshold, maxDiffPixels: visual.comparison.maxDiffPixels, timeout })
    comparison.status = 'matched'
  })
  if (result.status === 'failed') comparison.error = result.error
  comparison.diffs = info.attachments.filter(attachment => attachment.name.includes(checkpoint.id) && /diff/i.test(attachment.name))
    .map(attachment => attachment.path?.split(/[\\/]/).at(-1) ?? attachment.name)
  writeFileSync(info.outputPath(`${checkpoint.id}-comparison.md`), JSON.stringify(comparison, null, 2))
  if (gate.mode === 'report-only') {
    await info.attach(`${checkpoint.id}-comparison`, { body: JSON.stringify(comparison), contentType: 'application/json' })
    console.log(`${checkpoint.id}: REPORT-ONLY comparison ${comparison.status}${comparison.error ? `: ${comparison.error}` : ''}`)
  }
  return comparison.status === 'matched'
}

// Linux uses the private fontconfig installed before browser launch. The remote
// Mac quality lane uses the same Inter bytes through a test-only face; the
// product's CSS and fonts are never changed. Install before navigation so
// opening a popover cannot precede a typography/layout change.
export async function prepareVisualFonts(page: Page, baseURL: string) {
  verifyFontAssets(manifest)
  const mono = manifest.renderer.fonts.find((font: { asset: string }) => font.asset.endsWith('.ttf') && font.asset.includes('jetbrains'))
  const url = (asset: string) => new URL(`/${asset.replace(/^web\//, '')}`, baseURL).href
  const content = `@font-face { font-family: 'JetBrains Mono'; src: url('${url(mono.asset)}') format('truetype'); font-weight: 100 800; font-display: block; }
    ${process.platform === 'linux' ? '' : `@font-face { font-family: 'Inter Variable'; src: url('${url(manifest.renderer.fonts[0].asset)}') format('truetype'); font-weight: 100 900; font-display: block; }
    :root { --font: 'Inter Variable', -apple-system, system-ui, sans-serif !important; --serif: 'Inter Variable', -apple-system, system-ui, sans-serif !important; }`}`
  await page.addInitScript(content => {
    const add = () => {
      if (!document.head) return false
      const style = document.createElement('style'); style.dataset.visualFonts = ''; style.textContent = content
      document.head.append(style)
      return true
    }
    if (!add()) {
      const observer = new MutationObserver(() => { if (add()) observer.disconnect() })
      observer.observe(document, { childList: true, subtree: true })
    }
  }, content)
  if (page.url() !== 'about:blank') await page.addStyleTag({ content })
}

export async function renderedFonts(page: Page) {
  // CDP reports fonts from the painted layout. Await rendering after the last
  // style interaction, including the deliberate fallback negative control.
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
  const client = await page.context().newCDPSession(page)
  try {
    await client.send('DOM.enable'); await client.send('CSS.enable')
    await client.send('DOM.getDocument')
    const { result } = await client.send('Runtime.evaluate', { objectGroup: 'visual-fonts', expression: `
      Array.from(document.querySelectorAll('body, body *')).filter(el => {
        const directText = Array.from(el.childNodes).some(node => node.nodeType === 3 && node.textContent.trim());
        const textControl = ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName);
        const generatedText = ['::before', '::after'].some(pseudo => {
          const content = getComputedStyle(el, pseudo).content;
          return content && !['none', 'normal', '""'].includes(content);
        });
        if (!directText && !textControl && !generatedText) return false;
        if (!el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return false;
        const box = el.getBoundingClientRect();
        if (box.width <= 0 || box.height <= 0 || box.right <= 0 || box.bottom <= 0 || box.left >= innerWidth || box.top >= innerHeight) return false;
        // Screen-reader copies and opacity-zero tooltips have no painted
        // glyphs in the checkpoint. Never treat their layout fonts as pixels.
        const style = getComputedStyle(el);
        if (box.width <= 1 && box.height <= 1 && style.overflow === 'hidden') return false;
        return true;
      })` })
    if (!result.objectId) throw new Error('Visual font audit could not inspect rendered text')
    const { result: properties } = await client.send('Runtime.getProperties', { objectId: result.objectId, ownProperties: true })
    const objects = properties.filter(property => /^\d+$/.test(property.name) && property.value?.objectId)
    if (objects.length > 10_000) throw new Error('Visual font audit exceeds its node bound')
    const rendered = new Map<string, { family: string; custom: boolean }>()
    for (const object of objects) {
      const { nodeId } = await client.send('DOM.requestNode', { objectId: object.value!.objectId! })
      const { fonts } = await client.send('CSS.getPlatformFontsForNode', { nodeId })
      for (const font of fonts) if (font.glyphCount > 0) {
        if (!['Inter Variable', 'JetBrains Mono', 'Source Sans 3', 'Source Sans 3 Semibold', 'Source Sans 3 Bold', 'Source Serif 4 Display',
          ...(process.platform === 'darwin' ? ['.SF NS'] : [])].includes(font.familyName)) {
          const { node } = await client.send('DOM.describeNode', { nodeId, depth: 0 })
          const { outerHTML } = await client.send('DOM.getOuterHTML', { nodeId })
          throw new Error(`Visual font guard: unexpected painted font ${font.familyName} at ${node.nodeName}: ${outerHTML.slice(0, 200)}`)
        }
        rendered.set(font.familyName, { family: font.familyName, custom: font.isCustomFont })
      }
    }
    if (!rendered.size) throw new Error('Visual font audit found no rendered glyphs')
    return [...rendered.values()]
  } finally {
    await client.send('Runtime.releaseObjectGroup', { objectGroup: 'visual-fonts' }).catch(() => {})
    await client.detach()
  }
}

// A transparent probe inherits the actual computed stack and resolves glyphs
// through Chromium. font-family alone still says system-ui when fontconfig has
// accidentally mapped it to mono. Probes are removed before any screenshot.
export async function fontEvidence(page: Page, state: string) {
  await page.evaluate(() => document.fonts.ready)
  const quote = ['public-quote', 'quote-editor'].includes(state)
  if (quote) await page.waitForFunction(() => [...document.fonts].filter(face => face.family.replaceAll('"', '').startsWith('QuoteProfile')).length === 7)
  const client = await page.context().newCDPSession(page)
  const sample = async (selector: string) => {
    const computedFamily = await page.locator(selector).first().evaluate(element => {
      const probe = document.createElement('span')
      probe.id = 'visual-font-probe'; probe.textContent = 'Proportional body Wiim'
      probe.style.cssText = `position:fixed;opacity:0;pointer-events:none;font-family:${getComputedStyle(element).fontFamily}`
      document.body.append(probe)
      return getComputedStyle(element).fontFamily.replaceAll('"', '')
    })
    try {
      // Reading computed styles can be the first use of an injected face. Wait
      // after that read, before resolving its glyphs, even on the Mac lane.
      await page.evaluate(async () => {
        const probe = document.getElementById('visual-font-probe')!
        const style = getComputedStyle(probe)
        await document.fonts.load(`${style.fontSize} ${style.fontFamily}`)
        await document.fonts.ready
        await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      })
      const { root } = await client.send('DOM.getDocument')
      const { nodeId } = await client.send('DOM.querySelector', { nodeId: root.nodeId, selector: '#visual-font-probe' })
      const { fonts } = await client.send('CSS.getPlatformFontsForNode', { nodeId })
      return { computedFamily, renderedFamilies: [...new Set(fonts.filter(font => font.glyphCount > 0).map(font => font.familyName))].sort() }
    } finally { await page.locator('#visual-font-probe').evaluate(element => element.remove()) }
  }
  try {
    await client.send('DOM.enable'); await client.send('CSS.enable')
    const body = await sample('body')
    const profile = await page.evaluate(() => [...document.fonts].filter(face => face.family.replaceAll('"', '').startsWith('QuoteProfile')).map(face => ({
      family: face.family.replaceAll('"', ''), role: face.family.endsWith('body') ? 'body' : 'display', weight: face.weight, style: face.style,
      status: face.status, checked: document.fonts.check(`${face.style} ${face.weight} 14px "${face.family.replaceAll('"', '')}"`),
    })))
    const quoteDocument = quote ? { body: await sample('.quote-document'), display: await sample('.quote-document .quote-title') } : undefined
    const evidence = { body, profile, document: quoteDocument, painted: await renderedFonts(page) }
    assertFontEvidence(evidence, state, { platform: process.platform })
    return evidence
  } finally { await client.detach() }
}

export function materializeReference(info: TestInfo, checkpoint: { id: string; reference: { attachmentMember: string } },
  { visual = manifest, baselineFile = process.env.AEON_VISUAL_BASELINE_FILE } = {}) {
  if (info.config.updateSnapshots !== 'none') throw new Error('Visual baseline rewriting is forbidden; use the reviewed attachment')
  const state = visual.states.find((state: { checkpoints: { id: string }[] }) => state.checkpoints.some(row => row.id === checkpoint.id))
  if (!state) throw new Error(`Unknown visual checkpoint: ${checkpoint.id}`)
  const references = readBaselineBundle(baselineFile, visual, { checkpointIds: [checkpoint.id] })
  const reference = references.get(checkpoint.reference.attachmentMember ?? `${checkpoint.id}.png`)
  if (!reference) throw new Error(`Missing approved visual reference: ${checkpoint.id}`)
  const path = info.snapshotPath(`${checkpoint.id}.png`)
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, reference)
}
