// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, type Browser, type Page, type TestInfo } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { readBaselineBundle } from '../../../scripts/test-prune/visual-baselines.mjs'
import { domAudit } from '../ui-audit-rules'

const manifest = JSON.parse(readFileSync(fileURLToPath(new URL('../../../scripts/test-prune/visual-manifest.json', import.meta.url)), 'utf8'))
export type Profile = 'phone' | 'tablet' | 'desktop'
export type Theme = 'light' | 'dark'
export type VisualSetup = (page: Page, profile: Profile, theme: Theme) => Promise<void>
export interface Checkpoint {
  id: string
  profile: Profile
  theme: Theme
  fixture: string
  reference: { attachmentMember: string }
}

export function visualCases(id: string): Checkpoint[] {
  const state = manifest.states.find((state: { id: string }) => state.id === id)
  if (!state) throw new Error(`Unknown visual state: ${id}`)
  return state.checkpoints.map((checkpoint: Checkpoint) => ({ ...checkpoint, fixture: state.fixture }))
}

export async function checkVisual(browser: Browser, info: TestInfo, checkpoint: Checkpoint, setup: VisualSetup) {
    const viewport = manifest.profiles[checkpoint.profile]
    const context = await browser.newContext({ viewport, colorScheme: checkpoint.theme, reducedMotion: 'reduce',
      hasTouch: checkpoint.profile === 'phone', locale: 'en-GB', timezoneId: 'Europe/Vienna' })
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
      await page.evaluate(async theme => {
        document.documentElement.dataset.theme = theme
        await document.fonts.ready
        await Promise.all([...document.images].map(img => img.decode().catch(() => {})))
        await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      }, checkpoint.theme)
      // Always keep review evidence even when a quality check or the reference
      // authority fails. Capturing evidence is not a passing visual comparison.
      await page.screenshot({ path: info.outputPath(`${checkpoint.id}.png`), animations: 'disabled', caret: 'hide' })
      const layout = (await page.evaluate(domAudit)).filter(row => ['horizontal-overflow', 'interactive-overlap', 'clipped-text'].includes(row.kind))
      const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const violations = axe.violations.map(row => ({ id: row.id, impact: row.impact, nodes: row.nodes.map(node => node.target) }))
      writeFileSync(info.outputPath(`${checkpoint.id}.md`), JSON.stringify({ id: checkpoint.id, fixture: checkpoint.fixture,
        viewport, theme: checkpoint.theme, layout, axe: violations, console: errors, responses }, null, 2))
      expect(errors, 'unexpected console/page failure').toEqual([])
      expect(layout, 'unexpected clipping, overlap or overflow').toEqual([])
      expect(violations, 'WCAG A/AA violations').toEqual([])
      materializeReference(info, checkpoint)
      await expect(page).toHaveScreenshot(`${checkpoint.id}.png`, { fullPage: false, animations: 'disabled', caret: 'hide', threshold: .2, maxDiffPixels: 0 })
    } catch (error) {
      await page.screenshot({ path: info.outputPath(`failed-${checkpoint.id}.png`), animations: 'disabled', caret: 'hide' }).catch(() => {})
      writeFileSync(info.outputPath(`failed-${checkpoint.id}.md`), `${checkpoint.id}: ${String(error)}\n${JSON.stringify({ console: errors, responses }, null, 2)}\n`)
      throw error
    } finally { await context.close() }
}

export function materializeReference(info: TestInfo, checkpoint: { id: string; reference: { attachmentMember: string } }) {
  if (info.config.updateSnapshots !== 'none') throw new Error('Visual baseline rewriting is forbidden; use the reviewed attachment')
  const references = readBaselineBundle(process.env.AEON_VISUAL_BASELINE_FILE, manifest)
  const reference = references.get(checkpoint.reference.attachmentMember)
  if (!reference) throw new Error(`Missing approved visual reference: ${checkpoint.id}`)
  const path = info.snapshotPath(`${checkpoint.id}.png`)
  mkdirSync(dirname(path), { recursive: true })
  writeFileSync(path, reference)
}
