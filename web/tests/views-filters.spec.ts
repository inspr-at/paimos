// SPDX-License-Identifier: AGPL-3.0-only
// U22 (AEON-128): filters with exclusions, labels, epics, cost units, releases and
// dates; saved views (save, rename, duplicate, default, share, delete with undo,
// palette); grouping and multi-sort; view column sets; the persisted row height.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockView, mockWork, watchErrors, type Call, type Fixtures } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const rows = (page: Page) => grid(page).locator('tr.ticket-row:not(.ghost)')
const keys = (page: Page) => rows(page).locator('.key')
const lastList = (calls: Call[]) => calls.filter(call => call.path === '/api/nodes' && call.query.get('within') && call.query.get('limit') === '200').at(-1)!
const toolbar = (page: Page) => page.getByRole('toolbar', { name: 'Ticket list controls' })
const bar = (page: Page) => page.getByRole('navigation', { name: 'Saved views' })
// Active filter controls keep their accessible names when chips are folded
// into the toolbar. Verify the selection rather than its former container.
const selectedFilter = (page: Page, name: string) => toolbar(page).getByRole('button', { name: new RegExp(`^Edit ${name} filter:`) })
const priorityFilter = (page: Page) => toolbar(page).locator('button[data-dim="priority"]')
const MINE = '11111111-aaaa-4aaa-8aaa-000000000001'
const SHARED = '11111111-aaaa-4aaa-8aaa-000000000002'
const mira = '22222222-2222-4222-8222-222222222222'

function world(): Fixtures {
  const data = fixtures()
  const n5 = data.nodes.find(n => n.id === 'n-5')!
  n5.fields.cost_unit = { label: 'Support' }
  data.nodes.find(n => n.id === 'n-2')!.fields.classic = { cost_unit: { label: 'Development' } }
  data.nodes.find(n => n.id === 'n-4')!.fields.tags = ['docs']
  return data
}

test('an excluded value filters with "not", shows in its control and travels in the URL and the API', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await mockWork(page, world())
  await page.goto('/p/PHAROS?closed=1')
  await expect(rows(page)).toHaveCount(7)
  await toolbar(page).getByRole('button', { name: 'Status', exact: true }).click()
  const menu = page.getByRole('dialog', { name: 'Filter by Status' })
  // The minus key on a focused option excludes it.
  await menu.getByRole('checkbox', { name: /Done/ }).focus()
  await page.keyboard.press('-')
  await expect(page).toHaveURL(/status=!done/)
  await expect.poll(() => lastList(calls).query.get('state')).toBe('!done')
  await expect(rows(page)).toHaveCount(6)
  await expect(menu.locator('.facet-option.out')).toContainText('Done')
  // The Exclude button does the same for a second value; clicking the box includes instead.
  await menu.locator('.facet-option').filter({ hasText: 'Cancelled' }).getByRole('button', { name: 'Exclude Cancelled' }).click()
  await expect(rows(page)).toHaveCount(5)
  await page.keyboard.press('Escape')
  const status = selectedFilter(page, 'Status')
  await expect(status).toHaveAccessibleName('Edit Status filter: not Done, Cancelled')
  await expect(status).toContainText('not Done, Cancelled')
  // Backspace on the active filter removes it.
  await status.focus()
  await page.keyboard.press('Backspace')
  await expect(page).not.toHaveURL(/status=/)
  await expect(rows(page)).toHaveCount(7)
  expect(errors).toEqual([])
})

