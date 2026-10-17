// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1388 (group summaries, the header's first cell) and AEON-1390 (sorting in grouped lists), on the fixture list API
// and its figures endpoint. Risk: group figures that lie (a sum its leaf rows don't make, a parent's planned hours added,
// "—" or 0 where nothing is to say, a figure in the grouped-by column), controls that move when figures land, on hover or
// when the first cell swaps KEY for its controls, sorting by key that becomes unreachable, a group order that a header
// click doesn't change or that "No …" groups break, and folds or group order lost on reload or in a saved view.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, liveAgent, mockView, mockWork, watchErrors, type Fixtures } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const rows = (page: Page) => grid(page).locator('tr.ticket-row:not(.ghost)')
const groupRow = (page: Page, label: string) => grid(page).locator('tr.group-row').filter({ has: page.locator('.group-label', { hasText: new RegExp(`^${label}$`) }) })
const cell = (page: Page, label: string, column: string) => groupRow(page, label).locator(`td[data-group-column="${column}"] .gf-text`)
const labels = (page: Page) => grid(page).locator('.group-row .group-label')
const keyHead = (page: Page) => grid(page).locator('thead th.key-head')
const head = (page: Page, id: string) => grid(page).locator(`thead th[data-column-id="${id}"]`)
const hint = (page: Page) => page.locator('#figure-tip .wf-hint')
const display = (page: Page) => page.getByRole('dialog', { name: 'Display options' })
const COLUMNS = ['key', 'title', 'status', 'priority', 'assignee', 'estimate', 'updated']
const AGENT = '33333333-3333-4333-8333-333333333333'
const BOT = '44444444-4444-4444-8444-444444444444'

// The fixture project plus in-progress work: two tickets with a live session, two stale ones nobody works on. Leaves carry
// estimates; the parent PHAROS-12 carries planned hours of its own that no group may add.
function data(): Fixtures {
  const d = fixtures()
  const node = (id: string) => d.nodes.find(n => n.id === id)!
  Object.assign(node('n-1').fields, { estimate_hours: 4 })
  Object.assign(node('n-2').fields, { estimate_hours: 10 })
  Object.assign(node('n-3').fields, { estimate_hours: 2.5 })
  Object.assign(node('n-4').fields, { estimate_hours: 1 })
  Object.assign(node('n-5').fields, { estimate_hours: 3 })
  const extra = (id: string, key: string, title: string, fields: Record<string, unknown>, more: Partial<Fixtures['nodes'][number]> = {}) =>
    d.nodes.push({ id, key, kind_slug: 'ticket', title, state: 'in_progress', project: 'p-pharos', parent_id: 'p-pharos', body: '', fields, created_at: '2026-09-01T10:00:00Z', updated_at: '2026-09-23T09:00:00Z', ...more })
  extra('n-7', 'PHAROS-17', 'Rotate the bastion keys', { priority: 'high', estimate_hours: 2 }, { queue_stale: true })
  extra('n-8', 'PHAROS-18', 'Prune unused images', { priority: 'medium' }, { queue_stale: true })
  extra('n-9', 'PHAROS-19', 'Wire the probe dashboard', { priority: 'high', estimate_hours: 1.5 })
  d.live.push(
    liveAgent({ project_id: 'p-pharos', session_id: 's-1', principal_id: AGENT, name: 'Builder', ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', project_id: 'p-pharos' } }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-2', principal_id: BOT, name: 'Reviewer', harness: 'codex', ticket: { id: 'n-9', key: 'PHAROS-19', title: 'Wire the probe dashboard', project_id: 'p-pharos' } }),
  )
  d.preferences['list:display'] = { headerGraph: false }
  d.preferences['list:p-pharos'] = { visible: COLUMNS }
  return d
}

