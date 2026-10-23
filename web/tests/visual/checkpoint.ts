// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, type Browser, type Page, type TestInfo } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { readBaselineBundle, referenceGate } from '../../../scripts/test-prune/visual-baselines.mjs'
import { assertRenderer, captureEnabled, rendererIdentity } from '../../../scripts/test-prune/visual-renderer.mjs'
import { domAudit } from '../ui-audit-rules'

const manifest = JSON.parse(readFileSync(fileURLToPath(new URL('../../../scripts/test-prune/visual-manifest.json', import.meta.url)), 'utf8'))
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

export async function checkVisual(browser: Browser, info: TestInfo, checkpoint: Checkpoint, setup: VisualSetup) {
    const gate = await reportReference(info, checkpoint)
    // Product not built yet: report the explicit authority and return before
    // context creation, fixture setup or any product requirement assertion.
    if (gate.mode === 'deferred') {
      writeFileSync(info.outputPath(`${checkpoint.id}.md`), JSON.stringify({ id: checkpoint.id, reference: gate, requirementsRun: false }, null, 2))
      return
    }
    const capture = captureEnabled(manifest)
    if (capture && !manifest.capture.states.includes(gate.state)) throw new Error(`State is outside the manual capture set: ${gate.state}`)
    const renderer = rendererIdentity(manifest, browser.version(), { browserOptions: info.project.use })
    if (capture || gate.mode === 'compare') assertRenderer(renderer, manifest.renderer, { capture })
    const viewport = manifest.profiles[checkpoint.profile]
    const context = await browser.newContext({ viewport, deviceScaleFactor: manifest.renderer.deviceScaleFactor,
      colorScheme: checkpoint.theme, reducedMotion: manifest.renderer.reducedMotion,
      hasTouch: checkpoint.profile === 'phone', locale: manifest.renderer.locale, timezoneId: manifest.renderer.timezone })
    const page = await context.newPage()
    const errors: string[] = []
    const responses: { path: string; status: number }[] = []
    page.on('response', response => { if (response.status() >= 400) responses.push({ path: new URL(response.url()).pathname, status: response.status() }) })
    page.on('console', message => { if (message.type() === 'error') errors.push(`${message.text()} ${message.location().url}`) })
    page.on('pageerror', error => errors.push(error.message))
    // All relative fixtures and the application use this one clock. Timers
    // continue normally so readiness never depends on a frozen animation frame.
    await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
    try {
      await setup(page, checkpoint.profile, checkpoint.theme)
      await prepareVisualFonts(page, String(info.project.use.baseURL))
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
      const fonts = await renderedFonts(page)
      const png = capture || gate.mode === 'compare' ? await page.screenshot({ path: info.outputPath(`${checkpoint.id}.png`), animations: 'disabled', caret: 'hide' }) : null
      const layout = (await page.evaluate(domAudit)).filter(row => ['horizontal-overflow', 'interactive-overlap', 'clipped-text'].includes(row.kind))
      const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const violations = axe.violations.map(row => ({ id: row.id, impact: row.impact, nodes: row.nodes.map(node => node.target) }))
      const report = { id: checkpoint.id, owner: checkpoint.owner, fixture: checkpoint.fixture,
        captureSHA, pngSHA256: png ? createHash('sha256').update(png).digest('hex') : null, baselineCompared: false,
        reference: gate, viewport, theme: checkpoint.theme, renderer, fonts, requirementsRun: true,
        captureMode: capture, layout, axe: violations, console: errors, responses }
      const saveReport = () => writeFileSync(info.outputPath(`${checkpoint.id}.md`), JSON.stringify(report, null, 2))
      saveReport()
      expect(errors, 'unexpected console/page failure').toEqual([])
      expect(responses, 'unexpected failed HTTP reads').toEqual([])
      expect(layout, 'unexpected clipping, overlap or overflow').toEqual([])
      expect(violations, 'WCAG A/AA violations').toEqual([])
      report.baselineCompared = !capture && gate.mode === 'compare' ? await compareVisualReference(page, info, checkpoint, { renderer }) : false
      saveReport()
    } catch (error) {
      if (capture || gate.mode === 'compare') await page.screenshot({ path: info.outputPath(`failed-${checkpoint.id}.png`), animations: 'disabled', caret: 'hide' }).catch(() => {})
      writeFileSync(info.outputPath(`failed-${checkpoint.id}.md`), `${checkpoint.id}: ${String(error)}\n${JSON.stringify({ console: errors, responses }, null, 2)}\n`)
      throw error
    } finally { await context.close() }
}

