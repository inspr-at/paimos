// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1470: the ticket list at 1024 px. Risk: Title squeezed under its 240 px minimum by many columns or wide saved
// widths; columns that leave in the wrong order, take the sorted column with them or flap at a threshold; a column that
// leaves without a word in Display; a group row's priority mix that ellipsizes or miscounts its compact form; a stale tag
// that runs into the next cell; anything that moves on hover, a live change, a tip or a fold; mixed languages in the fold
// label. Phone rows stay as they were. Evidence: aeon1470-<width>-<theme>-<state>.png and aeon1470-checks.json (.md).
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { COLUMN_BY_ID, GIVE_WAY, RETURN_ROOM, type ColumnId } from '../src/lib/columns'
import { SORT_LABELS } from '../src/lib/work'
import { controlStability } from './control-stability'
import { fixtures, liveAgent, mockWork, watchErrors, type Fixtures } from './work-fixtures'

test.beforeEach(async ({ page }) => {
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  // A connected live stream the test can speak through (a status change made elsewhere).
  await page.addInitScript(() => {
    const streams: EventTarget[] = []
    class Aeon1470Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      readyState = 1
      constructor() {
        super()
        streams.push(this)
        queueMicrotask(() => {
          if (this.readyState !== 1) return
          this.onopen?.(new Event('open'))
          this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 40, resumed: false }), lastEventId: '40' }))
        })
      }
      close() { this.readyState = 2 }
    }
    Object.assign(window, {
      EventSource: Aeon1470Stream,
      aeon1470Emit: (type: string, data: unknown, id: number) => { for (const stream of streams) if ((stream as Aeon1470Stream).readyState === 1) stream.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data), lastEventId: String(id) })) },
    })
  })
})

const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const rows = (page: Page) => grid(page).locator('tr.ticket-row:not(.ghost)')
const groupRow = (page: Page, label: string) => grid(page).locator('tr.group-row').filter({ has: page.locator('.group-label', { hasText: new RegExp(`^${label}$`) }) })
const groupCell = (page: Page, label: string, column: string) => groupRow(page, label).locator(`td[data-group-column="${column}"]`)
const row = (page: Page, key: string) => rows(page).filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const display = (page: Page) => page.getByRole('dialog', { name: 'Display options' })
// A Display column row, found from the menu itself: a `has` locator rooted at the dialog never matches inside the row.
const columnRow = (menu: Locator, id: string) => menu.locator(`li.row:has(input[data-column-row="${id}"])`)
const SET_1388: ColumnId[] = ['status', 'priority', 'assignee', 'estimate', 'updated']
const ALL: ColumnId[] = [...COLUMN_BY_ID.keys()].filter(id => id !== 'key' && id !== 'title')
const WIDE_SAVED = { status: 260, priority: 200, assignee: 320, estimate: 180, updated: 200, key: 300 }
const NBSP = ' '
const FULL_MIX = `3${NBSP}high${NBSP}·${NBSP}1${NBSP}med${NBSP}·${NBSP}2${NBSP}low`
const COMPACT_MIX = `3${NBSP}high${NBSP}+3`

// The AEON-1388 fixture plus two low in-progress tickets: In progress holds 3 high, 1 medium and 2 low ("3 high +3"),
// two live sessions and two stale tickets under the long-titled epic.
function data(prefs: Record<string, unknown>): Fixtures {
  const d = fixtures()
  const node = (id: string) => d.nodes.find(n => n.id === id)!
  Object.assign(node('n-1').fields, { estimate_hours: 4 })
  Object.assign(node('n-2').fields, { estimate_hours: 10 })
  Object.assign(node('n-3').fields, { estimate_hours: 2.5 })
  Object.assign(node('n-4').fields, { estimate_hours: 1 })
  const extra = (id: string, key: string, title: string, fields: Record<string, unknown>, more: Partial<Fixtures['nodes'][number]> = {}) =>
    d.nodes.push({ id, key, kind_slug: 'ticket', title, state: 'in_progress', project: 'p-pharos', parent_id: 'p-pharos', body: '', fields, created_at: '2026-09-01T10:00:00Z', updated_at: '2026-09-23T09:00:00Z', ...more })
  extra('n-7', 'PHAROS-17', 'Rotate the bastion keys', { priority: 'high', estimate_hours: 2 }, { queue_stale: true, parent_id: 'n-epic' })
  extra('n-8', 'PHAROS-18', 'Prune unused images', { priority: 'medium' }, { queue_stale: true, parent_id: 'n-epic' })
  extra('n-9', 'PHAROS-19', 'Wire the probe dashboard', { priority: 'high', estimate_hours: 1.5 })
  extra('n-10', 'PHAROS-20', 'Document the restore drill', { priority: 'low' })
  extra('n-11', 'PHAROS-21', 'Tidy the provisioning logs', { priority: 'low' })
  d.live.push(
    liveAgent({ project_id: 'p-pharos', session_id: 's-1', principal_id: '33333333-3333-4333-8333-333333333333', name: 'Builder', ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', project_id: 'p-pharos' } }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-2', principal_id: '44444444-4444-4444-8444-444444444444', name: 'Reviewer', harness: 'codex', ticket: { id: 'n-9', key: 'PHAROS-19', title: 'Wire the probe dashboard', project_id: 'p-pharos' } }),
  )
  d.preferences['list:display'] = { headerGraph: false }
  d.preferences['list:p-pharos'] = prefs
  return d
}