async function open(page: Page, options: { width?: number; theme?: 'light' | 'dark'; query?: string; data?: Fixtures; routes?: (page: Page) => Promise<void> } = {}) {
  const width = options.width ?? 1440
  await page.setViewportSize({ width, height: width <= 600 ? 844 : 900 })
  const d = options.data ?? data()
  d.preferences.theme = { choice: options.theme ?? 'light' }
  const errors = watchErrors(page)
  const calls = await mockWork(page, d)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  await options.routes?.(page)
  await page.goto(`/p/PHAROS${options.query ?? '?group=status'}`)
  await expect(rows(page).first()).toBeVisible()
  return { calls, errors, data: d }
}
const boxOf = (locator: Locator) => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
async function boxes(locators: Record<string, Locator>) {
  const out: Record<string, { x: number; y: number; width: number; height: number }> = {}
  for (const [name, locator] of Object.entries(locators)) out[name] = await boxOf(locator)
  return out
}
function same(before: Awaited<ReturnType<typeof boxes>>, after: Awaited<ReturnType<typeof boxes>>) {
  for (const name of Object.keys(before)) for (const side of ['x', 'y', 'width', 'height'] as const) {
    expect(Math.abs(after[name][side] - before[name][side]), `${name}.${side}`).toBeLessThanOrEqual(.5)
  }
}
// Shown or hidden as a person sees it: KEY hides by visibility, the slot controls are transparent at rest (still reachable).
const visibility = (locator: Locator) => locator.evaluate(el => getComputedStyle(el).visibility === 'visible' && getComputedStyle(el).opacity === '1' ? 'visible' : 'hidden')
async function settled(page: Page) { await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))) }

test('AEON-1388 each group row carries its figures in its own columns: sums of its leaves, nothing where there is nothing to say', async ({ page }) => {
  const { errors, data: d } = await open(page)
  await expect(labels(page)).toHaveText(['New', 'Backlog', 'In progress', 'QA'])
  // In progress: four leaves, 4 + 2 + 1.5 h estimated (PHAROS-18 has none); the leaves only, as in the header.
  const progress = d.nodes.filter(n => n.project === 'p-pharos' && ['in-progress', 'in_progress'].includes(n.state))
  const sum = progress.reduce((total, n) => total + (typeof n.fields.estimate_hours === 'number' ? n.fields.estimate_hours : 0), 0)
  await expect(cell(page, 'In progress', 'estimate')).toHaveText(`~${sum}h`)
  await expect(cell(page, 'In progress', 'priority')).toHaveText('3 high · 1 med')
  // "N working" is the group's rows with a live session; their agents lead the cell.
  await expect(cell(page, 'In progress', 'assignee')).toHaveText('2 working')
  await expect(groupRow(page, 'In progress').locator('td[data-group-column="assignee"] .gf-agents > *')).toHaveCount(2)
  // Stale after the name, the word in the attention ink.
  await expect(groupRow(page, 'In progress').locator('.group-stale')).toHaveText('· 2 stale')
  const ink = await page.evaluate(() => { const probe = document.createElement('span'); probe.style.color = 'var(--warn-ink)'; document.body.append(probe); const color = getComputedStyle(probe).color; probe.remove(); return color })
  expect(await groupRow(page, 'In progress').locator('.group-stale .attn').evaluate(el => getComputedStyle(el).color)).toBe(ink)
  expect(await groupRow(page, 'In progress').locator('.group-stale').evaluate(el => getComputedStyle(el).color)).not.toBe(ink)
  // Backlog holds only parents: the epic and PHAROS-12 with its 10 planned hours. Nothing is added, nothing shows.
  await expect(cell(page, 'Backlog', 'estimate')).toHaveText('')
  await expect(cell(page, 'New', 'estimate')).toHaveText('~1h')
  await expect(cell(page, 'QA', 'estimate')).toHaveText('~2.5h')
  // The grouped-by column stays empty, and no group cell says "—" or 0.
  for (const label of ['New', 'Backlog', 'In progress', 'QA']) await expect(cell(page, label, 'status')).toHaveText('')
  const texts = await grid(page).locator('.group-row .gf-text').allTextContents()
  expect(texts.filter(text => /^\s*(—|0)(\s|$)|\b0 (working|stale|high|med|low|people)/.test(text))).toEqual([])
  // Each figure's tip is its column's figures for the group, titled with the group.
  await cell(page, 'In progress', 'estimate').hover()
  const tip = page.locator('#figure-tip')
  await expect(tip.locator('.wf-ttl')).toHaveText('In progress · 4 tickets')
  await expect(tip.locator('.wf-line.head .wf-v')).toHaveText(`~${sum}h`)
  // Figures are quiet (12 px, tabular) and turn ink-2 on hover of the group row; hover moves nothing.
  const figure = cell(page, 'In progress', 'priority')
  expect(await figure.evaluate(el => [getComputedStyle(el).fontSize, getComputedStyle(el).fontVariantNumeric])).toEqual(['12px', 'tabular-nums'])
  await page.mouse.move(5, 5)
  const before = await boxes({ row: groupRow(page, 'In progress'), estimate: cell(page, 'In progress', 'estimate'), priority: figure, head: keyHead(page) })
  const rest = await figure.evaluate(el => getComputedStyle(el.parentElement!).color)
  await groupRow(page, 'In progress').locator('.group-label').hover()
  await expect.poll(() => figure.evaluate(el => getComputedStyle(el.parentElement!).color)).not.toBe(rest)
  same(before, await boxes({ row: groupRow(page, 'In progress'), estimate: cell(page, 'In progress', 'estimate'), priority: figure, head: keyHead(page) }))
  expect(errors).toEqual([])
})