async function reportReference(info: TestInfo, checkpoint: { id: string }, visual = manifest) {
  const gate = referenceGate(visual, checkpoint.id)
  if (gate.mode !== 'compare') {
    const message = gate.mode === 'deferred' ? `${checkpoint.id}: ${gate.reason}; product requirement checks not run.` :
      `${checkpoint.id}: awaiting reference (${gate.status}); quality checks required. ${gate.reason}`
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
  if (gate.mode !== 'compare') {
    await reportReference(info, checkpoint, visual)
    return false
  }
  assertRenderer(renderer, visual.renderer)
  materializeReference(info, checkpoint, { visual, baselineFile })
  await expect(page).toHaveScreenshot(`${checkpoint.id}.png`, { fullPage: false, animations: 'disabled', caret: 'hide',
    threshold: visual.comparison.threshold, maxDiffPixels: visual.comparison.maxDiffPixels, timeout })
  return true
}

// Only test setup changes typography: production assets and styles stay owned
// by their product tickets. There is one app-hosted font in this checkout.
export async function prepareVisualFonts(page: Page, baseURL: string) {
  const url = new URL('/src/assets/fonts/jetbrains.woff2', baseURL).href
  await page.addStyleTag({ content: `@font-face { font-family: 'JetBrains Mono'; src: url('${url}') format('woff2'); font-weight: 100 800; font-display: block; }
    :root { --font: 'JetBrains Mono'; --serif: 'JetBrains Mono'; --mono: 'JetBrains Mono'; }
    *, *::before, *::after, *::placeholder, *::file-selector-button { font-family: 'JetBrains Mono' !important; }` })
  await page.evaluate(async () => {
    const loaded = await document.fonts.load('14px "JetBrains Mono"')
    if (!loaded.length || loaded.some(face => face.status !== 'loaded')) throw new Error('Self-hosted visual font did not load')
    await document.fonts.ready
  })
}

export async function renderedFonts(page: Page) {
  // CDP reports fonts from the painted layout. Await rendering after the last
  // style interaction, including the deliberate fallback negative control.
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
  const client = await page.context().newCDPSession(page)
  try {
    await client.send('DOM.enable'); await client.send('CSS.enable')
    const { root } = await client.send('DOM.getDocument')
    const { nodeIds } = await client.send('DOM.querySelectorAll', { nodeId: root.nodeId, selector: 'body, body *' })
    if (nodeIds.length > 10_000) throw new Error('Visual font audit exceeds its node bound')
    const rendered = new Map<string, { family: string; custom: boolean }>()
    for (const nodeId of nodeIds) {
      const { fonts } = await client.send('CSS.getPlatformFontsForNode', { nodeId })
      for (const font of fonts) if (font.glyphCount > 0) {
        if (!font.isCustomFont || font.familyName !== 'JetBrains Mono') {
          const { node } = await client.send('DOM.describeNode', { nodeId, depth: 0 })
          const { outerHTML } = await client.send('DOM.getOuterHTML', { nodeId })
          throw new Error(`System-font fallback in visual checkpoint: ${font.familyName} at ${node.nodeName}: ${outerHTML.slice(0, 200)}`)
        }
        rendered.set(font.familyName, { family: font.familyName, custom: font.isCustomFont })
      }
    }
    if (!rendered.size) throw new Error('Visual font audit found no rendered glyphs')
    return [...rendered.values()]
  } finally { await client.detach() }
}

export function materializeReference(info: TestInfo, checkpoint: { id: string; reference: { attachmentMember: string } },
  { visual = manifest, baselineFile = process.env.AEON_VISUAL_BASELINE_FILE } = {}) {
  if (info.config.updateSnapshots !== 'none') throw new Error('Visual baseline rewriting is forbidden; use the reviewed attachment')
  const state = visual.states.find((state: { checkpoints: { id: string }[] }) => state.checkpoints.some(row => row.id === checkpoint.id))
  if (!state) throw new Error(`Unknown visual checkpoint: ${checkpoint.id}`)
  const references = readBaselineBundle(baselineFile, visual, { stateIds: [state.id] })
  const reference = references.get(checkpoint.reference.attachmentMember)
  if (!reference) throw new Error(`Missing approved visual reference: ${checkpoint.id}`)
  const path = info.snapshotPath(`${checkpoint.id}.png`)
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, reference)
}