async function open(page: Page, options: { width?: number; theme?: 'light' | 'dark'; query?: string; prefs?: Record<string, unknown>; edit?: (d: Fixtures) => void } = {}) {
  const width = options.width ?? 1024
  await page.setViewportSize({ width, height: width <= 600 ? 844 : 900 })
  const d = data(options.prefs ?? { visible: SET_1388 })
  d.preferences.theme = { choice: options.theme ?? 'light' }
  options.edit?.(d)
  const errors = watchErrors(page)
  await mockWork(page, d)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  await page.goto(`/p/PHAROS${options.query ?? '?group=status'}`)
  await expect(rows(page).first()).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
  // Group figures have landed: Priority never steps aside, so some group's priority mix shows.
  if (width > 720) await expect(grid(page).locator('tr.group-row td[data-group-column="priority"] .gf-text').filter({ hasText: /\S/ }).first()).toBeVisible()
  await settled(page)
  return { errors, data: d }
}
async function settled(page: Page) { await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))) }
type Box = { x: number; y: number; width: number; height: number }
const boxOf = (locator: Locator) => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
const tableWidth = (page: Page) => page.locator('.table-card').evaluate(el => el.clientWidth)
const shownIds = (page: Page) => grid(page).locator('thead th').evaluateAll(cells => cells.map(cell => cell.getAttribute('data-column-id') as ColumnId))
const headWidths = (page: Page) => grid(page).locator('thead th').evaluateAll(cells => Object.fromEntries(cells.map(cell => [cell.getAttribute('data-column-id'), cell.getBoundingClientRect().width])))
// Sets the viewport so the table is exactly `target` px wide (the page gutter grows with the window). The resize
// observer and the render it triggers land before the second frame.
async function tableAt(page: Page, target: number) {
  let viewport = page.viewportSize()!.width + target - await tableWidth(page)
  for (let step = 0; step < 4; step++) {
    await page.setViewportSize({ width: viewport, height: 900 })
    await settled(page)
    const actual = await tableWidth(page)
    if (actual === target) break
    viewport += target - actual
  }
  return { viewport, actual: await tableWidth(page) }
}

// Every assert lands in a checks file with its width, theme and locale (Tick's evidence, AEON-1470 §8).
interface Check { assert: string; state: string; width: number; tableWidth?: number; theme: 'light' | 'dark'; locale: 'en'; value: unknown; ok: boolean }
function checks(testInfo: TestInfo) {
  const list: Check[] = []
  return {
    add(entry: Omit<Check, 'locale' | 'ok'> & { ok?: boolean }) { list.push({ locale: 'en', ok: true, ...entry }) },
    save() {
      const text = JSON.stringify({ test: testInfo.title, checks: list }, null, 1)
      // .md as well: the remote runner copies back only images and Markdown.
      writeFileSync(testInfo.outputPath('aeon1470-checks.json'), text)
      writeFileSync(testInfo.outputPath('aeon1470-checks.md'), text)
    },
  }
}
// Columns this width leaves out must be the first ones of the give-way order (a sorted one excepted), never others.
function expectGiveWayOrder(chosen: ColumnId[], shown: ColumnId[], keep: ColumnId[] = []) {
  const order = GIVE_WAY.filter(id => chosen.includes(id) && !keep.includes(id))
  const dropped = order.filter(id => !shown.includes(id))
  expect(dropped, `dropped ${dropped.join()} must lead ${order.join()}`).toEqual(order.slice(0, dropped.length))
  for (const id of ['key', 'title', 'status', 'priority', ...keep]) expect(shown, `keeps ${id}`).toContain(id)
  return dropped
}

test('AEON-1470 Title keeps at least 240 px at table widths 760–1440 for the 1388 set, every column and wide saved widths', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  for (const [set, prefs, chosen] of [['1388 set', { visible: SET_1388 }, SET_1388], ['all columns', { visible: ALL }, ALL], ['wide saved widths', { visible: SET_1388, widths: WIDE_SAVED }, SET_1388]] as const) {
    const { errors } = await open(page, { prefs })
    for (const target of [760, 900, 1000, 1280, 1440]) {
      const { viewport, actual } = await tableAt(page, target)
      expect(actual).toBe(target)
      const widths = await headWidths(page), shown = await shownIds(page)
      expect(widths.title, `${set} at ${target}: title`).toBeGreaterThanOrEqual(240)
      log.add({ assert: 'title ≥ 240 px', state: set, width: viewport, tableWidth: actual, theme: 'light', value: Math.round(widths.title * 10) / 10 })
      // No column below its minimum, and nothing scrolls sideways.
      for (const [id, width] of Object.entries(widths)) expect(width, `${set} at ${target}: ${id}`).toBeGreaterThanOrEqual(COLUMN_BY_ID.get(id as ColumnId)!.min - .5)
      log.add({ assert: 'no column below its min', state: set, width: viewport, tableWidth: actual, theme: 'light', value: Object.fromEntries(Object.entries(widths).map(([id, width]) => [id, Math.round(width)])) })
      const sideways = await grid(page).evaluate(el => el.getBoundingClientRect().width - el.parentElement!.clientWidth)
      expect(sideways, `${set} at ${target}: table wider than its card`).toBeLessThanOrEqual(1)
      const dropped = expectGiveWayOrder([...chosen], shown)
      log.add({ assert: 'drop order', state: set, width: viewport, tableWidth: actual, theme: 'light', value: dropped })
      // S2: at 1440 nothing leaves while Title fits.
      if (target === 1440 && set !== 'all columns') { expect(dropped).toEqual([]); log.add({ assert: 'nothing drops while Title fits', state: set, width: viewport, tableWidth: actual, theme: 'light', value: dropped }) }
    }
    expect(errors).toEqual([])
  }
  log.save()
})

