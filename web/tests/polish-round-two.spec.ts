// SPDX-License-Identifier: AGPL-3.0-only
// AEON-56 / PL2: PL2_SHOTS=before|after captures the same mocked screen matrix.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { crmData, mockCRM, HOFER } from './crm-fixtures'
import { businessData, mockBusiness, NOW } from './business-fixtures'
import { mockQuoteEditor, QUOTE_ID } from './quote-inspector-fixtures'
import { journeyWorld, mockJourney, type JourneyStart } from './journey-fixtures'
import { releaseHistory, mockReleases } from './releases-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })
const round = process.env.PL2_SHOTS
const shots = resolve('..', '.agent-shots', 'pl2', round ?? 'after')
const scenarios = [
  { name: 'companies', path: '/business/customers', ready: '.name-link, .card-link' },
  { name: 'company', path: `/business/customers/${HOFER}`, ready: '.hero' },
  { name: 'contacts', path: `/business/customers/${HOFER}`, ready: '.contact', scroll: '[aria-labelledby="contacts-title"]' },
  { name: 'notes', path: `/business/customers/${HOFER}`, ready: '.notes-body', scroll: '.notes' },
  { name: 'quote-editor', path: `/business/quotes/${QUOTE_ID}`, ready: '.quote-document[data-quote-ready="true"]' },
  { name: 'quote-preview', path: `/business/quotes/${QUOTE_ID}`, ready: '.quote-document[data-quote-ready="true"]' },
  { name: 'hours', path: '/business/hours', ready: '.period-chip' },
  { name: 'entries', path: '/business/hours', ready: '.e-title', scroll: '.day-group' },
  { name: 'periods', path: '/business/hours?view=approvals&period=p-me-38', ready: '[aria-label="Period review"]' },
  ...(['inspire', 'shape', 'plan', 'build'] as const).map(stage => ({ name: stage, path: `/p/PHAROS/journey?stage=${stage}`, ready: '.j-grid' })),
  { name: 'walker', path: '/p/PHAROS/journey?stage=plan&walk=PHAROS-11', ready: '.walker-bar' },
  { name: 'releases', path: '/releases', ready: '.listbox .row' },
  { name: 'ticket-full', path: '/p/PHAROS/PHAROS-11?view=full', ready: 'article.ticket-ws .title-text' },
]

async function setup(page: Page, name: string) {
  await mockWork(page, fixtures())
  if (['companies', 'company', 'contacts', 'notes'].includes(name)) await mockCRM(page, crmData())
  if (name.startsWith('quote-')) await mockQuoteEditor(page)
  if (['hours', 'entries', 'periods'].includes(name)) await mockBusiness(page, businessData())
  if (['inspire', 'shape', 'plan', 'build', 'walker'].includes(name)) await mockJourney(page, journeyWorld(name === 'walker' ? 'plan' : name as JourneyStart))
  if (name === 'releases') await mockReleases(page, releaseHistory(NOW.getTime()))
  await page.clock.setSystemTime(NOW)
}

