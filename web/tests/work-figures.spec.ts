// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1358 (Tick's approved design r4b §3.1–§3.2, §5.1): the header work bar and the column header figures, on the
// AEON-1368 figures API. Risk: figures that lie (a total that is not the sum, a label that runs ahead of its figures, a
// failed or other scope's figures shown as this one's), popups that let the page show through or clip their text, and
// controls that move when figures land, while counting, on hover or when Hide closed changes.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'
import { viewFigures, wholeFigures } from './work-figures-fixture'
import type { ProjectFigures } from '../src/lib/api'
import { statusFill } from '../src/lib/statusMotion'

const bar = (page: Page) => page.locator('.head-work')
const barButton = (page: Page) => page.getByRole('button', { name: 'Work by hours', exact: true })
const tip = (page: Page) => page.locator('#figure-tip')
const header = (page: Page, label: string) => page.getByRole('grid', { name: 'Tickets' }).locator('thead th').filter({ has: page.locator('.th-sort, .th-label', { hasText: new RegExp(`^${label}$`, 'i') }) }).locator('.th-sort, .th-label')
const group = (page: Page, id: string) => page.getByRole('group', { name: 'Filter tickets by status', exact: true }).locator(`[data-group="${id}"] .group-count`)
const search = (page: Page) => page.getByRole('searchbox', { name: 'Search tickets in this project' })
const density = (page: Page, name: string) => page.getByRole('radio', { name: `${name} project header`, exact: true })

// The design's figures: the whole project, or "This view · 10 of 166 tickets" for any narrowed view. A gate holds the
// narrowed answers until the test releases them, so "Counting…" is a state the test controls rather than a race.
async function serveFigures(page: Page, options: { marksReleases?: boolean; figures?: ProjectFigures } = {}) {
  const requests: URL[] = []
  let gate: Promise<void> | null = null
  await page.route('**/api/projects/*/figures**', async route => {
    const url = new URL(route.request().url())
    requests.push(url)
    const narrowed = ['q', 'state', 'work_state', 'work_bucket', 'priority', 'assignee', 'tag'].some(name => url.searchParams.get(name))
    if (narrowed && gate) await gate
    await route.fulfill({ json: narrowed ? viewFigures() : options.figures ?? wholeFigures(options) })
  })
  return {
    requests,
    hold() { let release!: () => void; gate = new Promise<void>(resolve => { release = resolve }); return () => { gate = null; release() } },
  }
}