test('AEON-1470 columns leave in the give-way order; sorted by Updated, Updated stays and the next one leaves instead', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page, { prefs: { visible: ALL } })
  const plain = await shownIds(page)
  const dropped = expectGiveWayOrder(ALL, plain)
  expect(dropped[0]).toBe('updated')
  log.add({ assert: 'drop order', state: 'all columns, default sort', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: dropped })
  await open(page, { prefs: { visible: ALL }, query: '?group=status&sort=-updated_at' })
  await expect(grid(page).locator('thead th[data-column-id="updated"]')).toHaveAttribute('aria-sort', 'descending')
  const sorted = await shownIds(page)
  const droppedSorted = expectGiveWayOrder(ALL, sorted, ['updated'])
  expect(droppedSorted.length).toBe(dropped.length)
  log.add({ assert: 'sorted column never drops', state: 'all columns, sorted by updated', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: { shown: sorted.includes('updated'), dropped: droppedSorted } })
  log.save()
})

test('AEON-1470 a column that left returns only with 16 px to spare: ±8 px around a threshold keeps the set', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page)
  // Narrow the table a pixel at a time until Updated leaves. It starts out shown: the scan finds where it leaves.
  await tableAt(page, 920)
  expect(await shownIds(page)).toContain('updated')
  let width = 920
  while ((await shownIds(page)).includes('updated')) { width--; await tableAt(page, width); expect(width).toBeGreaterThan(721) }
  const set = await shownIds(page)
  for (const step of [8, -8]) {
    await tableAt(page, width + step)
    expect(await shownIds(page), `left out at ${width + step}`).toEqual(set)
    log.add({ assert: 'hysteresis keeps the set', state: `Updated left at ${width}; ${step > 0 ? '+' : ''}${step} px`, width: page.viewportSize()!.width, tableWidth: width + step, theme: 'light', value: set })
  }
  // It returns with RETURN_ROOM to spare above the width it needs (width + 1), then holds ±8 px around that threshold.
  const back = width + 1 + RETURN_ROOM
  await tableAt(page, back - 1)
  expect(await shownIds(page)).not.toContain('updated')
  await tableAt(page, back)
  const returned = await shownIds(page)
  expect(returned).toContain('updated')
  for (const step of [-8, 8]) {
    await tableAt(page, back + step)
    expect(await shownIds(page), `back at ${back + step}`).toEqual(returned)
    log.add({ assert: 'hysteresis keeps the set', state: `Updated back at ${back}; ${step > 0 ? '+' : ''}${step} px`, width: page.viewportSize()!.width, tableWidth: back + step, theme: 'light', value: returned })
  }
  log.save()
})

test('AEON-1470 group row priority mix: compact "3 high +3" when the full mix does not fit, the full mix in its tip, never ellipsized', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  for (const theme of ['light', 'dark'] as const) {
    const { errors } = await open(page, { theme })
    const cell = groupCell(page, 'In progress', 'priority'), text = cell.locator('.gf-text')
    await expect(text).toHaveText(COMPACT_MIX)
    expect(await text.evaluate(el => el.textContent)).toBe(COMPACT_MIX)
    const fit = await text.evaluate(el => ({ scroll: el.scrollWidth, client: el.clientWidth }))
    expect(fit.scroll).toBeLessThanOrEqual(fit.client)
    log.add({ assert: 'priority cell scrollWidth ≤ clientWidth', state: 'In progress, 3 buckets', width: 1024, tableWidth: await tableWidth(page), theme, value: fit })
    log.add({ assert: 'compact text', state: 'In progress, 3 buckets', width: 1024, theme, value: await text.evaluate(el => el.textContent) })
    // Every group cell either fits whole or shows the compact form.
    for (const label of ['New', 'Backlog', 'QA']) {
      const other = groupCell(page, label, 'priority').locator('.gf-text')
      expect(await other.evaluate(el => el.scrollWidth <= el.clientWidth), label).toBe(true)
    }
    if (theme === 'light') {
      await page.mouse.move(5, 5)
      await cell.locator('.gf-cell').hover()
      const tip = page.locator('#figure-tip')
      await expect(tip).toBeVisible()
      await settled(page)
      await expect(tip.locator('.wf-sum')).toHaveText(FULL_MIX)
      expect(await tip.evaluate(el => el.textContent)).toContain(FULL_MIX)
      log.add({ assert: 'tooltip contains the full mix', state: 'In progress tip open', width: 1024, theme, value: await tip.locator('.wf-sum').evaluate(el => el.textContent) })
      // Opaque, and never over its own cell.
      const alpha = await tip.evaluate(el => {
        const color = getComputedStyle(el).backgroundColor
        const slash = /\/\s*([\d.]+%?)\s*\)$/.exec(color), rgba = /^rgba\(([^)]+)\)$/.exec(color)
        if (slash) return slash[1].endsWith('%') ? parseFloat(slash[1]) / 100 : Number(slash[1])
        if (rgba) return Number(rgba[1].split(',')[3])
        return color === 'transparent' ? 0 : 1
      })
      expect(alpha).toBe(1)
      const a = await boxOf(tip), b = await boxOf(cell)
      const overlap = Math.max(0, Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)) * Math.max(0, Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y))
      expect(overlap).toBe(0)
      log.add({ assert: 'tooltip opaque and clear of its cell', state: 'In progress tip open', width: 1024, theme, value: { alpha, overlap } })
      await page.screenshot({ path: testInfo.outputPath('aeon1470-1024-light-s3-tip.png'), fullPage: false })
      await page.mouse.move(5, 5)
    } else {
      await page.mouse.move(5, 5)
      await page.screenshot({ path: testInfo.outputPath('aeon1470-1024-dark-s3.png'), fullPage: false })
    }
    expect(errors).toEqual([])
  }
  // A priority column the person widened holds the full mix.
  await open(page, { width: 1440, prefs: { visible: SET_1388, widths: { priority: 200 } } })
  await expect(groupCell(page, 'In progress', 'priority').locator('.gf-text')).toHaveText(FULL_MIX)
  log.add({ assert: 'full form when it fits', state: 'priority saved at 200 px', width: 1440, theme: 'light', value: await groupCell(page, 'In progress', 'priority').locator('.gf-text').evaluate(el => el.textContent) })
  log.save()
})

