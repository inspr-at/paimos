// SPDX-License-Identifier: AGPL-3.0-only
// AEON-974: one Type filter in the workspace's own names (leaf, else a parent's
// level), and a toolbar that shows only applied filters: Filter first and still,
// a pill per applied filter, then Clear all.
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { controlStability } from './control-stability'
import { fixtures, mockView, mockWork, watchErrors, type Call } from './work-fixtures'

const shots = 'test-results/aeon-974-typefilter'
const LEAF = 'Arbeitsschritt mit ausführlicher Beschreibung'
const TOP = 'Vorhaben'
const toolbar = (page: Page) => page.getByRole('toolbar', { name: 'Ticket list controls' })
const filterButton = (page: Page) => toolbar(page).getByRole('button', { name: 'Add a filter' })
const rows = (page: Page) => page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row:not(.ghost)')
const lastList = (calls: Call[]) => calls.filter(call => call.path === '/api/nodes' && call.query.get('within') && call.query.get('limit') === '200').at(-1)!
// The newest list request's levels, undefined before the first one.
const level = (calls: Call[]) => calls.filter(call => call.path === '/api/nodes' && call.query.get('within') && call.query.get('limit') === '200').at(-1)?.query.get('level')

async function world(page: Page, theme = 'light', vocabulary = { revision: 3, leaf: { name: LEAF, icon: 'check' }, levels: [{ name: TOP, icon: 'tree' }, { name: 'Teilvorhaben', icon: 'layers' }] }) {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { headerGraph: false }
  // Every item is work; its level follows its shape: the leaf, else its depth.
  for (const n of data.nodes) {
    n.kind_slug = 'work'
    n.is_leaf = !data.nodes.some(c => c.parent_id === n.id)
    let parent = data.nodes.find(p => p.id === n.parent_id), depth = 1
    while (parent) { depth++; parent = data.nodes.find(p => p.id === parent!.parent_id) }
    n.depth = depth
  }
  const calls = await mockWork(page, data)
  await page.route('**/api/settings/work-vocabulary', route => route.fulfill({ json: vocabulary }))
  return { data, calls }
}
async function addFilter(page: Page, name: string) {
  await filterButton(page).click()
  // An applied filter's item also names how many values it holds ("Type 2").
  await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: new RegExp(`^${name}( \\d+)?$`) }).click()
  return page.getByRole('dialog', { name: `Filter by ${name}` })
}