for (const scenario of scenarios) for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`PL2 ${scenario.name} ${width} ${theme}`, async ({ page }) => {
    test.setTimeout(60_000)
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await setup(page, scenario.name)
    await page.goto(scenario.path)
    await expect(page.locator(scenario.ready).first()).toBeVisible({ timeout: 30_000 })
    if ('scroll' in scenario) await page.locator(scenario.scroll!).first().evaluate(el => el.scrollIntoView({ block: 'start' }))
    if (scenario.name === 'quote-preview') await page.emulateMedia({ media: 'print' })
    await page.evaluate(() => document.fonts.ready)
    await page.waitForTimeout(500)
    if (round) {
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: resolve(shots, `${scenario.name}-${width}-${theme}.png`) })
      const contrast = await new AxeBuilder({ page }).withRules(['color-contrast']).exclude('.calendar-version').analyze()
      writeFileSync(resolve(shots, `${scenario.name}-${width}-${theme}.json`), JSON.stringify(contrast.violations.map(v => ({ id: v.id, nodes: v.nodes.map(n => ({ target: n.target, summary: n.failureSummary })) })), null, 2))
      const detail: Record<string, string> = { inspire: '.draft', shape: '.decide', plan: '.add-row', build: '.progress', 'quote-editor': '.quote-page .quote-positions', 'quote-preview': '.quote-page .quote-positions', 'ticket-full': '.sections' }
      if (detail[scenario.name]) {
        await page.locator(detail[scenario.name]).first().evaluate(el => el.scrollIntoView({ block: 'center' }))
        await page.screenshot({ path: resolve(shots, `${scenario.name}-${width}-${theme}-detail.png`) })
      }
      if (scenario.name === 'releases') {
        await page.locator('.list-pane').evaluate(el => { el.scrollTop = 220 })
        await page.screenshot({ path: resolve(shots, `${scenario.name}-${width}-${theme}-detail.png`) })
      }
    }
    if (round === 'before') return
    expect(errors).toEqual([])
    if (scenario.name !== 'quote-preview') expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)

    if (scenario.name.startsWith('quote-')) {
      const table = page.locator('.quote-page .quote-positions table').first()
      for (const column of [3, 4, 5, 6]) {
        const heading = await table.locator(`th:nth-child(${column})`).evaluate(el => getComputedStyle(el).textAlign)
        const value = await table.locator(`tbody tr:first-child td:nth-child(${column})`).first().evaluate(el => getComputedStyle(el).textAlign)
        expect(heading).toBe(value)
      }
      await expect(page.locator('.quote-page').first()).toHaveCSS('color', 'rgb(32, 60, 61)')
    }
    if (scenario.name === 'periods') {
      const contrast = await new AxeBuilder({ page }).include('.metrics').withRules(['color-contrast']).analyze()
      expect(contrast.violations).toEqual([])
    }
    if (scenario.name === 'shape') {
      const rightEdges = await page.locator('.decide-btn .sub').evaluateAll(els => els.map(el => el.getBoundingClientRect().right))
      expect(rightEdges).toHaveLength(4)
      expect(Math.max(...rightEdges) - Math.min(...rightEdges)).toBeLessThanOrEqual(1)
    }
    if (width !== 390) return
    const controls: Record<string, string> = {
      companies: '.toolbar .field, .toolbar .seg, .toolbar .btn',
      contacts: '.add-contact, .contacts .row-actions .icon-btn',
      notes: '.note-actions .btn',
      inspire: '.j-card .btn.sm', plan: '.j-card .btn.sm', build: '.j-card .btn.sm',
      walker: '.walker-bar .tools button',
    }
    if (controls[scenario.name]) {
      const heights = await page.locator(controls[scenario.name]).evaluateAll(els => els.map(el => el.getBoundingClientRect().height).filter(h => h > 0))
      expect(heights.length).toBeGreaterThan(0)
      for (const height of heights) expect(height).toBe(44)
    }
    if (scenario.name === 'hours') {
      const collisions = await page.locator('.week-grid td.c-day').evaluateAll(cells => cells.filter(cell => {
        const range = document.createRange(); range.selectNodeContents(cell)
        const text = range.getBoundingClientRect(), box = cell.getBoundingClientRect()
        return text.left < box.left + 1 || text.right > box.right - 1
      }).map(cell => cell.textContent))
      expect(collisions).toEqual([])
      const scroll = page.getByRole('region', { name: 'Weekly hours, scroll for all days' })
      await scroll.focus()
      for (let i = 0; i < 5; i++) await page.keyboard.press('ArrowRight')
      await expect.poll(() => scroll.evaluate(el => el.scrollLeft)).toBeGreaterThan(0)
    }
    if (scenario.name === 'entries') {
      const note = page.locator('.e-meta').first()
      await expect(note).toHaveCSS('display', 'block')
      await expect(note).toHaveCSS('text-overflow', 'ellipsis')
      expect(await note.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true)
    }
    if (scenario.name === 'walker') {
      for (const button of await page.locator('.walker-bar .tools button').all()) {
        const box = (await button.boundingBox())!, icon = (await button.locator('svg').boundingBox())!
        expect(Math.abs(box.x + box.width / 2 - icon.x - icon.width / 2)).toBeLessThanOrEqual(1)
        expect(Math.abs(box.y + box.height / 2 - icon.y - icon.height / 2)).toBeLessThanOrEqual(1)
      }
    }
  })
}