// AEON-1470 (fix 7). Risk: a large group's compact mix ("25,000 high +25,000") outgrowing the priority cell and
// ellipsizing, or the room it needs following the counts, so live figures would move the columns.
test('AEON-1470 a group of 50,000 tickets keeps its compact priority mix whole, in a column as wide as with three', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page)
  const small = await headWidths(page)
  const inProgress = (query: URLSearchParams) => query.getAll('work_state').flatMap(value => value.split(',')).includes('in_progress')
  const { errors } = await open(page, { edit: d => { d.figuresHook = (figures, query) => {
    if (inProgress(query)) figures.columns.priority = { ...figures.columns.priority, values: [{ value: 'high', count: 25_000, hours: 0 }, { value: 'low', count: 25_000, hours: 0 }] }
  } } })
  const cell = groupCell(page, 'In progress', 'priority'), text = cell.locator('.gf-text')
  await expect(text).toHaveText(`25K${NBSP}high${NBSP}+25K`)
  const fit = await text.evaluate(el => ({ scroll: el.scrollWidth, client: el.clientWidth }))
  expect(fit.scroll).toBeLessThanOrEqual(fit.client)
  log.add({ assert: 'priority cell scrollWidth ≤ clientWidth', state: 'In progress, 25,000 high and 25,000 low', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: fit })
  // The reserve is the widest count the figures allow, never the counts on screen: every column keeps its width.
  const large = await headWidths(page)
  expect(Object.keys(large)).toEqual(Object.keys(small))
  for (const id of Object.keys(small)) expect(Math.abs(large[id] - small[id]), id).toBeLessThanOrEqual(.5)
  log.add({ assert: 'column widths independent of the counts', state: '3 vs 50,000 tickets', width: 1024, theme: 'light', value: { priority: [small.priority, large.priority] } })
  // The tip names every count in full.
  await page.mouse.move(5, 5)
  await cell.locator('.gf-cell').hover()
  const tip = page.locator('#figure-tip')
  await expect(tip.locator('.wf-sum')).toHaveText(`25,000${NBSP}high${NBSP}·${NBSP}25,000${NBSP}low`)
  await page.mouse.move(5, 5)
  expect(errors).toEqual([])
  log.save()
})

test('AEON-1470 the stale tag keeps 8 px to the next cell and never truncates; the group name ellipsizes first', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page)
  const head = groupRow(page, 'In progress')
  const stale = head.locator('.group-stale')
  await expect(stale).toHaveText('· 2 stale')
  const gap = async (tag: Locator, next: Locator) => { const a = await boxOf(tag), b = await boxOf(next); return b.x - (a.x + a.width) }
  const groupGap = await gap(stale, head.locator('td.group-fig').first().locator('.gf-cell'))
  expect(groupGap).toBeGreaterThanOrEqual(8)
  expect(await stale.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  log.add({ assert: 'stale tag right edge + 8 ≤ next cell content', state: 'In progress group row', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: Math.round(groupGap * 10) / 10 })
  // The row's own stale hint stays inside the status cell, clear of the priority icon.
  const hint = row(page, 'PHAROS-17').locator('.stale-hint.shown')
  await expect(hint).toBeVisible()
  const rowGap = await gap(hint, row(page, 'PHAROS-17').locator('td.c-prio .cell'))
  expect(rowGap).toBeGreaterThanOrEqual(8)
  log.add({ assert: 'row stale hint right edge + 8 ≤ priority content', state: 'PHAROS-17 row', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: Math.round(rowGap * 10) / 10 })
  // A status column too narrow for its label shows the icon alone and names the status in its tip; a wider one keeps the label.
  const status = row(page, 'PHAROS-17').locator('td.c-status'), narrow = await status.evaluate(el => el.classList.contains('narrow'))
  if (narrow) await expect(status.locator('.status-btn')).toHaveAttribute('data-tip', 'In progress')
  else await expect(status.locator('.status-btn > span:not(.status-icon)').first()).toBeVisible()
  log.add({ assert: 'status label readable (label or tip)', state: 'PHAROS-17 row', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: { narrow, width: Math.round((await boxOf(status)).width) } })
  await page.mouse.move(5, 5)
  const crop = await boxOf(head)
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1024-light-s4-stale.png'), clip: { x: 0, y: Math.max(0, crop.y - 120), width: 1024, height: 300 } })
  // Grouped by epic on a 760 px table: the long epic title gives way, the tag stays whole and clear.
  await open(page, { query: '?group=epic' })
  await tableAt(page, 760)
  const epic = grid(page).locator('tr.group-row').filter({ has: page.locator('.group-epic .key', { hasText: 'PHAROS-10' }) })
  const epicStale = epic.locator('.group-stale')
  await expect(epicStale).toHaveText('· 2 stale')
  expect(await epic.locator('.group-epic .group-label').evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true)
  expect(await epicStale.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  const epicGap = await gap(epicStale, epic.locator('td.group-fig').first().locator('.gf-cell'))
  expect(epicGap).toBeGreaterThanOrEqual(8)
  log.add({ assert: 'group name ellipsizes before the stale tag', state: 'epic group, long title', width: page.viewportSize()!.width, tableWidth: 760, theme: 'light', value: { nameEllipsized: true, tagWhole: true, gap: Math.round(epicGap * 10) / 10 } })
  log.save()
})