test('the toolbar shows Filter and only applied filters; Type uses the workspace names and never moves Filter or the list', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const { calls } = await world(page)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  // Nothing applied: no empty Status, Priority, Assignee or Type controls, and no count.
  await expect(toolbar(page).locator('.facet-control')).toHaveCount(0)
  for (const name of ['Status', 'Priority', 'Assignee', 'Type', 'Legacy type', 'Parents / Leaves']) await expect(toolbar(page).getByRole('button', { name, exact: true })).toHaveCount(0)
  await expect(toolbar(page)).not.toContainText(/\d+ tickets/)
  await expect(toolbar(page).getByRole('button', { name: 'Clear all' })).toHaveCount(0)
  // Filter offers Type once; Parents / Leaves, Depth and the legacy type are gone.
  await filterButton(page).click()
  await expect(page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem')).toHaveText(['Status', 'Priority', 'Assignee', 'Type', 'Labels', 'Human check', 'Parent', 'Cost unit', 'Imported release', 'Date'])
  await page.keyboard.press('Escape')

  const guard = await controlStability(page, { filter: filterButton(page), search: toolbar(page).getByRole('searchbox'), listHead: page.getByRole('grid', { name: 'Tickets' }).locator('thead tr'), firstRow: rows(page).first() })
  await guard.check(async () => {
    const type = await addFilter(page, 'Type')
    // Top level first, the leaf last, each counted by the server's level facet.
    await expect.poll(() => calls.some(call => call.path === '/api/nodes' && call.query.get('facets') === 'level')).toBe(true)
    await expect(type.locator('.facet-option .option-label')).toHaveText([TOP, 'Teilvorhaben', LEAF])
    await expect(type.locator('.facet-option').filter({ hasText: LEAF }).locator('.count')).toHaveText('3')
    await type.getByRole('checkbox', { name: new RegExp(LEAF) }).check()
    await expect(rows(page)).toHaveCount(3)
    await page.keyboard.press('Escape')
  })
  expect(lastList(calls).query.get('level')).toBe('leaf')
  expect(lastList(calls).query.get('kind')).toBe('work,ticket,task,epic')
  await expect(page).toHaveURL(/type=leaf/)
  const pill = toolbar(page).getByRole('button', { name: `Edit Type filter: ${LEAF}` })
  await expect(pill).toContainText(`Type · ${LEAF}`)
  await expect(toolbar(page).getByRole('button', { name: 'Clear all' })).toBeVisible()
  // A second filter adds a pill to the right; Filter and the list stay put.
  await guard.check(async () => {
    const status = await addFilter(page, 'Status')
    await status.getByRole('checkbox', { name: /New/ }).check()
    await page.keyboard.press('Escape')
    await expect(toolbar(page).locator('.facet-control')).toHaveCount(2)
  })
  const [filterBox, typeBox, statusBox, clearBox] = await Promise.all([filterButton(page), pill, toolbar(page).locator('.facet-btn[data-dim="status"]'), toolbar(page).getByRole('button', { name: 'Clear all' })].map(l => l.boundingBox()))
  expect(filterBox!.x + filterBox!.width).toBeLessThanOrEqual(typeBox!.x)
  // Pills keep the order they were applied in: Status joins after Type.
  expect(typeBox!.x + typeBox!.width).toBeLessThanOrEqual(statusBox!.x)
  expect(statusBox!.x).toBeLessThan(clearBox!.x)
  await mkdir(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/applied-pills-1440-light.png` })
  // Removing pills by their button and by Backspace moves nothing; focus returns to Filter.
  await guard.check(async () => {
    await toolbar(page).getByRole('button', { name: 'Remove Status filter' }).click()
    await expect(page).not.toHaveURL(/status=/)
    await pill.focus()
    await page.keyboard.press('Backspace')
    await expect(toolbar(page).locator('.facet-control')).toHaveCount(0)
    await expect(filterButton(page)).toBeFocused()
  })
  guard.done()
  await expect(rows(page)).toHaveCount(5)
  expect(errors).toEqual([])
})

test('levels that share a name are one Type option, one pill and one request for all of them', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await world(page, 'light', { revision: 1, leaf: { name: 'Schritt', icon: 'check' }, levels: [{ name: TOP, icon: 'tree' }, { name: 'Schritt', icon: '' }] })
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  const type = await addFilter(page, 'Type')
  await expect(type.locator('.facet-option .option-label')).toHaveText([TOP, 'Schritt'])
  // PHAROS-12 is a level-2 parent named like the leaf: it counts and matches with the leaves.
  await expect(type.locator('.facet-option').filter({ hasText: 'Schritt' }).locator('.count')).toHaveText('4')
  await type.getByRole('checkbox', { name: /Schritt/ }).check()
  await expect.poll(() => lastList(calls).query.get('level')).toBe('2,leaf')
  await expect(rows(page)).toHaveCount(4)
  await page.keyboard.press('Escape')
  await expect(toolbar(page).getByRole('button', { name: 'Edit Type filter: Schritt', exact: true })).toBeVisible()
})

test('a shared-name Type option shows a partial saved choice as mixed; a click completes it and never narrows it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await world(page, 'light', { revision: 1, leaf: { name: 'Schritt', icon: 'check' }, levels: [{ name: TOP, icon: 'tree' }, { name: 'Schritt', icon: '' }] })
  const mixed = (box: ReturnType<Page['locator']>) => box.evaluate(el => (el as HTMLInputElement).indeterminate)
  // A link that holds only level 2 of the two levels named "Schritt".
  await page.goto('/p/PHAROS?type=2')
  await expect.poll(() => level(calls)).toBe('2')
  let type = await addFilter(page, 'Type')
  const step = type.getByRole('checkbox', { name: /Schritt/ })
  await expect(step).not.toBeChecked()
  expect(await mixed(step)).toBe(true)
  await step.click()
  await expect.poll(() => level(calls)).toBe('2,leaf')
  await expect(step).toBeChecked()
  expect(await mixed(step)).toBe(false)
  await step.click()
  await expect.poll(() => level(calls)).toBeNull()
  await expect(step).not.toBeChecked()
  // Excluding only level 2 is mixed too; "not" then excludes both.
  await page.goto('/p/PHAROS?type=!2')
  await expect.poll(() => level(calls)).toBe('!2')
  type = await addFilter(page, 'Type')
  const not = type.getByRole('button', { name: 'Exclude Schritt' })
  expect(await mixed(type.getByRole('checkbox', { name: /Schritt/ }))).toBe(true)
  await expect(not).toHaveAttribute('aria-pressed', 'false')
  await not.click()
  await expect.poll(() => level(calls)).toBe('!2,!leaf')
  await expect(not).toHaveAttribute('aria-pressed', 'true')
})

// The level counts answer only when the test releases them; unfiltered: only
// the counts an applied Type filter reads, asked without it.
async function heldLevelCounts(page: Page, unfiltered = false) {
  const state = { asked: false, release: () => {} }
  const gate = new Promise<void>(resolve => { state.release = resolve })
  await page.route(url => url.pathname === '/api/nodes' && url.searchParams.get('facets') === 'level' && (!unfiltered || !url.searchParams.get('level')), async route => {
    state.asked = true
    await gate
    await route.fallback()
  })
  return state
}
const DEFAULT_NAMES = { revision: 0, leaf: { name: '', icon: '' }, levels: [] }

test('Type rows wait for their names and counts, then hold their places while the menu is open', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await world(page, 'light', DEFAULT_NAMES)
  const counts = await heldLevelCounts(page)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  const type = await addFilter(page, 'Type')
  await expect.poll(() => counts.asked).toBe(true)
  // Nothing to click while the answer is out: Story would land between Epic and Ticket.
  await expect(type.getByRole('status')).toHaveText('Loading…')
  await expect(type.getByRole('checkbox')).toHaveCount(0)
  counts.release()
  await expect(type.locator('.facet-option .option-label')).toHaveText(['Epic', 'Story', 'Ticket'])
  await expect(type.getByRole('checkbox', { name: /Epic/ })).toBeFocused()
  const box = (name: string) => type.getByRole('checkbox', { name: new RegExp(`^${name}`) })
  const guard = await controlStability(page, { epic: box('Epic'), story: box('Story'), ticket: box('Ticket'), title: type.locator('.facet-head') })
  await guard.check(async () => {
    await box('Story').check()
    await expect.poll(() => level(calls)).toBe('2')
    await expect(rows(page)).toHaveCount(1)
  })
  await guard.check(async () => {
    await box('Epic').check()
    await expect.poll(() => level(calls)).toBe('2,1')
  })
  guard.done()
})

test('the Filters sheet shows its filters once their counts are in and keeps every row in place', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { calls } = await world(page, 'light', DEFAULT_NAMES)
  const counts = await heldLevelCounts(page)
  await page.goto('/p/PHAROS')
  await expect(rows(page).first()).toBeVisible()
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  await expect.poll(() => counts.asked).toBe(true)
  await expect(sheet.getByText('Loading filters…')).toBeVisible()
  await expect(sheet.locator('.sheet-section .eyebrow', { hasText: /^Type$/ })).toHaveCount(0)
  counts.release()
  const type = sheet.locator('.sheet-section').filter({ has: page.locator('.eyebrow', { hasText: /^Type$/ }) })
  await expect(type.locator('.facet-option .option-label')).toHaveText(['Epic', 'Story', 'Ticket'])
  await type.scrollIntoViewIfNeeded()
  const box = (name: string) => type.getByRole('checkbox', { name: new RegExp(`^${name}`) })
  const guard = await controlStability(page, { epic: box('Epic'), story: box('Story'), ticket: box('Ticket'), today: sheet.getByRole('group', { name: 'Period' }).getByRole('button', { name: 'Today' }), done: sheet.locator('footer button') })
  await guard.check(async () => {
    await box('Story').check()
    await expect.poll(() => level(calls)).toBe('2')
    await expect(box('Story')).toBeChecked()
  })
  guard.done()
})

test('with Type applied, the Filters sheet waits for its unfiltered counts; a later new value never moves the sections below', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { data, calls } = await world(page, 'light', DEFAULT_NAMES)
  const counts = await heldLevelCounts(page, true)
  await page.goto('/p/PHAROS?type=leaf')
  await expect(rows(page).first()).toBeVisible()
  await expect.poll(() => counts.asked).toBe(true)
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  // Story would join the Type section late and push Labels and Date down.
  await expect(sheet.getByText('Loading filters…')).toBeVisible()
  await expect(sheet.getByRole('checkbox', { name: /^Ticket/ })).toHaveCount(0)
  counts.release()
  const section = (title: string) => sheet.locator('.sheet-section').filter({ has: page.locator('.eyebrow', { hasText: new RegExp(`^${title}$`) }) })
  const type = section('Type')
  await expect(type.locator('.facet-option .option-label')).toHaveText(['Epic', 'Story', 'Ticket'])
  await expect(type.getByRole('checkbox', { name: /^Ticket/ })).toBeChecked()
  await type.scrollIntoViewIfNeeded()
  const box = (name: string) => type.getByRole('checkbox', { name: new RegExp(`^${name}`) })
  const guard = await controlStability(page, {
    story: box('Story'), ticket: box('Ticket'), labels: section('Labels').locator('.eyebrow'),
    bug: section('Labels').getByRole('checkbox', { name: /^BUG/ }), today: sheet.getByRole('group', { name: 'Period' }).getByRole('button', { name: 'Today' }),
  })
  // A ticket in a status the sheet does not show arrives with the next answer.
  data.nodes.push({ ...data.nodes.find(n => n.id === 'n-4')!, id: 'n-late', key: 'PHAROS-17', title: 'Parked meanwhile', state: 'parked' })
  await guard.check(async () => {
    await box('Story').check()
    await expect.poll(() => level(calls)).toBe('leaf,2')
    await expect(rows(page).filter({ hasText: 'Parked meanwhile' })).toHaveCount(1)
    await expect(box('Story')).toBeChecked()
  })
  await expect(section('Status').getByRole('checkbox', { name: /parked/i })).toHaveCount(0)
  guard.done()
})

for (const width of [1024, 940]) {
  test(`Filter and Search stay put while pills overflow, come and go at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await world(page)
    await page.goto('/p/PHAROS?status=new&priority=high&assignee=none&type=leaf&tag=BUG&human_check=none&epic=none&cost=none&release=none&date=updated:7d')
    await expect(toolbar(page).locator('.facet-control')).toHaveCount(10)
    const pills = toolbar(page).locator('.pills')
    // Only the pills scroll; Filter and Clear all keep their room.
    expect(await pills.evaluate(el => el.scrollWidth - el.clientWidth)).toBeGreaterThan(20)
    expect(await toolbar(page).locator('.facets').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    const guard = await controlStability(page, { filter: filterButton(page), search: toolbar(page).getByRole('searchbox') })
    for (const name of ['Status', 'Priority', 'Assignee', 'Labels', 'Human check', 'Parent', 'Cost unit', 'Imported release', 'Type']) {
      await guard.check(async () => {
        await toolbar(page).getByRole('button', { name: `Remove ${name} filter` }).click()
        await expect(toolbar(page).getByRole('button', { name: `Remove ${name} filter` })).toHaveCount(0)
      })
    }
    await guard.check(async () => {
      const status = await addFilter(page, 'Status')
      await status.getByRole('checkbox', { name: /New/ }).check()
      await page.keyboard.press('Escape')
      await expect(toolbar(page).locator('.facet-control')).toHaveCount(2)
    })
    guard.done()
  })
}

test('links and saved views with the legacy type or Parents / Leaves open as Type', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { data, calls } = await world(page)
  data.views.push(mockView({ id: '11111111-aaaa-4aaa-8aaa-000000000009', name: 'Old epics', filters: { type: 'epic' } }))
  await page.goto('/p/PHAROS?type=epic')
  await expect(toolbar(page).getByRole('button', { name: `Edit Type filter: ${TOP}`, exact: true })).toBeVisible()
  await expect.poll(() => level(calls)).toBe('1')
  expect(lastList(calls).query.get('kind')).toBe('work,ticket,task,epic')
  await expect(rows(page)).toHaveCount(1)
  await page.goto('/p/PHAROS?shape=leaf')
  await expect(toolbar(page).getByRole('button', { name: `Edit Type filter: ${LEAF}`, exact: true })).toBeVisible()
  await expect(toolbar(page).locator('.facet-control')).toHaveCount(1)
  await expect.poll(() => level(calls)).toBe('leaf')
  // The saved view reads as Type and is unchanged (no dot, no Save).
  await page.goto('/p/PHAROS')
  const views = page.getByRole('navigation', { name: 'Saved views' })
  await views.getByRole('link', { name: /Old epics/ }).click()
  await expect(toolbar(page).getByRole('button', { name: `Edit Type filter: ${TOP}`, exact: true })).toBeVisible()
  await expect(views.locator('.dirty')).toBeHidden()
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`applied Type filter at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark', reducedMotion: 'reduce' })
    await world(page, theme)
    await mkdir(shots, { recursive: true })
    await page.goto('/p/PHAROS?type=leaf,1&status=new,backlog,in-progress&priority=high')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(rows(page).first()).toBeVisible()
    if (width <= 900) {
      // Phones and small tablets keep every filter in the Filters sheet.
      await page.getByRole('button', { name: 'Filters', exact: true }).click()
      const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
      const type = sheet.locator('.sheet-section').filter({ has: page.locator('.eyebrow', { hasText: /^Type$/ }) })
      await type.scrollIntoViewIfNeeded()
      await expect(type.locator('.facet-option .option-label')).toHaveText([TOP, 'Teilvorhaben', LEAF])
      await expect(sheet.getByText('Parents / Leaves', { exact: true })).toHaveCount(0)
      await page.screenshot({ path: `${shots}/sheet-${width}-${theme}.png` })
      return
    }
    await expect(toolbar(page).locator('.facet-control')).toHaveCount(3)
    await expect(toolbar(page).getByRole('button', { name: `Edit Type filter: ${LEAF}, ${TOP}`, exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
    await page.screenshot({ path: `${shots}/toolbar-${width}-${theme}.png` })
    const type = await addFilter(page, 'Type')
    await expect(type).toBeVisible()
    await page.screenshot({ path: `${shots}/type-menu-${width}-${theme}.png` })
  })
}