test('AEON-1388 figures land in reserved slots: the group row and the header never move while they arrive', async ({ page }) => {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  // Registered after the fixture API, so this hold sees the group counts first.
  await open(page, { routes: async target => target.route('**/api/projects/*/figures**', async route => {
    if (new URL(route.request().url()).searchParams.get('work_state')) await gate
    await route.fallback()
  }) })
  await expect(cell(page, 'In progress', 'estimate')).toHaveText('')
  await settled(page)
  const watch = { row: groupRow(page, 'In progress'), label: groupRow(page, 'In progress').locator('.group-label'), estimate: groupRow(page, 'In progress').locator('td[data-group-column="estimate"]'), first: rows(page).first(), head: keyHead(page) }
  const before = await boxes(watch)
  release()
  await expect(cell(page, 'In progress', 'estimate')).toHaveText('~7.5h')
  same(before, await boxes(watch))
})

test('AEON-1388 the first header cell swaps KEY for three controls in fixed slots, on hover of that cell only', async ({ page }) => {
  await open(page)
  const fold = keyHead(page).locator('.key-fold'), check = keyHead(page).locator('.key-check'), sort = keyHead(page).locator('.key-icon'), key = keyHead(page).locator('.key-rest')
  const slots = { fold, check, sort, head: keyHead(page), title: head(page, 'title') }
  await page.mouse.move(700, 600)
  // At rest: KEY, the controls hidden in their slots.
  expect(await Promise.all([visibility(key), visibility(fold), visibility(check), visibility(sort)])).toEqual(['visible', 'hidden', 'hidden', 'hidden'])
  const rest = await boxes(slots)
  // The chevron sits right above the group chevrons, the checkbox right above the group checkboxes.
  const groupToggle = await boxOf(groupRow(page, 'New').locator('.group-toggle'))
  const groupCheck = await boxOf(groupRow(page, 'New').locator('.group-check-target'))
  expect(Math.abs(rest.fold.x - groupToggle.x)).toBeLessThanOrEqual(.5)
  expect(Math.abs(rest.check.x + rest.check.width / 2 - (groupCheck.x + groupCheck.width / 2))).toBeLessThanOrEqual(.5)
  // Hover of the Title header shows nothing here; hover of this cell shows the three controls and hides KEY.
  await head(page, 'title').hover()
  expect(await visibility(fold)).toBe('hidden')
  await keyHead(page).hover({ position: { x: (await boxOf(keyHead(page))).width - 14, y: 10 } })
  expect(await Promise.all([visibility(key), visibility(fold), visibility(check), visibility(sort)])).toEqual(['hidden', 'visible', 'visible', 'visible'])
  same(rest, await boxes(slots))
  await expect(fold).toHaveAccessibleName('Collapse all groups')
  // Keyboard focus in the cell shows them too.
  await page.mouse.move(700, 600)
  await head(page, 'title').locator('.th-sort').focus()
  await page.keyboard.press('Shift+Tab')
  await expect(keyHead(page).locator(':focus-visible')).toHaveCount(1)
  expect(await visibility(fold)).toBe('visible')
  same(rest, await boxes(slots))
  // An active selection keeps them out.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
  await rows(page).first().locator('.row-check').check()
  await page.mouse.move(700, 600)
  expect(await visibility(check)).toBe('visible')
  expect(await visibility(key)).toBe('hidden')
  same(rest, await boxes(slots))
})