test('AEON-1470 nothing moves on hover, a live status change, an open tip or a fold', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  const { data: d } = await open(page)
  const columns = await shownIds(page)
  const watched: Record<string, Locator> = {}
  for (const id of columns) watched[`head ${id}`] = grid(page).locator(`thead th[data-column-id="${id}"]`)
  for (let i = 0; i < 3; i++) watched[`row ${i + 1}`] = rows(page).nth(i)
  async function sample() { const out: Record<string, Box> = {}; for (const [name, locator] of Object.entries(watched)) out[name] = await boxOf(locator); return out }
  await page.mouse.move(5, 5)
  const before = await sample()
  async function expectStill(state: string) {
    await settled(page)
    const after = await sample()
    let movement = 0
    for (const name of Object.keys(before)) for (const side of ['x', 'y', 'width', 'height'] as const) movement = Math.max(movement, Math.abs(after[name][side] - before[name][side]))
    expect(movement, state).toBeLessThanOrEqual(.5)
    expect(await shownIds(page), state).toEqual(columns)
    log.add({ assert: 'movementPx 0 (header cells, first three rows)', state, width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: Math.round(movement * 100) / 100 })
  }
  await rows(page).nth(1).hover()
  await grid(page).locator('thead th[data-column-id="status"] .th-sort').hover()
  await expectStill('hover')
  // Someone else moves PHAROS-21 to QA: the stream names the change, the list reads the ticket again.
  const changed = d.nodes.find(n => n.id === 'n-11')!
  changed.state = 'qa'
  changed.updated_at = '2026-09-23T11:59:00Z'
  await page.evaluate(([at]) => (window as unknown as { aeon1470Emit: (type: string, data: unknown, id: number) => void }).aeon1470Emit('node.updated', { id: 41, type: 'node.updated', actor_principal_id: '99999999-9999-4999-8999-999999999999', node_changes: [{ id: 'n-11', project_id: 'p-pharos', change: 'updated', fields: ['state'], revision: at }] }, 41), [changed.updated_at])
  await expect(row(page, 'PHAROS-21').locator('.c-status')).toContainText('QA')
  await page.mouse.move(5, 5)
  await expectStill('live status change')
  await groupCell(page, 'In progress', 'priority').locator('.gf-cell').hover()
  await expect(page.locator('#figure-tip')).toBeVisible()
  await expectStill('tooltip open')
  await page.mouse.move(5, 5)
  await expect(page.locator('#figure-tip')).toHaveCount(0)
  const qa = groupRow(page, 'QA').locator('.group-toggle')
  const count = await rows(page).count()
  await qa.click()
  await expect.poll(() => rows(page).count()).toBeLessThan(count)
  await page.mouse.move(5, 5)
  await expectStill('group fold')
  await qa.click()
  await expect.poll(() => rows(page).count()).toBe(count)
  await page.mouse.move(5, 5)
  await expectStill('group unfold')
  log.save()
})

test('AEON-1470 the fold label is English like the rest of the list', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page)
  const head = grid(page).locator('thead th.key-head'), fold = head.locator('.key-fold')
  await head.hover()
  await expect(fold).toHaveAccessibleName('Collapse all groups')
  log.add({ assert: 'fold label', state: 'groups open', width: 1024, theme: 'light', value: await fold.getAttribute('aria-label') })
  await fold.click()
  await expect(rows(page)).toHaveCount(0)
  await expect(fold).toHaveAccessibleName('Expand all groups')
  log.add({ assert: 'fold label', state: 'groups folded', width: 1024, theme: 'light', value: await fold.getAttribute('aria-label') })
  await fold.click()
  await expect(rows(page).first()).toBeVisible()
  log.save()
})

test('AEON-1470 S1 and S2: the 1388 set and every column at 1024 (light, dark) and 1440', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  for (const [width, theme] of [[1024, 'light'], [1024, 'dark'], [1440, 'light']] as const) {
    for (const [set, prefs] of [['1388', { visible: SET_1388 }], ['all', { visible: ALL }]] as const) {
      const { errors } = await open(page, { width, theme, prefs })
      const widths = await headWidths(page)
      expect(widths.title).toBeGreaterThanOrEqual(240)
      log.add({ assert: 'title ≥ 240 px', state: `S${width === 1440 ? 2 : 1} ${set}`, width, tableWidth: await tableWidth(page), theme, value: Math.round(widths.title * 10) / 10 })
      await page.mouse.move(5, 5)
      await page.screenshot({ path: testInfo.outputPath(`aeon1470-${width}-${theme}-s${width === 1440 ? 2 : 1}-${set}.png`), fullPage: false })
      expect(errors).toEqual([])
    }
  }
  log.save()
})

test('AEON-1470 S5: Display keeps a dropped column ticked with "hidden at this width", and its sort stays offered', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page, { prefs: { visible: ALL }, query: '?group=status&sort=key' })
  const shown = await shownIds(page)
  const dropped = expectGiveWayOrder(ALL, shown)
  expect(dropped.length).toBeGreaterThan(3)
  await page.getByRole('button', { name: /^Display/ }).click()
  const menu = display(page)
  await expect(menu).toBeVisible()
  for (const id of ALL) {
    const box = menu.locator(`input[data-column-row="${id}"]`)
    if (!await box.count()) continue
    await expect(box).toBeChecked()
    const note = columnRow(menu, id).locator('.away-note')
    if (dropped.includes(id)) await expect(note).toHaveText('hidden at this width')
    else await expect(note).toHaveCount(0)
  }
  log.add({ assert: 'dropped columns stay ticked with the note', state: 'Display open', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: dropped })
  // The sort choices still offer every dropped column that sorts.
  const offered = await menu.getByRole('combobox', { name: 'Sort key 1' }).locator('option').allTextContents()
  for (const id of dropped) { const field = COLUMN_BY_ID.get(id)!.sort; if (field) expect(offered).toContain(SORT_LABELS[field]) }
  log.add({ assert: 'dropped column sorts stay in the sort choices', state: 'Display open', width: 1024, theme: 'light', value: dropped.filter(id => COLUMN_BY_ID.get(id)!.sort).map(id => SORT_LABELS[COLUMN_BY_ID.get(id)!.sort!]) })
  // The note never overflows its row.
  for (const id of dropped) {
    const item = columnRow(menu, id)
    expect(await item.evaluate(el => el.scrollWidth <= el.clientWidth + 1), id).toBe(true)
  }
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1024-light-s5-display.png'), fullPage: false })
  log.save()
})