test('Shift F opens every filter: labels, parent, cost unit and release by name, and a relative date', async ({ page }) => {
  const calls = await mockWork(page, world())
  await page.goto('/p/PHAROS?closed=1')
  await expect(rows(page)).toHaveCount(7)
  await grid(page).focus()
  await page.keyboard.press('Shift+F')
  const more = page.getByRole('menu', { name: 'Filter by' })
  await expect(more).toBeVisible()
  await expect(more.getByRole('menuitem')).toContainText(['Status', 'Priority', 'Assignee', 'Parents / Leaves', 'Depth', 'Legacy type', 'Labels', 'Human check', 'Parent', 'Cost unit', 'Imported release', 'Date'])
  await more.getByRole('menuitem', { name: 'Labels' }).click()
  const labels = page.getByRole('dialog', { name: 'Filter by Labels' })
  // Label counts are asked for when the menu opens.
  await expect.poll(() => calls.some(call => call.path === '/api/nodes' && call.query.get('facets') === 'tag')).toBe(true)
  await expect(labels.locator('.facet-option').filter({ hasText: 'BUG' }).locator('.count')).toHaveText('1')
  await labels.getByText('BUG', { exact: true }).click()
  await expect(page).toHaveURL(/tag=BUG/)
  await expect.poll(() => lastList(calls).query.get('tag')).toBe('BUG')
  await expect(keys(page)).toHaveText(['PHAROS-13'])
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /Remove Labels filter/ }).click()
  // Epic: the project's epics by title; the list asks for the epic's id.
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: 'Parent' }).click()
  const epics = page.getByRole('dialog', { name: 'Filter by Parent' })
  await epics.getByText('Guarded multi-cloud provisioning').click()
  await expect.poll(() => lastList(calls).query.get('epic')).toBe('n-epic')
  await expect(keys(page)).toHaveText(['PHAROS-11', 'PHAROS-12', 'PHAROS-13'])
  await page.keyboard.press('Escape')
  await expect(selectedFilter(page, 'Parent')).toHaveAccessibleName('Edit Parent filter: Guarded multi-cloud provisioning')
  await expect(selectedFilter(page, 'Parent')).toContainText('Guarded multi-cloud provisioning')
  await page.getByRole('button', { name: /Remove Parent filter/ }).click()
  // Cost unit: native and imported labels alike.
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: 'Cost unit' }).click()
  const costs = page.getByRole('dialog', { name: 'Filter by Cost unit' })
  await costs.getByText('Development', { exact: true }).click()
  await costs.getByText('Support', { exact: true }).click()
  await expect.poll(() => lastList(calls).query.get('cost_unit')).toBe('Development,Support')
  await expect(keys(page)).toHaveText(['PHAROS-12', 'PHAROS-15'])
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /Remove Cost unit filter/ }).click()
  // Date: a preset keeps the field and sends local-day bounds.
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: 'Date' }).click()
  const date = page.getByRole('dialog', { name: 'Filter by date' })
  await date.getByRole('radio', { name: 'Updated' }).click()
  await date.getByRole('button', { name: /^Today/ }).click()
  await expect(page).toHaveURL(/date=updated:today/)
  await expect.poll(() => lastList(calls).query.get('date_field')).toBe('updated')
  const from = lastList(calls).query.get('date_from')!, to = lastList(calls).query.get('date_to')!
  expect(Date.parse(to) - Date.parse(from)).toBe(86_400_000)
  await expect(selectedFilter(page, 'date')).toHaveAccessibleName('Edit date filter: Updated today')
  await expect(selectedFilter(page, 'date')).toContainText('today')
  // Clear all takes every filter and the date.
  await page.getByRole('button', { name: /Remove date filter/ }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?closed=1&view=list')
})

test('a custom date range filters imported start dates by day', async ({ page }) => {
  const data = world()
  data.nodes.find(n => n.id === 'n-1')!.fields.start_date = '2026-09-10'
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: 'Date' }).click()
  const date = page.getByRole('dialog', { name: 'Filter by date' })
  await date.getByRole('radio', { name: 'Start' }).click()
  await date.getByLabel('From').fill('2026-09-01')
  await date.getByLabel('To').fill('2026-09-15')
  await date.getByRole('button', { name: 'Apply range' }).click()
  await expect(page).toHaveURL(/date=start:2026-09-01..2026-09-15/)
  await expect(keys(page)).toHaveText(['PHAROS-11'])
  expect(lastList(calls).query.get('date_field')).toBe('start')
  await expect(selectedFilter(page, 'date')).toHaveAccessibleName('Edit date filter: Start 1 Sep – 15 Sep')
  await expect(selectedFilter(page, 'date')).toContainText('1 Sep – 15 Sep')
})