test('AEON-1388 sorting by key stays reachable: KEY at rest shows its arrow, and the slot control sorts the same way', async ({ page }) => {
  const { calls } = await open(page)
  const lastSort = () => calls.filter(call => call.path === '/api/nodes' && call.query.get('limit') === '200').at(-1)?.query.get('sort')
  // At rest the KEY label is the sort control (a click without hover) …
  await keyHead(page).locator('.key-rest').dispatchEvent('click')
  await expect(page).toHaveURL(/sort=key/)
  await expect.poll(lastSort).toBe('state,key,-updated_at')
  await page.mouse.move(700, 600)
  await expect(keyHead(page).locator('.key-rest .sort-mark svg')).toHaveCount(1)
  expect(await visibility(keyHead(page).locator('.key-rest'))).toBe('visible')
  // … and on hover the slot control continues the same cycle: descending, then off.
  await keyHead(page).hover()
  await keyHead(page).locator('.key-icon').click()
  await expect(head(page, 'key')).toHaveAttribute('aria-sort', 'descending')
  await expect(keyHead(page).locator('.key-icon')).toHaveClass(/on/)
  await keyHead(page).locator('.key-icon').click()
  await expect(head(page, 'key')).toHaveAttribute('aria-sort', 'none')
  await expect.poll(lastSort).toBe('state,-updated_at')
})

test('AEON-1388 an ungrouped list keeps the checkbox above the row checkboxes and the sort control, without a chevron', async ({ page }) => {
  await open(page, { query: '' })
  await expect(keyHead(page).locator('.key-fold')).toHaveCount(0)
  await page.mouse.move(700, 600)
  const rest = await boxes({ check: keyHead(page).locator('.key-check'), sort: keyHead(page).locator('.key-icon') })
  const rowCheck = await boxOf(rows(page).first().locator('.row-check'))
  expect(Math.abs(rest.check.x + rest.check.width / 2 - (rowCheck.x + rowCheck.width / 2))).toBeLessThanOrEqual(.5)
  await keyHead(page).hover({ position: { x: (await boxOf(keyHead(page))).width - 14, y: 10 } })
  expect(await visibility(keyHead(page).locator('.key-icon'))).toBe('visible')
  same(rest, await boxes({ check: keyHead(page).locator('.key-check'), sort: keyHead(page).locator('.key-icon') }))
  await keyHead(page).locator('.key-icon').click()
  await page.mouse.move(700, 600)
  await expect(head(page, 'key')).toHaveAttribute('aria-sort', 'ascending')
})

test.describe('coarse pointer', () => {
  test.use({ hasTouch: true, isMobile: true })
  test('AEON-1388 a coarse pointer shows the first cell’s controls, 44 px each, above the group rows’ own', async ({ page }) => {
    await open(page, { width: 1024 })
    const fold = keyHead(page).locator('.key-fold'), check = keyHead(page).locator('.key-check'), sort = keyHead(page).locator('.key-icon')
    expect(await Promise.all([visibility(fold), visibility(check), visibility(sort), visibility(keyHead(page).locator('.key-rest'))])).toEqual(['visible', 'visible', 'visible', 'hidden'])
    for (const control of [fold, check, sort]) expect((await boxOf(control)).width).toBeGreaterThanOrEqual(44)
    expect(Math.abs((await boxOf(fold)).x - (await boxOf(groupRow(page, 'New').locator('.group-toggle'))).x)).toBeLessThanOrEqual(.5)
    // Each control stays inside the header row, clear of the first group row's targets.
    const header = await boxOf(keyHead(page)), group = await boxOf(groupRow(page, 'New'))
    for (const control of [fold, check, sort]) { const box = await boxOf(control); expect(box.y).toBeGreaterThanOrEqual(header.y - .5); expect(box.y + box.height).toBeLessThanOrEqual(group.y + .5) }
  })
})