// AEON-1470 (fix 7). Risk: a "hidden at this width" note that gives its row another height or wraps under a long
// name, so ticking, unticking or a column coming back moves the checkboxes below it (AEON-541).
test('AEON-1470 Columns rows hold still while hidden notes come and go', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  await open(page, { prefs: { visible: ALL }, query: '?group=status&sort=key' })
  const shown = await shownIds(page)
  const dropped = expectGiveWayOrder(ALL, shown)
  // Suggested release is the longest name the width hides: name and note do not fit the row together.
  expect(dropped).toContain('suggested')
  await page.getByRole('button', { name: /^Display/ }).click()
  const menu = display(page)
  await expect(menu).toBeVisible()
  const ids = await menu.locator('input[data-column-row]').evaluateAll(boxes => boxes.map(box => box.getAttribute('data-column-row')!))
  const controls: Record<string, Locator> = {}
  for (const id of ids) { controls[`check ${id}`] = menu.locator(`input[data-column-row="${id}"]`); controls[`row ${id}`] = columnRow(menu, id) }
  const guard = await controlStability(page, controls)
  const box = (id: string) => menu.locator(`input[data-column-row="${id}"]`)
  const note = (id: string) => columnRow(menu, id).locator('.away-note')
  await expect(note('suggested')).toHaveText('hidden at this width')
  // Unticked, the note leaves; ticked again, it returns. Neither moves a row.
  await guard.check(async () => { await box('suggested').uncheck(); await expect(note('suggested')).toHaveCount(0); await settled(page) })
  await guard.check(async () => { await box('suggested').check(); await expect(note('suggested')).toHaveText('hidden at this width'); await settled(page) })
  // Unticking two shown columns frees at least 168 px: the last hidden one (120 px at most) comes back with 16 px to
  // spare and its note leaves in place; ticking them again hides it again.
  const freed = ['cost', 'parent', 'assignee', 'release'].filter(id => shown.includes(id as ColumnId)).slice(0, 2)
  expect(freed).toHaveLength(2)
  const last = dropped[dropped.length - 1]
  await guard.check(async () => { for (const id of freed) await box(id).uncheck(); await expect(note(last)).toHaveCount(0); await settled(page) })
  await guard.check(async () => { for (const id of freed) await box(id).check(); await expect(note(last)).toHaveText('hidden at this width'); await settled(page) })
  guard.done()
  // The name gives way to the note on one line; its tip reads it whole.
  const name = columnRow(menu, 'suggested').locator('.name-text')
  const clipped = await name.evaluate(el => el.scrollWidth > el.clientWidth + 1)
  if (clipped) await expect(name).toHaveAttribute('data-tip', 'Suggested release')
  // A row with a hidden note is as tall as a plain one (rows with their own column note excepted).
  const plain = await columnRow(menu, 'status').evaluate(el => el.getBoundingClientRect().height)
  for (const id of dropped.filter(id => ids.includes(id))) {
    const row = await columnRow(menu, id).evaluate(el => ({ height: el.getBoundingClientRect().height, noted: !!el.querySelector('.col-note') }))
    if (!row.noted) expect(Math.abs(row.height - plain), id).toBeLessThanOrEqual(.5)
  }
  log.add({ assert: 'Columns rows and checkboxes move 0 px (±0.5) through ticks and returns', state: 'Display open, all columns', width: 1024, tableWidth: await tableWidth(page), theme: 'light', value: { dropped, toggled: ['suggested', ...freed], nameClipped: clipped } })
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1024-light-s5-display-rows.png'), fullPage: false })
  log.save()
})

test('AEON-1470 S6: phone rows at 390 stay as they were (light, dark)', async ({ page }, testInfo) => {
  const log = checks(testInfo)
  for (const theme of ['light', 'dark'] as const) {
    const { errors } = await open(page, { width: 390, theme })
    await expect(grid(page).locator('thead')).toBeHidden()
    await expect(groupRow(page, 'In progress').locator('.group-facts')).toHaveText('· 2 working · 2 stale · ~7.5h')
    const sideways = await page.evaluate(() => document.scrollingElement!.scrollWidth - window.innerWidth)
    expect(sideways).toBeLessThanOrEqual(0)
    log.add({ assert: 'phone line and no sideways scroll', state: 'S6', width: 390, theme, value: { facts: await groupRow(page, 'In progress').locator('.group-facts').textContent(), sideways } })
    await page.screenshot({ path: testInfo.outputPath(`aeon1470-390-${theme}-s6.png`), fullPage: false })
    expect(errors).toEqual([])
  }
  log.save()
})

