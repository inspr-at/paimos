// SPDX-License-Identifier: AGPL-3.0-only
// U20 (AEON-99): the quote docked beside the Quotes list is a deliberate split.
// At 1024 and 1440 with the dock open: the list and the dock share
// the window with no dead gap; the page head never wraps its summary word by
// word; every Business tab shows whole; the quote title never collapses; the
// dock's title bar folds instead of overlapping. The divider sets the dock's own
// width (a ticket panel's width does not leak in), remembered per person, and
// the list always keeps its room.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { crmData, mockCRM } from './crm-fixtures'
import { MIRA, mockQuotes, presenceOf, quoteWorld, Q } from './quote-list-fixtures'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

async function docked(page: Page, options: { panel?: number; quotePanel?: number; id?: string; people?: boolean } = {}) {
  const work = fixtures()
  // The ticket panel's width (Markus had it wide) must not size the quote dock.
  work.preferences.layout = { panel: options.panel ?? 900, ...(options.quotePanel ? { quotePanel: options.quotePanel } : {}) }
  await mockWork(page, work)
  await mockCRM(page, crmData())
  const world = quoteWorld()
  if (options.people) world.presence = presenceOf([{ id: MIRA.id, name: MIRA.name, mode: 'editing' }, { id: '33333333-3333-4333-8333-333333333333', name: 'Jonas Berger', mode: 'viewing' }])
  await mockQuotes(page, world)
  await page.goto(`/business/quotes?quote=${options.id ?? Q.draft}`)
  await expect(page.locator('.quote-dock .quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await page.waitForTimeout(250)
  return work
}
type Box = { x: number; y: number; width: number; height: number }
const rect = (page: Page, selector: string) => page.locator(selector).first().evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
const overlaps = (a: Box, b: Box) => a.x < b.x + b.width - 0.5 && b.x < a.x + a.width - 0.5 && a.y < b.y + b.height - 0.5 && b.y < a.y + a.height - 0.5

// Every visible control of the dock's title bar, as boxes.
function titleBarControls(page: Page) {
  return page.locator('.quote-dock .titlebar').evaluate(bar => [...bar.querySelectorAll<HTMLElement>('button, a, .state-chip, .number, .save-text, .zoom, .frozen-profile')]
    .filter(el => { const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== 'hidden' && !el.closest('.floating') })
    // A control inside another listed one is part of it (the zoom's buttons, the save button's text).
    .filter((el, _i, all) => !all.some(other => other !== el && other.contains(el)))
    .map(el => { const r = el.getBoundingClientRect(); return { name: el.getAttribute('aria-label') || el.className || el.tagName, box: { x: r.x, y: r.y, width: r.width, height: r.height } } }))
}

for (const width of [1024, 1440]) {
  test(`at ${width} the list and the dock share the window: no gap, no overlap, nothing collapsed`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 1024 ? 768 : 900 })
    const errors = watchErrors(page)
    await docked(page)
    const dock = await rect(page, '.quote-dock')
    const list = await rect(page, '.quotes-list-page .table-card')
    // The dock sits at the right edge; the list reaches up to it, with only the split's gutter between.
    expect(dock.x + dock.width).toBeGreaterThan(width - 12)
    expect(dock.x - (list.x + list.width)).toBeGreaterThanOrEqual(8)
    expect(dock.x - (list.x + list.width)).toBeLessThanOrEqual(40)
    // The ticket panel's 900px did not size the quote dock, and the list keeps its room.
    expect(dock.width).toBeLessThan(Math.min(900, width - 470))
    expect(list.width).toBeGreaterThanOrEqual(400)

    // The page head: each summary part stays on one line (no word-by-word column).
    const parts = await page.locator('.quotes-list-page .summary .dot-list > *').evaluateAll(els => els.map(el => {
      const r = el.getBoundingClientRect(), line = parseFloat(getComputedStyle(el).lineHeight) || 20
      return { text: el.textContent?.trim(), lines: Math.round(r.height / line) }
    }))
    expect(parts.length).toBeGreaterThanOrEqual(2)
    for (const part of parts) expect(part.lines, part.text).toBe(1)
    const summary = await rect(page, '.quotes-list-page .summary')
    expect(summary.width).toBeGreaterThan(300)
    // The actions never sit on the summary.
    const actions = await rect(page, '.quotes-list-page .head-actions')
    expect(overlaps(actions, summary)).toBe(false)

    // Every tab shows whole inside its bar.
    const nav = await rect(page, '.quotes-list-page .biz-tabs')
    for (const tab of await page.locator('.quotes-list-page .biz-tab').evaluateAll(els => els.map(el => { const r = el.getBoundingClientRect(); const label = el.querySelector('span')!; return { x: r.x, width: r.width, clipped: label.scrollWidth > label.clientWidth + 1 } }))) {
      expect(tab.x).toBeGreaterThanOrEqual(nav.x - 0.5)
      expect(tab.x + tab.width).toBeLessThanOrEqual(nav.x + nav.width + 0.5)
      expect(tab.clipped).toBe(false)
    }

    // Number, title and status stay; the title keeps room for its words.
    const heads = await page.locator('.quotes thead th').allInnerTexts()
    expect(heads.map(h => h.trim().toLowerCase())).toEqual(expect.arrayContaining(['number', 'quote', 'status']))
    const title = await rect(page, `#quote-${Q.issued} .title-link`)
    expect(title.width).toBeGreaterThan(110)
    const chip = await page.locator(`#quote-${Q.issued} .number`).evaluate(el => el.scrollWidth <= el.clientWidth + 1)
    expect(chip).toBe(true)

    // The dock's title bar: no two controls overlap, none leaves the bar.
    const bar = await rect(page, '.quote-dock .titlebar')
    const controls = await titleBarControls(page)
    expect(controls.length).toBeGreaterThan(5)
    for (const c of controls) {
      expect(c.box.x, c.name).toBeGreaterThanOrEqual(bar.x - 0.5)
      expect(c.box.x + c.box.width, c.name).toBeLessThanOrEqual(bar.x + bar.width + 0.5)
    }
    for (let i = 0; i < controls.length; i++) for (let j = i + 1; j < controls.length; j++) {
      expect(overlaps(controls[i]!.box, controls[j]!.box), `${controls[i]!.name} overlaps ${controls[j]!.name}`).toBe(false)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(errors).toEqual([])
  })
}