test('AEON-1388 collapse and expand all: the first cell, Alt-click on a chevron and Display; folds survive a reload of the view', async ({ page }) => {
  await open(page)
  const fold = keyHead(page).locator('.key-fold')
  await keyHead(page).hover()
  await fold.click()
  await expect(rows(page)).toHaveCount(0)
  await expect(fold).toHaveAccessibleName('Expand all groups')
  await page.reload()
  await expect(labels(page)).toHaveText(['New', 'Backlog', 'In progress', 'QA'])
  await expect(rows(page)).toHaveCount(0)
  // Alt-click on any group chevron opens (or folds) every group, as in Finder.
  await groupRow(page, 'QA').locator('.group-toggle').click({ modifiers: ['Alt'] })
  await expect(rows(page)).toHaveCount(8)
  await groupRow(page, 'New').locator('.group-toggle').click({ modifiers: ['Alt'] })
  await expect(rows(page)).toHaveCount(0)
  // Display › Group by has the same pair.
  await page.getByRole('button', { name: 'Grouped by status' }).click()
  await display(page).getByRole('button', { name: 'Expand all', exact: true }).click()
  await expect(rows(page)).toHaveCount(8)
  await display(page).getByRole('button', { name: 'Collapse all', exact: true }).click()
  await expect(rows(page)).toHaveCount(0)
})

test('AEON-1390 the grouped-by column flips the group order; other columns sort inside the groups; both arrows hold their slots', async ({ page }) => {
  const { calls } = await open(page, { query: '?group=priority' })
  const lastSort = () => calls.filter(call => call.path === '/api/nodes' && call.query.get('limit') === '200').at(-1)?.query.get('sort')
  await expect(labels(page)).toHaveText(['High', 'Medium', 'Low', 'No priority'])
  const priority = head(page, 'priority').locator('.th-sort'), estimate = head(page, 'estimate').locator('.th-sort')
  const watch = { priority, estimate, status: head(page, 'status').locator('.th-sort'), key: keyHead(page) }
  const before = await boxes(watch)
  await expect(priority.locator('.sort-mark.groups .group-mark')).toHaveCount(1)
  await priority.hover()
  await expect(hint(page)).toHaveText('Groups: high to low')
  // The grouped-by column reverses the groups; "No priority" stays last. It never goes to unsorted.
  await priority.click()
  await expect(page).toHaveURL(/group_order=reversed/)
  await expect(labels(page)).toHaveText(['Low', 'Medium', 'High', 'No priority'])
  await expect(hint(page)).toHaveText('Groups: reversed')
  await expect.poll(lastSort).toBe('-priority,-updated_at')
  await priority.click()
  await expect(labels(page)).toHaveText(['High', 'Medium', 'Low', 'No priority'])
  await priority.click()
  // Another column sorts the tickets inside each group; the group order stays.
  await estimate.click()
  await estimate.click()
  await expect(labels(page)).toHaveText(['Low', 'Medium', 'High', 'No priority'])
  await expect.poll(lastSort).toBe('-priority,-estimate,-updated_at')
  await expect(groupRow(page, 'High').locator('xpath=following-sibling::tr[contains(@class,"ticket-row")]').first().locator('.key')).toHaveText('PHAROS-11')
  // Both arrows show at once, each in its own column's slot; clicking moved nothing.
  await expect(priority.locator('.sort-mark svg')).toHaveCount(2)
  await expect(estimate.locator('.sort-mark svg')).toHaveCount(1)
  await page.mouse.move(5, 5)
  await estimate.hover()
  await expect(hint(page)).toContainText('Tickets in each group: highest estimate first')
  same(before, await boxes(watch))
  // The same for status and assignee: Unassigned stays last when reversed.
  await page.goto('/p/PHAROS?group=assignee&group_order=reversed')
  await expect(labels(page)).toHaveText(['Mira Holm', 'Markus Barta', 'Unassigned'])
  await page.goto('/p/PHAROS?group=status')
  await head(page, 'status').locator('.th-sort').click()
  await expect(labels(page)).toHaveText(['QA', 'In progress', 'Backlog', 'New'])
})