// ---------- AEON-1470 Parent (spec v2) ----------
const parentHeader = (page: Page) => page.getByRole('columnheader', { name: 'Parent', exact: true })
function shareData(withParent: number): Fixtures {
  const d = fixtures()
  for (const node of d.nodes) if (node.project === 'p-pharos') node.parent_id = 'p-pharos'
  for (let index = 0; index < 4; index++) d.nodes.push({
    id: `n-share-${index}`, key: `PHAROS-${30 + index}`, kind_slug: 'ticket', title: `Share ticket ${index + 1}`,
    state: 'in_progress', project: 'p-pharos', parent_id: 'p-pharos', body: '', fields: {},
    created_at: '2026-09-01T10:00:00Z', updated_at: '2026-09-23T09:00:00Z',
  })
  const children = d.nodes.filter(node => node.project === 'p-pharos' && node.id !== 'n-epic' && node.state !== 'cancelled' && ['work', 'ticket', 'task', 'epic'].includes(node.kind_slug))
  for (const node of children.slice(0, withParent)) node.parent_id = 'n-epic'
  return d
}
async function wholeShare(page: Page, openRows: number, withParent: number) {
  return page.waitForResponse(async response => {
    if (!response.url().includes('/api/projects/p-pharos/figures') || !response.ok()) return false
    const body = await response.json() as { scope?: { kind?: string; open_rows?: number; open_with_parent?: number } }
    return body.scope?.kind === 'whole_project' && body.scope.open_rows === openRows && body.scope.open_with_parent === withParent
  })
}

test('AEON-1470 Parent cell shows the kind icon and key, adds the title from 200 px, and has no chip', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const d = fixtures()
  d.preferences.theme = { choice: 'light' }
  const landed = wholeShare(page, 6, 3)
  await mockWork(page, d)
  await page.goto('/p/PHAROS')
  await landed
  await expect(rows(page)).toHaveCount(5)
  await expect(parentHeader(page)).toBeVisible()
  await expect(page.locator('.parent-chip')).toHaveCount(0)
  const child = row(page, 'PHAROS-11')
  await expect(child.locator('.parent-glyph')).toHaveAttribute('width', '12')
  expect(await child.locator('.parent-glyph').innerHTML()).toContain('M9 1.8')
  await expect(child.locator('.parent-key')).toHaveText('PHAROS-10')
  await expect(child.locator('.parent-name')).toHaveCount(0)
  await expect(child.locator('td.c-parent')).toHaveAttribute('data-tip', 'Epic PHAROS-10\nGuarded multi-cloud provisioning')
  await expect(row(page, 'PHAROS-13').locator('.parent-key')).toHaveText('PHAROS-12')
  expect(await row(page, 'PHAROS-13').locator('.parent-glyph').innerHTML()).toContain('<rect')
  await expect(row(page, 'PHAROS-13').locator('td.c-parent')).toHaveAttribute('data-tip', 'Ticket PHAROS-12\nAdd an Oracle Cloud connector')
  const top = row(page, 'PHAROS-14').locator('td.c-parent')
  await expect(top.locator('.parent-cell')).toHaveCount(0)
  await expect(top).not.toHaveAttribute('data-tip')
  await expect(top).not.toContainText('—')
  const paint = await child.locator('.parent-cell').evaluate(el => { const style = getComputedStyle(el); return { radius: style.borderRadius, background: style.backgroundColor } })
  expect(paint.radius).toBe('0px')
  expect(paint.background === 'transparent' || paint.background === 'rgba(0, 0, 0, 0)').toBe(true)
  expect((await page.locator('thead th.c-title').boundingBox())!.width).toBeGreaterThanOrEqual(240)

  await page.setViewportSize({ width: 1024, height: 900 })
  // The resize observer and the render it triggers land before the second frame.
  await settled(page)
  await expect(parentHeader(page)).toBeVisible()
  await expect(page.locator('.parent-chip')).toHaveCount(0)
  expect((await page.locator('thead th.c-title').boundingBox())!.width).toBeGreaterThanOrEqual(240)
  await page.mouse.move(5, 5)
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1024-light-parent-on.png'), fullPage: false })

  d.preferences['list:p-pharos'] = { widths: { parent: 240 } }
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await expect(parentHeader(page)).toBeVisible()
  await expect(child.locator('.parent-name')).toHaveText('Guarded multi-cloud provisioning')
  await expect(row(page, 'PHAROS-14').locator('.parent-name')).toHaveCount(0)
  expect(Math.abs((await page.locator('thead th.c-parent').boundingBox())!.width - 240)).toBeLessThan(8)
  expect((await page.locator('thead th.c-title').boundingBox())!.width).toBeGreaterThanOrEqual(240)
  await page.mouse.move(5, 5)
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1440-light-parent-on.png'), fullPage: false })

  d.preferences.theme = { choice: 'dark' }
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/p/PHAROS')
  await expect(child.locator('.parent-name')).toHaveText('Guarded multi-cloud provisioning')
  await expect(page.locator('.parent-chip')).toHaveCount(0)
  await page.mouse.move(5, 5)
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1440-dark-parent-on.png'), fullPage: false })
})

// AEON-1470 (fix 7). Risk: the Parent header counting each ticket's epic while its cells show the direct parent, so the
// task PHAROS-13 under ticket PHAROS-12 adds to epic PHAROS-10 instead.
test('AEON-1470 the Parent header counts the direct parents its cells show, with "No parent"', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const d = fixtures()
  d.preferences.theme = { choice: 'light' }
  const landed = wholeShare(page, 6, 3)
  await mockWork(page, d)
  await page.goto('/p/PHAROS')
  await landed
  await expect(parentHeader(page)).toBeVisible()
  await expect(row(page, 'PHAROS-13').locator('.parent-key')).toHaveText('PHAROS-12')
  await page.mouse.move(5, 5)
  await parentHeader(page).locator('.th-label').hover()
  const tip = page.locator('#figure-tip')
  await expect(tip).toBeVisible()
  const lines = tip.locator('.wf-line:not([aria-hidden])')
  await expect(lines.first()).toBeVisible()
  const read = () => lines.evaluateAll(items => items.map(item => [item.firstElementChild?.textContent?.trim(), item.querySelector('.wf-v')?.textContent?.trim()]))
  // Leaves: PHAROS-11 under the epic, PHAROS-13 under its ticket, three straight under the project.
  expect(await read()).toEqual([['PHAROS-10', '1'], ['PHAROS-12', '1'], ['No parent', '3']])
  await page.mouse.move(5, 5)
})