test('saving a view: named from its filters, shared, then the bar shows it and changes mark it until saved', async ({ page }) => {
  const errors = watchErrors(page)
  const data = world()
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS?priority=high&group=status')
  await expect(rows(page)).toHaveCount(2)
  await bar(page).getByRole('button', { name: 'Save view' }).click()
  const panel = page.getByRole('dialog', { name: 'Save view' })
  await expect(panel.getByLabel('View name')).toHaveValue('High')
  await panel.getByLabel('View name').fill('Urgent')
  await panel.getByText('Share with the project').click()
  await panel.getByRole('button', { name: 'Save view' }).click()
  await expect(page.getByText('Saved the view “Urgent”, shared with the project')).toBeVisible()
  const created = calls.find(call => call.path === '/api/views' && call.method === 'POST')!
  expect(created.body).toMatchObject({ name: 'Urgent', project_id: 'p-pharos', shared: true, filters: { priority: 'high' }, group_by: 'status', sort_keys: [], columns: [] })
  const id = data.views[0].id
  await expect(page).toHaveURL(new RegExp(`v=${id}`))
  const tab = bar(page).getByRole('link', { name: /Urgent/ })
  await expect(tab).toHaveAttribute('aria-current', 'page')
  // A change marks the view; Save writes it, Reset goes back.
  await priorityFilter(page).click()
  await page.getByRole('dialog', { name: 'Filter by Priority' }).getByText('Medium', { exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(bar(page).locator('.dirty')).toBeVisible()
  await bar(page).getByRole('button', { name: 'Reset' }).click()
  await expect(bar(page).locator('.dirty')).toBeHidden()
  await expect(rows(page)).toHaveCount(2)
  await priorityFilter(page).click()
  await page.getByRole('dialog', { name: 'Filter by Priority' }).getByText('Medium', { exact: true }).click()
  await page.keyboard.press('Escape')
  await bar(page).getByRole('button', { name: 'Save changes to the view' }).click()
  await expect(page.getByText('Saved the changes to “Urgent”')).toBeVisible()
  await expect(bar(page).locator('.dirty')).toBeHidden()
  expect(calls.filter(call => call.method === 'PATCH' && call.path === `/api/views/${id}`).at(-1)!.body).toMatchObject({ filters: { priority: 'high,medium' } })
  // All tickets goes back to the plain list.
  await bar(page).getByRole('link', { name: 'All tickets' }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets')
  await expect(rows(page)).toHaveCount(5)
  expect(errors).toEqual([])
})

test('view menu: rename, duplicate, default on the next visit, share, copy link and delete with undo', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const data = world()
  data.views.push(
    mockView({ id: MINE, name: 'Mine', filters: { priority: 'high' } }),
    mockView({ id: SHARED, name: 'Team', owner_principal_id: mira, shared: true, filters: { status: 'backlog' } }),
  )
  const calls = await mockWork(page, data)
  await page.goto(`/p/PHAROS?priority=high&v=${MINE}`)
  await expect(rows(page)).toHaveCount(2)
  const options = (name: string) => bar(page).getByRole('button', { name: `Options for view ${name}`, exact: true })
  await options('Mine').click()
  let menu = page.getByRole('menu', { name: 'View Mine' })
  await menu.getByRole('menuitem', { name: 'Rename' }).click()
  await page.getByRole('dialog', { name: 'Rename view' }).getByLabel('View name').fill('High priority')
  await page.keyboard.press('Enter')
  await expect(bar(page).getByRole('link', { name: 'High priority' })).toBeVisible()
  // Default: the next visit to the project opens the view.
  await options('High priority').click()
  await page.getByRole('menu', { name: 'View High priority' }).getByRole('menuitem', { name: 'Open the project with it' }).click()
  await expect.poll(() => (data.preferences['list:p-pharos'] as { defaultView?: string } | undefined)?.defaultView).toBe(MINE)
  await page.goto('/p/PHAROS')
  await expect(page).toHaveURL(new RegExp(`v=${MINE}`))
  await expect(rows(page)).toHaveCount(2)
  // Only the first list request is the view's: the plain list never loads first.
  expect(calls.filter(call => call.path === '/api/nodes' && call.query.get('limit') === '200' && !call.query.get('priority'))).toHaveLength(0)
  // Copy link and share.
  await options('High priority').click()
  await page.getByRole('menu', { name: 'View High priority' }).getByRole('menuitem', { name: 'Copy link' }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(`v=${MINE}`)
  await options('High priority').click()
  await page.getByRole('menu', { name: 'View High priority' }).getByRole('menuitem', { name: 'Share with the project' }).click()
  await expect(page.getByText('“High priority” is shared with everyone in Pharos')).toBeVisible()
  expect(data.views.find(v => v.id === MINE)!.shared).toBe(true)
  // A shared view of someone else: copy it to my views.
  await bar(page).getByRole('link', { name: 'Team', exact: true }).click()
  // Both views contain two rows; the count alone cannot prove navigation. Wait
  // for Team to be current before resolving its options, rather than capturing
  // the old view's button while the router is still changing the selection.
  await expect(bar(page).getByRole('link', { name: 'Team', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(rows(page)).toHaveCount(2)
  await options('Team').click()
  menu = page.getByRole('menu', { name: 'View Team' })
  await expect(menu.getByRole('menuitem', { name: 'Rename' })).toHaveCount(0)
  await expect(menu.getByRole('menuitem', { name: 'Delete view' })).toHaveCount(0)
  await menu.getByRole('menuitem', { name: 'Copy to my views' }).click()
  await expect(bar(page).getByRole('link', { name: 'Team copy' })).toHaveAttribute('aria-current', 'page')
  // Delete, then Undo brings the view back with its id.
  await options('Team copy').click()
  await page.getByRole('menu', { name: 'View Team copy' }).getByRole('menuitem', { name: 'Delete view' }).click()
  await expect(bar(page).getByRole('link', { name: 'Team copy' })).toHaveCount(0)
  await expect(page).not.toHaveURL(/v=/)
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(bar(page).getByRole('link', { name: 'Team copy' })).toBeVisible()
  expect(calls.some(call => call.method === 'POST' && /\/restore$/.test(call.path))).toBe(true)
})

test('the palette finds a project’s views by name and opens them', async ({ page }) => {
  const data = world()
  data.views.push(mockView({ id: MINE, name: 'Backlog triage', filters: { status: 'backlog' } }))
  await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await page.keyboard.press('Control+k')
  const palette = page.getByRole('dialog', { name: 'Search and commands' })
  await expect(palette.getByRole('group', { name: 'Views' })).toContainText('Backlog triage')
  await page.keyboard.type('triage')
  await expect(palette.getByRole('option').first()).toContainText('Backlog triage')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`status=backlog.*v=${MINE}|v=${MINE}.*status=backlog`))
  await expect(bar(page).getByRole('link', { name: 'Backlog triage' })).toHaveAttribute('aria-current', 'page')
  await expect(rows(page)).toHaveCount(2)
})

test('grouping by assignee, priority and label counts its groups; groups collapse together', async ({ page }) => {
  const data = world()
  data.nodes.find(n => n.id === 'n-1')!.fields.tags = ['BUG', 'ops']
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await page.getByRole('button', { name: /^Display/ }).click()
  const display = page.getByRole('dialog', { name: 'Display options' })
  await display.getByRole('radio', { name: 'Assignee' }).click()
  await expect(page).toHaveURL(/group=assignee/)
  await expect.poll(() => lastList(calls).query.get('sort')).toBe('assignee,-updated_at')
  await expect(grid(page).locator('.group-row .group-label')).toHaveText(['Markus Barta', 'Mira Holm', 'Unassigned'])
  await expect(grid(page).locator('.group-row .group-count')).toHaveText(['1', '1', '3'])
  await display.getByRole('radio', { name: 'Priority' }).click()
  await expect(grid(page).locator('.group-row .group-label')).toHaveText(['High', 'Medium', 'Low', 'No priority'])
  // A ticket with two labels shows under each; keyboard order visits it once.
  await display.getByRole('radio', { name: 'Label' }).click()
  await expect(grid(page).locator('.group-row .group-label')).toHaveText(['BUG', 'docs', 'ops', 'No labels'])
  await expect(grid(page).locator('tr[id="row-n-1"]')).toHaveCount(1)
  await expect(rows(page).filter({ hasText: 'PHAROS-11' })).toHaveCount(2)
  await display.getByRole('button', { name: 'Collapse groups' }).click()
  await expect(rows(page)).toHaveCount(0)
  await display.getByRole('button', { name: 'Expand groups' }).click()
  await expect(rows(page)).toHaveCount(6)
})

test('the sort editor adds, reverses, reorders and removes keys, the same as Shift-clicking headers', async ({ page }) => {
  const calls = await mockWork(page, world())
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await page.getByRole('button', { name: /^Display/ }).click()
  const display = page.getByRole('dialog', { name: 'Display options' })
  await display.getByRole('button', { name: 'Sort by' }).click()
  await expect(display.getByRole('combobox', { name: 'Sort key 1' })).toBeFocused()
  await display.getByRole('combobox', { name: 'Sort key 1' }).selectOption('priority')
  await expect(page).toHaveURL(/[?&]sort=priority(?:&|$)/)
  await display.getByRole('button', { name: 'Then by' }).click()
  await display.getByRole('combobox', { name: 'Sort key 2' }).selectOption('assignee')
  await expect.poll(() => lastList(calls).query.get('sort')).toBe('priority,assignee,-updated_at')
  await display.getByRole('button', { name: /^Priority: ascending/ }).click()
  await expect(page).toHaveURL(/sort=-priority,assignee/)
  // Alt and the arrows move a key.
  await display.getByRole('combobox', { name: 'Sort key 2' }).focus()
  await page.keyboard.press('Alt+ArrowUp')
  await expect(page).toHaveURL(/sort=assignee,-priority/)
  await expect(page.getByRole('columnheader', { name: 'Assignee' }).locator('.sort-index')).toHaveText('1')
  await display.getByRole('button', { name: 'Remove Assignee from the sort' }).click()
  await expect(page).toHaveURL(/[?&]sort=-priority(?:&|$)/)
  await display.getByRole('button', { name: 'Default' }).click()
  await expect(page).not.toHaveURL(/sort=/)
})

test('in a view the column set belongs to the view; row height is the person’s own', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = world()
  data.views.push(mockView({ id: MINE, name: 'Lean', columns: ['status', 'updated'] }))
  const calls = await mockWork(page, data)
  await page.goto(`/p/PHAROS?cols=status,updated&v=${MINE}`)
  await expect(rows(page)).toHaveCount(5)
  await expect(grid(page).locator('thead th')).toHaveText(['Key', 'Title', 'Status', 'Updated'])
  await page.getByRole('button', { name: /^Display/ }).click()
  const display = page.getByRole('dialog', { name: 'Display options' })
  await display.getByRole('checkbox', { name: 'Cost unit' }).check()
  await expect(page).toHaveURL(/cols=status,updated,cost/)
  await expect(grid(page).locator('thead th')).toHaveText(['Key', 'Title', 'Status', 'Updated', 'Cost unit'])
  await expect(bar(page).locator('.dirty')).toBeVisible()
  // The person's own columns are untouched.
  expect(data.preferences['list:p-pharos']?.visible).toBeUndefined()
  const compact = display.getByRole('radiogroup', { name: 'Row height', exact: true }).getByRole('radio', { name: 'Compact', exact: true })
  await compact.click()
  await expect(compact).toHaveAttribute('aria-checked', 'true')
  await expect.poll(() => (data.preferences['list:display'] as { density?: string } | undefined)?.density).toBe('compact')
  await page.keyboard.press('Escape')
  await bar(page).getByRole('button', { name: 'Save changes to the view' }).click()
  await expect.poll(() => calls.filter(call => call.method === 'PATCH').at(-1)?.body).toMatchObject({ columns: ['status', 'updated', 'cost'] })
  await page.reload()
  await expect(rows(page).first()).toHaveCSS('height', '30px')
})

test('a link to a view someone cannot see keeps its filters and drops the view', async ({ page }) => {
  await mockWork(page, world())
  await page.goto(`/p/PHAROS?priority=high&v=${SHARED}`)
  await expect(rows(page)).toHaveCount(2)
  await expect(page).toHaveURL('/p/PHAROS/tickets?priority=high&view=list')
  await expect(bar(page).getByRole('link', { name: 'All tickets' })).toHaveAttribute('aria-current', 'page')
})

// Seven full-page scans per scheme get independent test budgets; combining both
// schemes timed out during the final scan on hosted CI, without an axe failure.
for (const colorScheme of ['light', 'dark'] as const) {
  test(`filters and views have no axe violations in ${colorScheme}`, async ({ page }) => {
    const data = world()
    data.views.push(mockView({ id: MINE, name: 'Mine', filters: { priority: 'high' } }), mockView({ id: SHARED, name: 'Team', owner_principal_id: mira, shared: true }))
    data.preferences['list:p-pharos'] = { defaultView: MINE }
    await mockWork(page, data)
    const scan = async () => {
      // Judge settled states only. A sort or filter change re-queries the list, and
      // until the answer arrives the previous rows stay on screen dimmed as stale
      // (tbody.dim, the grid aria-busy); scanning then measured the dimmed avatar
      // initials, a loading transition rather than the page. Popovers mount off
      // screen and are placed on the next frame.
      await expect(grid(page)).toHaveAttribute('aria-busy', 'false')
      await expect(grid(page).locator('tbody.dim')).toHaveCount(0)
      for (const pop of await page.locator('.floating').all()) await expect(pop).toBeInViewport()
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id}: ${v.help} ${v.nodes.slice(0, 3).map(n => n.target.join(' ')).join(' | ')}`)
      expect(summary, summary.join('\n')).toEqual([])
    }
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await page.goto(`/p/PHAROS?priority=high,!low&tag=!docs&date=updated:30d&v=${MINE}&status=!done`)
    await expect(rows(page).first()).toBeVisible()
    await scan()
    await page.getByRole('button', { name: 'Filter by more' }).click()
    await scan()
    await page.getByRole('menuitem', { name: 'Labels' }).click()
    await expect(page.getByRole('dialog', { name: 'Filter by Labels' }).locator('.facet-option').first()).toBeVisible()
    await scan()
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Filter by more' }).click()
    await page.getByRole('menuitem', { name: 'Date' }).click()
    await scan()
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: /^Display/ }).click()
    await page.getByRole('dialog', { name: 'Display options' }).getByRole('button', { name: 'Sort by' }).click()
    await scan()
    await page.keyboard.press('Escape')
    await bar(page).getByRole('button', { name: /^Options for view/ }).click()
    await scan()
    await page.getByRole('menuitem', { name: 'Save as new view' }).click()
    await expect(page.getByRole('dialog', { name: 'Save view' })).toBeVisible()
    await scan()
    await page.keyboard.press('Escape')
  })
}

for (const width of [1920, 1440, 1280, 1024, 390]) {
  test(`at ${width}px the view bar and toolbar never overflow the page`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const data = world()
    data.views.push(
      mockView({ id: MINE, name: 'My open work in this project', filters: { assignee: me.id } }),
      mockView({ id: SHARED, name: 'Bugs to fix before the release', owner_principal_id: mira, shared: true }),
      mockView({ id: '11111111-aaaa-4aaa-8aaa-000000000003', name: 'Release v4.8.0', shared: true }),
    )
    await mockWork(page, data)
    await page.goto(`/p/PHAROS?assignee=${me.id}&status=!done&v=${MINE}`)
    await expect(rows(page).first()).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    const main = page.locator('#main')
    expect(await main.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
    // The controls keep to one line on desktop widths; applied filters get their own.
    if (width >= 1024) {
      const viewsBox = await toolbar(page).getByRole('tablist', { name: 'Ticket views' }).boundingBox()
      const createBox = await toolbar(page).getByRole('button', { name: 'New work item', exact: true }).boundingBox()
      expect(Math.abs(viewsBox!.y - createBox!.y)).toBeLessThan(6)
    }
  })
}