test('AEON-1390 Display › Groups matches the header and a saved view keeps the group order across a reload', async ({ page }) => {
  const d = data()
  d.views.push(mockView({ id: '00000000-0000-4000-8000-000000001390', name: 'By priority, low first', project_id: 'p-pharos', filters: { group_order: 'reversed' }, group_by: 'priority', columns: COLUMNS }))
  await open(page, { data: d, query: '?group=priority' })
  await expect(labels(page)).toHaveText(['High', 'Medium', 'Low', 'No priority'])
  // Opening the saved view brings its group order with its grouping, and a reload keeps it.
  const tab = page.getByRole('link', { name: 'By priority, low first' })
  await tab.click()
  await expect(tab).toHaveAttribute('aria-current', 'page')
  await expect(labels(page)).toHaveText(['Low', 'Medium', 'High', 'No priority'])
  await page.reload()
  await expect(labels(page)).toHaveText(['Low', 'Medium', 'High', 'No priority'])
  await expect(page.getByRole('img', { name: 'changed since saved' }).first()).toBeHidden()
  // The Display item says what the header shows, and changes it.
  await page.getByRole('button', { name: 'Grouped by priority' }).click()
  const groups = display(page).getByRole('radiogroup', { name: 'Groups' })
  await expect(groups.getByRole('radio', { name: 'Reversed' })).toHaveAttribute('aria-checked', 'true')
  await groups.getByRole('radio', { name: 'Natural order' }).click()
  await expect(labels(page)).toHaveText(['High', 'Medium', 'Low', 'No priority'])
  await expect(groups.getByRole('radio', { name: 'Natural order' })).toHaveAttribute('aria-checked', 'true')
})

test('AEON-1388 phones: one muted line after the name, at most three facts in order, never wrapped; Display orders the groups', async ({ page }) => {
  await open(page, { width: 390 })
  const line = groupRow(page, 'In progress').locator('.group-facts')
  await expect(line).toHaveText('· 2 working · 2 stale · ~7.5h')
  const facts = await line.evaluate(el => el.textContent!.split('·').map(part => part.trim()).filter(Boolean))
  expect(facts.length).toBeLessThanOrEqual(3)
  const lineBox = await boxOf(line), label = await boxOf(groupRow(page, 'In progress').locator('.group-label'))
  expect(Math.abs(lineBox.y + lineBox.height / 2 - (label.y + label.height / 2)), 'the facts share the name’s line').toBeLessThanOrEqual(2)
  expect(await line.evaluate(el => el.getClientRects().length)).toBe(1)
  await expect(grid(page).locator('.group-row td.group-fig')).toHaveCount(0)
  // No column headers on a phone: Filters › Display settings… › Groups is the way to order the groups.
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  await page.getByRole('dialog', { name: 'Filters', exact: true }).getByRole('button', { name: 'Display settings…', exact: true }).click()
  await display(page).getByRole('radiogroup', { name: 'Groups' }).getByRole('radio', { name: 'Reversed' }).click()
  await expect(labels(page)).toHaveText(['QA', 'In progress', 'Backlog', 'New'])
})

for (const width of [1440, 1024, 390]) {
  test(`AEON-1388 grouped list at ${width} px, light and dark: figures or the phone line, still on hover`, async ({ page }, testInfo) => {
    for (const theme of ['light', 'dark'] as const) {
      const { errors } = await open(page, { width, theme })
      if (width > 720) {
        await expect(cell(page, 'In progress', 'estimate')).toHaveText('~7.5h')
        const watch = { head: keyHead(page), row: groupRow(page, 'In progress'), estimate: cell(page, 'In progress', 'estimate'), first: rows(page).first() }
        await page.mouse.move(5, 5)
        const before = await boxes(watch)
        await keyHead(page).hover()
        await groupRow(page, 'In progress').hover()
        same(before, await boxes(watch))
      } else {
        await expect(groupRow(page, 'In progress').locator('.group-facts')).toHaveText('· 2 working · 2 stale · ~7.5h')
      }
      await page.mouse.move(5, 5)
      await page.screenshot({ path: testInfo.outputPath(`aeon-1388-${width}-${theme}.png`), fullPage: false })
      expect(errors).toEqual([])
    }
  })
}