// The rows behind the figures' three models (claude-opus-5-5 · gpt-6.1-sol · grok-4.7), each measured, with registry
// names, so the Model cells show "Opus 5.5 · Sol 6.1 · Grok 4.7" and never the ids.
const MODELS = [
  { key: 'PHAROS-11', id: 'n-1', model: { label: 'Claude Opus 5.5', harness: 'claude', provider: 'anthropic', model: 'claude-opus-5-5', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5' } },
  { key: 'PHAROS-12', id: 'n-2', model: { label: 'Codex Sol', harness: 'codex', provider: 'openai', model: 'gpt-6.1-sol', display_name: 'GPT Sol', short_name: 'Sol', model_version: '6.1' } },
  { key: 'PHAROS-13', id: 'n-3', model: { label: 'Grok 4.7', harness: 'grok', provider: 'xai', model: 'grok-4.7', display_name: 'Grok', model_version: '4.7' } },
]
async function open(page: Page, options: { width?: number; theme?: string; marksReleases?: boolean; comfortable?: boolean; automaticColumns?: boolean; visible?: string[]; models?: boolean; figures?: ProjectFigures } = {}) {
  const width = options.width ?? 1440
  await page.setViewportSize({ width, height: width <= 600 ? 844 : 900 })
  const data = fixtures()
  data.preferences.theme = { choice: options.theme ?? 'light' }
  data.preferences['list:display'] = { headerGraph: false }
  // A saved column choice, so Estimate shows at every desktop width and filtering never changes the columns.
  if (!options.automaticColumns) data.preferences['list:p-pharos'] = { visible: options.visible ?? ['key', 'title', 'status', 'priority', 'assignee', 'estimate', 'updated'] }
  if (options.models) for (const { key, model } of MODELS) {
    data.nodes.find(node => node.key === key)!.planning = {
      route: null, models: [{ ...model, sessions: [{ id: `s-${key}`, effort: 'high', effort_level: 3, role: 'build', running: false, tokens: 1000 }] }],
      tokens: { spent: 1000, input: 800, output: 200, cached: 0, sessions: 1, running: 0, unreported: 0, estimated: null },
    }
  }
  const errors = watchErrors(page)
  await mockWork(page, data)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  const figures = await serveFigures(page, options)
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  if (options.comfortable && width > 600) await density(page, 'Comfortable').click()
  await expect(bar(page)).toHaveAttribute('aria-busy', 'false')
  await expect(bar(page).locator('.wk-total b')).toHaveText('~728h')
  return { figures, errors }
}
async function settled(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
const box = async (locator: Locator) => (await locator.boundingBox())!
const lineText = (page: Page) => bar(page).locator('.wk-item').evaluateAll(items => items.map(item => [...item.childNodes].filter(node => !(node instanceof HTMLElement && node.classList.contains('wk-probe'))).map(node => node.textContent).join('').replace(/\s+/g, ' ').trim()))
async function hoverOpen(page: Page, target: Locator) {
  await target.hover()
  await expect(tip(page)).toBeVisible()
  await settled(page)
}
// Nothing in an open popup is clipped: every line and footer fits its box.
async function expectNothingClipped(page: Page) {
  const clipped = await tip(page).evaluate(el => [...el.querySelectorAll<HTMLElement>('.wf-line > :first-child > span, .wf-v, .wf-foot, .wf-fline, .wf-ttl, .wf-tbl > *')]
    .filter(node => node.scrollWidth > node.clientWidth + 1).map(node => node.textContent))
  expect(clipped).toEqual([])
}
async function expectOpaque(locator: Locator) {
  const bg = await locator.evaluate(el => getComputedStyle(el).backgroundColor)
  const alpha = /rgba\([^)]*,\s*([\d.]+)\)/.exec(bg)?.[1]
  expect(alpha === undefined || Number(alpha) === 1, `${bg} is opaque`).toBe(true)
}

test('AEON-1358 the header work bar shows the whole project in hours, drawn to scale, with opaque counts on hover', async ({ page }) => {
  const { errors } = await open(page, { comfortable: true })
  await expect(bar(page).locator('.wk-scope .wk-long')).toHaveText('Whole project')
  await expect(bar(page).locator('.wk-total')).toHaveText('Total ~728h')
  expect(await lineText(page)).toEqual(['Completed619h', 'Done, not live31h', 'In progress35h', 'Open43h', '18 without estimate'])
  await expect(barButton(page)).toHaveAccessibleDescription('Whole project. Total about 728h. Completed 619h · Done, not live 31h · In progress 35h · Open 43h · 18 without estimate.')
  // To scale (±1 px): each segment is its share of the track; Open is the rest of the track.
  const track = await box(bar(page).locator('.wk-track'))
  const hours = { c: 618.6, d: 31.4, p: 34.6 }
  let left = 0
  for (const [id, h] of Object.entries(hours)) {
    const segment = await box(bar(page).locator(`.wk-seg[data-group="${id}"]`))
    expect(Math.abs(segment.width - track.width * h / 728), `${id} width`).toBeLessThanOrEqual(1)
    expect(Math.abs(segment.x - track.x - track.width * left / 728), `${id} left`).toBeLessThanOrEqual(1)
    left += h
  }
  // The fills are the status icons' bar form (AEON-1362's one map): Completed is Delivered's solid, Done, not live
  // Done's outline with a tint, In progress the 45° stripes; the line's swatches use the same fills.
  const fills = await bar(page).locator('.wk-seg').evaluateAll(segs => segs.map(seg => ({ cls: seg.className, before: getComputedStyle(seg, '::before').backgroundImage })))
  expect(fills.map(f => f.cls)).toEqual(['delivered', 'done', 'in_progress'].map(state => `wk-seg ${statusFill(state)}`))
  expect(await bar(page).locator('.wk-line .wk-sw').evaluateAll(swatches => swatches.map(sw => sw.className))).toEqual([...['delivered', 'done', 'in_progress'].map(state => `wk-sw ${statusFill(state)}`), 'wk-sw sw-open'])
  expect(fills[2].before).toContain('repeating-linear-gradient')

  // Hover after 380 ms: the counts, opaque, the label as the title, one fact per footer line.
  await hoverOpen(page, barButton(page))
  await expect(barButton(page)).toHaveAttribute('aria-describedby', /figure-tip/)
  await expect(tip(page).locator('.wf-ttl')).toHaveText('Whole project')
  const table = await tip(page).locator('.wf-tbl > *').evaluateAll(cells => cells.map(cell => cell.textContent!.trim()))
  expect(table).toEqual([
    '', 'Hours', 'Share', 'Tickets', 'Share',
    'Completed', '619h', '85%', '118', '85%',
    'Done, not live', '31h', '4%', '11', '8%',
    'In progress', '35h', '5%', '6', '4%',
    'Open', '43h', '6%', '4', '3%',
    'Total', '~728h', '', '139', '',
  ])
  await expect(tip(page).locator('.wf-fline')).toHaveText(['Hours are agent estimates · leaves only', '18 without estimate count as 0h', '27 closed not counted', 'Hide closed doesn’t change these figures.'])
  await expectOpaque(tip(page))
  await expectNothingClipped(page)
  await page.keyboard.press('Escape')
  await expect(tip(page)).toHaveCount(0)
  expect(errors).toEqual([])
})

test('AEON-1358 counting keeps label, total, bar and line together; the new scope lands in one frame; nothing moves', async ({ page }) => {
  const { figures } = await open(page, { comfortable: true })
  const guard = await controlStability(page, {
    open: group(page, 'open'), doing: group(page, 'in_progress'), done: group(page, 'done'), bar: barButton(page), total: bar(page).locator('.wk-total'),
    search: search(page), status: header(page, 'Status'), estimate: header(page, 'Estimate'),
  })
  const segments = async () => bar(page).locator('.wk-seg').evaluateAll(segs => segs.map(seg => (seg as HTMLElement).style.width))
  const before = await segments()
  const release = figures.hold()
  await guard.check(async () => {
    await search(page).fill('Hetzner')
    await expect(page).toHaveURL(/q=Hetzner/)
    // Counting: the previous label, total, bar and line, dimmed together.
    await expect(bar(page)).toHaveAttribute('aria-busy', 'true')
    await expect(bar(page).locator('.wk-scope .wk-long')).toHaveText('Whole project')
    await expect(bar(page).locator('.wk-total b')).toHaveText('~728h')
    expect(await segments()).toEqual(before)
    for (const part of ['.wk-head', '.wk-track', '.wk-line']) await expect(bar(page).locator(part)).toHaveCSS('opacity', '0.5')
  })
  // A column popup opened while counting says so, at the size the landed figures take.
  await guard.check(() => hoverOpen(page, header(page, 'Status')))
  await expect(tip(page).locator('.wf-line').first()).toHaveText('Counting…')
  await expect(tip(page).locator('.wf-line')).toHaveCount(4)
  const counting = await box(tip(page))
  await guard.check(async () => {
    release()
    await expect(bar(page)).toHaveAttribute('aria-busy', 'false')
    await expect(bar(page).locator('.wk-scope .wk-long')).toHaveText('This view · 10 of 166 tickets')
    await expect(bar(page).locator('.wk-total b')).toHaveText('~78h')
    await expect(tip(page).locator('.wf-line').first()).toContainText('Completed4')
  })
  const landed = await box(tip(page))
  expect(Math.abs(landed.width - counting.width)).toBeLessThanOrEqual(.5)
  expect(Math.abs(landed.height - counting.height)).toBeLessThanOrEqual(.5)
  expect(await lineText(page)).toEqual(['Completed41h', 'Done, not live6h', 'In progress15h', 'Open16h', '1 without estimate'])
  // Moving along the header row switches popups at once; Estimate's total is the header's total.
  await guard.check(() => header(page, 'Estimate').hover())
  await expect(tip(page).locator('.wf-line').first()).toHaveText('Total~78h')
  await guard.check(async () => { await page.mouse.move(5, 5); await expect(tip(page)).toHaveCount(0) })
  // Back to the whole project: cached, so it lands at once without a new request.
  const asked = figures.requests.length
  await guard.check(async () => {
    await search(page).fill('')
    await expect(bar(page).locator('.wk-scope .wk-long')).toHaveText('Whole project')
    await expect(bar(page)).toHaveAttribute('aria-busy', 'false')
  })
  expect(figures.requests.length).toBe(asked)
  guard.done()
})

test('AEON-1358 Hide closed only hides rows: the figures stay, footers say incl. hidden rows', async ({ page }) => {
  const { figures } = await open(page, { comfortable: true })
  const asked = figures.requests.length
  // Hide closed is on by default: the popups count the hidden rows too and say so.
  await page.keyboard.press('Shift')
  await header(page, 'Estimate').focus()
  await expect(tip(page)).toBeVisible()
  await expect(tip(page).locator('.wf-hint')).toHaveText('Sort by estimate\nShift-click adds a secondary sort')
  await expect(tip(page).locator('.wf-foot')).toHaveText('139 of 166 incl. hidden rows · closed left out')
  await page.keyboard.press('Escape')
  await expect(tip(page)).toHaveCount(0)
  await expect(header(page, 'Estimate')).toBeFocused()
  const guard = await controlStability(page, { bar: barButton(page), total: bar(page).locator('.wk-total'), open: group(page, 'open') })
  await guard.check(async () => {
    await page.getByRole('checkbox', { name: 'Hide closed', exact: true }).uncheck()
    await expect(page).toHaveURL(/closed=1/)
  })
  await expect(bar(page).locator('.wk-scope .wk-long')).toHaveText('Whole project')
  await expect(bar(page).locator('.wk-total b')).toHaveText('~728h')
  expect(figures.requests.length, 'Hide closed is not part of the scope').toBe(asked)
  await hoverOpen(page, header(page, 'Estimate'))
  await expect(tip(page).locator('.wf-foot')).toHaveText('139 of 166 shown · closed left out')
  await hoverOpen(page, barButton(page))
  await expect(tip(page).locator('.wf-fline')).toHaveCount(3)
  guard.done()
})

test('AEON-1358 column popups: the sort hint, then a fixed set of figure lines; keyboard focus opens them', async ({ page }) => {
  await open(page, { comfortable: true })
  const columns: [string, string[], string][] = [
    ['Key', ['Parents2', 'Leaves166', 'KeysAIT-4 – AIT-177'], '168 incl. hidden rows · all rows'],
    ['Status', ['Completed118Delivered 28 · Accepted 90', 'Done, not live11', 'In progress6In progress 5 · QA 1', 'Open4Backlog 1 · Open 3'], '166 incl. hidden rows · 27 closed · leaves only'],
    ['Priority', ['High64', 'Medium85', 'Low17', '–No priority0'], '166 incl. hidden rows · leaves only'],
    ['Updated', ['Range12 Sep – ', 'Median since update2.8d', 'Open, untouched 7d+0'], '168 incl. hidden rows · all rows'],
  ]
  await page.keyboard.press('Shift')
  for (const [label, lines, foot] of columns) {
    await header(page, label).focus()
    await expect(tip(page)).toBeVisible()
    await expect(tip(page).locator('.wf-hint')).toHaveText(`Sort by ${label.toLowerCase()}\nShift-click adds a secondary sort`)
    const shown = await tip(page).locator('.wf-line').evaluateAll(rows => rows.map(row => row.textContent!.replace(/\s+/g, ' ').trim()))
    expect(shown.map((text, index) => lines[index].endsWith(' ') ? text.slice(0, lines[index].length) : text), label).toEqual(lines)
    await expect(tip(page).locator('.wf-foot')).toHaveText(foot)
    await expectOpaque(tip(page))
    await expectNothingClipped(page)
  }
  // Title has the sort hint only.
  await header(page, 'Title').focus()
  await expect(tip(page).locator('.wf-hint')).toHaveText('Sort by title\nShift-click adds a secondary sort')
  await expect(tip(page).locator('.wf-line, .wf-rule')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(tip(page)).toHaveCount(0)
})

test('AEON-1358 a project that does not mark releases makes Done its last step', async ({ page }) => {
  await open(page, { comfortable: true, marksReleases: false })
  expect(await lineText(page)).toEqual(['Done650h', 'In progress35h', 'Open43h', '18 without estimate'])
  await expect(bar(page).locator('.wk-seg')).toHaveCount(2)
  await hoverOpen(page, barButton(page))
  await expect(tip(page).locator('.wf-fline').last()).toHaveText('This project doesn’t mark releases, so Done is its last step.')
  await expectNothingClipped(page)
})

test('AEON-1358 compact header: scope, bar and total on one row; the bar never changes width', async ({ page }) => {
  const { figures } = await open(page)
  await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
  await expect(bar(page).locator('.wk-line')).toBeHidden()
  await expect(bar(page).locator('.wk-short')).toHaveText('Whole project')
  const guard = await controlStability(page, { bar: barButton(page), total: bar(page).locator('.wk-total b'), scope: bar(page).locator('.wk-scope'), doing: group(page, 'in_progress') })
  const release = figures.hold()
  await guard.check(async () => { await search(page).fill('Hetzner'); await expect(bar(page)).toHaveAttribute('aria-busy', 'true') })
  await guard.check(async () => { release(); await expect(bar(page).locator('.wk-short')).toHaveText('This view'); await expect(bar(page).locator('.wk-total b')).toHaveText('~78h') })
  // The counts and the line live in the hover.
  await guard.check(() => hoverOpen(page, barButton(page)))
  await expect(tip(page).locator('.wf-ttl')).toHaveText('This view · 10 of 166 tickets')
  guard.done()
})

test('AEON-1358 a view that cannot be counted says so in place and never shows another scope\'s figures', async ({ page }) => {
  await open(page, { comfortable: true })
  await page.route('**/api/projects/*/figures**', route => new URL(route.request().url()).searchParams.get('q') ? route.fulfill({ status: 422, json: { error: 'figures exceed 50000 rows; narrow the view' } }) : route.fallback())
  const guard = await controlStability(page, { bar: barButton(page), total: bar(page).locator('.wk-total'), open: group(page, 'open') })
  const height = (await box(bar(page))).height
  await guard.check(async () => {
    await search(page).fill('Hetzner')
    await expect(bar(page).getByRole('status')).toHaveText('Too many tickets to count here; narrow the view. Retry')
  })
  await expect(bar(page).locator('.wk-total b')).toHaveText('–')
  await expect(bar(page).locator('.wk-seg')).toHaveCount(0)
  expect(Math.abs((await box(bar(page))).height - height)).toBeLessThanOrEqual(.5)
  guard.done()
})

test.describe('phone', () => {
  test.use({ hasTouch: true, isMobile: true })
  test('AEON-1358 on a phone the bar takes the full width and a tap pins the counts', async ({ page }) => {
    await open(page, { width: 390 })
    // Phones have no header row, so no column popups.
    await expect(page.getByRole('grid', { name: 'Tickets' }).locator('thead')).toBeHidden()
    const frame = await box(page.locator('.head-flex'))
    const work = await box(bar(page))
    expect(Math.abs(work.width - frame.width)).toBeLessThanOrEqual(1)
    await expect(bar(page).locator('.wk-line')).toBeVisible()
    const reach = await box(barButton(page))
    expect(reach.height).toBeGreaterThanOrEqual(44)
    await barButton(page).tap()
    await expect(tip(page)).toBeVisible()
    await expect(tip(page).locator('.wf-ttl')).toHaveText('Whole project')
    await expectNothingClipped(page)
    const shot = await box(tip(page))
    expect(shot.x).toBeGreaterThanOrEqual(0)
    expect(shot.x + shot.width).toBeLessThanOrEqual(390)
    await page.locator('h1').tap()
    await expect(tip(page)).toHaveCount(0)
  })
})

for (const theme of ['light', 'dark']) {
  test(`AEON-1358 every tooltip is opaque and the header's Closed group reads at 4.5:1 (${theme})`, async ({ page }) => {
    await open(page, { comfortable: true, theme })
    const tipBg = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--tip-bg').trim())
    expect(tipBg).toBe(theme === 'light' ? '#203c3d' : '#061214')
    await group(page, 'open').hover()
    const tooltip = page.locator('.tooltip')
    await expect(tooltip).toBeVisible()
    await expectOpaque(tooltip)
    // The Closed group's text against the page canvas, with every ancestor's opacity applied.
    const contrast = await page.locator('[data-group="closed"]').evaluate(element => {
      const parse = (c: string) => (c.match(/[\d.]+/g) ?? []).map(Number)
      const lum = ([r, g, b]: number[]) => { const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b) }
      const bg = parse(getComputedStyle(document.body).backgroundColor)
      let opacity = 1
      for (let node: HTMLElement | null = element as HTMLElement; node; node = node.parentElement) opacity *= Number(getComputedStyle(node).opacity)
      return [...element.querySelectorAll<HTMLElement>('.group-count span, .group-count b')].map(text => {
        const fg = parse(getComputedStyle(text).color), alpha = opacity * (fg[3] ?? 1)
        const mixed = fg.slice(0, 3).map((v, i) => v * alpha + bg[i] * (1 - alpha))
        const [a, b] = [lum(mixed), lum(bg)].sort((x, y) => y - x)
        return (a + 0.05) / (b + 0.05)
      })
    })
    expect(contrast.length).toBeGreaterThan(0)
    for (const ratio of contrast) expect(ratio).toBeGreaterThanOrEqual(4.5)
  })
}

// Evidence for the visual check against the approved design: 390 / 960 / 1440, light and dark, comfortable and compact,
// with the counts and a column popup open on desktop.
for (const width of [390, 960, 1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-1358 evidence ${width} ${theme}`, async ({ page }, info) => {
    await open(page, { width, theme, comfortable: true, automaticColumns: true })
    await settled(page)
    await page.screenshot({ path: info.outputPath(`work-bar-${width}-${theme}-comfortable.png`) })
    if (width > 600) {
      await hoverOpen(page, barButton(page))
      await page.screenshot({ path: info.outputPath(`work-bar-${width}-${theme}-counts.png`) })
      await hoverOpen(page, header(page, 'Status'))
      await page.screenshot({ path: info.outputPath(`work-bar-${width}-${theme}-status-popup.png`) })
      await page.mouse.move(0, 0)
      await density(page, 'Compact').click()
      await expect(bar(page).locator('.wk-short')).toBeVisible()
      await settled(page)
      await page.screenshot({ path: info.outputPath(`work-bar-${width}-${theme}-compact.png`) })
    }
  })
}

// Tick's fix round 6 (2026-10-15): the Model popup names each model exactly as its Model cells do, and every top-N
// popup keeps its empty lines reserved but blank, with Others only above 0. Phones have no header row and so no column
// popups; there the cases prove that no popup can open. The Model and Cost unit shots are for Tick's confirm.
const lineTexts = (page: Page) => tip(page).locator('.wf-line').evaluateAll(rows => rows.map(row => ({ text: row.textContent!.replace(/\s+/g, ' ').trim(), height: row.getBoundingClientRect().height })))
async function closeTip(page: Page) { await page.mouse.move(5, 5); await expect(tip(page)).toHaveCount(0) }
// One value or none in each top-N column and nothing in the tail: Model, Epic, Release, Suggested, Tags and Cost unit.
function sparseFigures(): ProjectFigures {
  const whole = wholeFigures(), c = whole.columns
  const one = (value: string, count: number) => [{ value, count, hours: 0 }]
  return { ...whole, columns: { ...c,
    model: { ...c.model, values: one('gpt-6.1-sol', 41), others: 0 }, epic: { ...c.epic, values: one('AIT-12', 40), others: 0 },
    release: { ...c.release, values: [], others: 0 }, suggested_release: { ...c.suggested_release, values: [], others: 0 },
    tag: { ...c.tag, values: one('bug', 19), others: 0 }, cost_unit: { ...c.cost_unit, others: 0 },
  } }
}
const TOP_N: [string, number][] = [['Model', 3], ['Epic', 2], ['Release', 3], ['Suggested release', 3], ['Tags', 2], ['Cost unit', 3]]

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-1358 the Model popup names each model as its Model cells do (${width} ${theme})`, async ({ page }, info) => {
    const { errors } = await open(page, { width, theme, comfortable: true, models: true, visible: ['key', 'title', 'status', 'model', 'cost'] })
    if (width <= 720) {
      await expect(page.getByRole('grid', { name: 'Tickets' }).locator('thead')).toBeHidden()
      await expect(tip(page)).toHaveCount(0)
      return
    }
    const cells: string[] = []
    for (const { id } of MODELS) cells.push((await page.locator(`#row-${id} [data-column-id="model"] .model-name`).textContent())!.trim())
    expect(cells, 'the cells show registry names').toEqual(['Opus 5.5', 'Sol 6.1', 'Grok 4.7'])
    await hoverOpen(page, header(page, 'Model'))
    const names = await tip(page).locator('.wf-line > :first-child').evaluateAll(rows => rows.map(row => row.textContent!.trim()))
    expect(names.slice(0, 3), 'each popup name equals its cell text').toEqual(cells)
    for (const name of names) expect(name).not.toMatch(/claude-opus|gpt-6|grok-4\.7/)
    expect((await lineTexts(page))[3].text, 'Others shows while the tail has rows').toBe('Others3')
    await expectNothingClipped(page)
    await page.screenshot({ path: info.outputPath(`model-popup-${width}-${theme}.png`) })
    await closeTip(page)
    // The whole project has one cost unit: the other three lines are reserved and blank.
    await hoverOpen(page, header(page, 'Cost unit'))
    expect((await lineTexts(page)).map(line => line.text)).toEqual(['AIT core412h · $1.9k', '', '', ''])
    await page.screenshot({ path: info.outputPath(`cost-unit-popup-${width}-${theme}.png`) })
    expect(errors).toEqual([])
  })

  test(`AEON-1358 top-N popups keep empty lines reserved but blank, with Others only above 0 (${width} ${theme})`, async ({ page }) => {
    await open(page, { width, theme, comfortable: true, models: true, figures: sparseFigures(), visible: ['key', 'title', 'model', 'epic', 'release', 'suggested', 'tags', 'cost'] })
    if (width <= 720) {
      await expect(page.getByRole('grid', { name: 'Tickets' }).locator('thead')).toBeHidden()
      await expect(tip(page)).toHaveCount(0)
      return
    }
    for (const [label, blanks] of TOP_N) {
      await hoverOpen(page, header(page, label))
      const lines = await lineTexts(page)
      expect(lines, `${label} keeps its four lines`).toHaveLength(4)
      // Every line keeps its 20 px slot, so the popup never changes size with the figures.
      for (const line of lines) expect(Math.abs(line.height - 20), `${label} line height`).toBeLessThanOrEqual(.5)
      expect(lines.filter(line => line.text === '').length, `${label} blank lines`).toBe(blanks)
      for (const line of lines) {
        expect(line.text, label).not.toContain('—')
        expect(line.text, label).not.toMatch(/^Others/)
      }
      await closeTip(page)
    }
  })
}