test('AEON-1470 Parent stays out of the Epic group and the outline, and the column toggle does not move the other columns', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const d = fixtures()
  d.preferences.theme = { choice: 'light' }
  const landed = wholeShare(page, 6, 3)
  await mockWork(page, d)
  await page.goto('/p/PHAROS?group=epic')
  await landed
  await expect(page.locator('tr.group-row').first()).toBeVisible()
  await expect(parentHeader(page)).toHaveCount(0)
  await expect(page.locator('.parent-chip')).toHaveCount(0)
  const before = await shownIds(page)
  expect(before).toEqual(['key', 'title', 'status', 'priority', 'assignee', 'updated'])
  await page.mouse.move(5, 5)
  await page.screenshot({ path: testInfo.outputPath('aeon1470-1440-light-parent-grouped.png'), fullPage: false })
  await page.getByRole('button', { name: /^Display/ }).click()
  const menu = display(page)
  await expect(menu.getByRole('radio', { name: 'Epic' })).toBeChecked()
  const parentBox = columnRow(menu, 'parent')
  await expect(parentBox.locator('.away-note')).toHaveCount(0)
  await parentBox.getByRole('checkbox', { name: 'Parent' }).check()
  await expect(parentHeader(page)).toHaveCount(0)
  expect(await shownIds(page)).toEqual(before)
  await expect(parentBox.locator('.away-note')).toHaveCount(0)
  await page.keyboard.press('Escape')

  await page.goto('/p/PHAROS?view=outline')
  const outline = page.getByRole('treegrid', { name: 'Ticket outline' })
  await expect(outline).toBeVisible()
  await expect(outline.getByRole('columnheader', { name: 'Parent', exact: true })).toHaveCount(0)
  const outlineIds = () => outline.locator('thead th').evaluateAll(cells => cells.map(cell => cell.getAttribute('data-column-id')))
  const outlineBefore = await outlineIds()
  expect(outlineBefore).not.toContain('parent')
  await page.getByRole('button', { name: /^Display/ }).click()
  await display(page).getByRole('checkbox', { name: 'Parent' }).click()
  await expect(outline.getByRole('columnheader', { name: 'Parent', exact: true })).toHaveCount(0)
  expect(await outlineIds()).toEqual(outlineBefore)
})

test('AEON-1470 Parent keeps a saved Epic column and its width', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const d = shareData(1)
  d.preferences['list:p-pharos'] = {
    order: ['status', 'epic', 'assignee', 'updated'],
    visible: ['status', 'priority', 'epic', 'assignee', 'updated'],
    widths: { epic: 240 },
  }
  await mockWork(page, d)
  await page.goto('/p/PHAROS')
  await expect(rows(page).first()).toBeVisible()
  await expect(parentHeader(page)).toBeVisible()
  expect(await shownIds(page)).toEqual(['key', 'title', 'status', 'parent', 'assignee', 'updated', 'priority'])
  expect(Math.abs((await page.locator('thead th.c-parent').boundingBox())!.width - 240)).toBeLessThan(8)
  await expect(page.locator('.parent-chip')).toHaveCount(0)
  const saved = d.preferences['list:p-pharos'] as { order?: string[]; visible?: string[]; widths?: Record<string, number> }
  saved.widths = { parent: 200, epic: 240 }
  await page.goto('/p/PHAROS')
  await expect(parentHeader(page)).toBeVisible()
  expect(Math.abs((await page.locator('thead th.c-parent').boundingBox())!.width - 200)).toBeLessThan(8)

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  await expect(page.locator('[data-column-id="parent"]')).toHaveCount(0)
  await expect(page.locator('.parent-chip')).toHaveCount(0)
})

test('AEON-1470 Parent turns on at 40 percent, stays off at 10 percent, and a live parent does not change the columns', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const on = shareData(4)
  const onLanded = wholeShare(page, 10, 4)
  await mockWork(page, on)
  await page.goto('/p/PHAROS')
  await onLanded
  await expect(rows(page).first()).toBeVisible()
  await expect(parentHeader(page)).toBeVisible()

  const off = shareData(1)
  const offLanded = wholeShare(page, 10, 1)
  await mockWork(page, off)
  await page.goto('/p/PHAROS')
  await offLanded
  await expect(rows(page).first()).toBeVisible()
  const before = await shownIds(page)
  expect(before).not.toContain('parent')
  await expect(parentHeader(page)).toHaveCount(0)
  const ticket = off.nodes.find(node => node.id === 'n-4')!
  ticket.parent_id = 'n-epic'
  ticket.updated_at = '2026-09-23T11:59:00Z'
  for (const id of ['n-share-0', 'n-share-1']) off.nodes.find(node => node.id === id)!.parent_id = 'n-epic'
  await page.evaluate(([at]) => (window as unknown as { aeon1470Emit: (type: string, data: unknown, id: number) => void }).aeon1470Emit('node.updated', { id: 41, type: 'node.updated', actor_principal_id: '99999999-9999-4999-8999-999999999999', node_changes: [{ id: 'n-4', project_id: 'p-pharos', change: 'updated', fields: ['parent_id'], revision: at }] }, 41), [ticket.updated_at])
  await expect.poll(() => shownIds(page)).toEqual(before)
  await expect(parentHeader(page)).toHaveCount(0)
})