test('what folds out of the dock title bar is in its … menu, and works there', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  // One row (the dock is wider than 780px), too narrow for every control once
  // two more people are here: labels go first, then the zoom.
  await docked(page, { quotePanel: 790, people: true })
  const bar = page.locator('.quote-dock .titlebar')
  await expect(bar).not.toHaveClass(/compact/)
  await expect(bar).toHaveClass(/fold-[2-4]/)
  await expect(bar.locator('.pdf-text')).toBeHidden()
  // The profile picker keeps its sheet and its name for screen readers, not its label.
  await expect(bar.getByRole('button', { name: /^Document profile: Standard document/ })).toBeVisible()
  await expect(bar.locator('.picker-name')).toBeHidden()
  const fold = Number((await bar.getAttribute('class'))!.match(/fold-(\d)/)![1])
  await page.locator('.quote-dock').getByRole('button', { name: 'More actions' }).click()
  const menu = page.getByRole('menu', { name: 'Quote actions' })
  if (fold >= 2) {
    await expect(bar.getByRole('group', { name: 'Zoom' })).toHaveCount(0)
    await expect(menu.getByRole('group', { name: /^Zoom, \d+ %$/ })).toBeVisible()
    await menu.getByRole('menuitem', { name: 'Fit page' }).click()
    await page.locator('.quote-dock').getByRole('button', { name: 'More actions' }).click()
    await expect(menu.getByRole('menuitem', { name: 'Fit page' })).toHaveAttribute('aria-current', 'true')
  } else {
    await expect(bar.getByRole('group', { name: 'Zoom' })).toBeVisible()
  }
  await expect(menu.getByRole('menuitem', { name: 'Duplicate as a new quote' })).toBeVisible()
})

test('the divider sets the dock’s own width, kept per person; the list keeps its room', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const work = await docked(page)
  const handle = page.getByRole('separator', { name: 'Resize the panel' })
  const before = await rect(page, '.quote-dock')
  const box = (await handle.boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + 100)
  await page.mouse.down()
  await page.mouse.move(box.x - 180, box.y + 100, { steps: 8 })
  await page.mouse.up()
  const after = await rect(page, '.quote-dock')
  expect(after.width).toBeGreaterThan(before.width + 150)
  await expect.poll(() => (work.preferences.layout as { quotePanel?: number }).quotePanel).toBeGreaterThan(before.width + 150)
  // The ticket panel's own width stays as it was.
  expect((work.preferences.layout as { panel?: number }).panel).toBe(900)
  const list = await rect(page, '.quotes-list-page .table-card')
  expect(list.width).toBeGreaterThan(300)
  // Dragged far left, the list still keeps its room.
  const far = (await handle.boundingBox())!
  await page.mouse.move(far.x + far.width / 2, far.y + 100)
  await page.mouse.down()
  await page.mouse.move(20, far.y + 100, { steps: 8 })
  await page.mouse.up()
  expect((await rect(page, '.quote-dock')).width).toBeLessThanOrEqual(1440 - 480 + 1)
  expect((await rect(page, '.quotes-list-page .table-card')).width).toBeGreaterThanOrEqual(400)
  // A reload keeps the width; the keyboard moves it; Home goes back to the default.
  const kept = (await rect(page, '.quote-dock')).width
  await expect.poll(() => (work.preferences.layout as { quotePanel?: number }).quotePanel).toBe(Math.round(kept))
  await page.reload()
  await expect(page.locator('.quote-dock .quote-document')).toHaveAttribute('data-quote-ready', 'true')
  expect(Math.abs((await rect(page, '.quote-dock')).width - kept)).toBeLessThan(2)
  await handle.focus()
  await page.keyboard.press('Home')
  await expect.poll(async () => Math.round((await rect(page, '.quote-dock')).width)).toBe(640)
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: the split at 1024 and the folded menu, in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1440, height: 900 })
    await docked(page, { quotePanel: 790, people: true })
    const check = async () => {
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').exclude('.quote-document').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
    }
    await page.locator('.quote-dock').getByRole('button', { name: 'More actions' }).click()
    await expect(page.getByRole('menu', { name: 'Quote actions' })).toBeVisible()
    await check()
    await page.keyboard.press('Escape')
    await page.setViewportSize({ width: 1024, height: 768 })
    await page.waitForTimeout(300)
    await check()
  })
}

// Temporary same-host worker timing; removed before handoff.
import { writeFileSync as writeCutATiming } from 'node:fs'
import { hostname as cutAHost } from 'node:os'
let cutAStarted = 0
test.beforeEach(() => { cutAStarted = performance.now() })
test.afterEach(async ({}, testInfo) => {
  writeCutATiming(testInfo.outputPath('p10-cutA-timing.md'), JSON.stringify({
    host: cutAHost(), title: testInfo.title, seconds: (performance.now() - cutAStarted) / 1000,
    status: testInfo.status, retry: testInfo.retry,
  }))
})
