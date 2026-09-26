// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Call, type MockNode } from './work-fixtures'

// setSystemTime lets time flow (setFixedTime would freeze Vue's event timestamps).
test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const outline = (page: Page) => page.getByRole('treegrid', { name: 'Ticket outline' })
const ticketViews = (page: Page) => page.getByRole('tablist', { name: 'Ticket views' })
const row = (page: Page, key: string) => outline(page).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const keys = (page: Page) => outline(page).locator('tr.ticket-row:not(.ghost) .key')
const ago = (h: number) => new Date(Date.parse('2026-09-23T12:00:00Z') - h * 3_600_000).toISOString()

// The shared fixtures plus a finished epic that still holds open work, and a ticket with tasks.
function tree() {
  const data = fixtures()
  const add = (node: Partial<MockNode> & Pick<MockNode, 'id' | 'key' | 'kind_slug' | 'title' | 'state'>) => data.nodes.push({ body: '', fields: {}, parent_id: 'p-pharos', project: 'p-pharos', created_at: ago(500), updated_at: ago(8), ...node })
  add({ id: 'n-epic-2', key: 'PHAROS-30', kind_slug: 'epic', title: 'Fleet clocks', state: 'done', updated_at: ago(90) })
  add({ id: 'n-7', key: 'PHAROS-31', kind_slug: 'ticket', title: 'Drift alarm for fleet clocks', state: 'backlog', parent_id: 'n-epic-2', fields: { priority: 'high' } })
  add({ id: 'n-8', key: 'PHAROS-32', kind_slug: 'ticket', title: 'Clock source picker', state: 'done', parent_id: 'n-epic-2' })
  return data
}
const lists = (calls: Call[]) => calls.filter(call => call.path === '/api/nodes' && call.method === 'GET')

test('the List | Outline switch keeps the view in the URL and the same toolbar', async ({ page }) => {
  const errors = watchErrors(page)
  await mockWork(page, tree())
  await page.goto('/p/PHAROS')
  await ticketViews(page).getByRole('tab', { name: 'Outline' }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=outline')
  await expect(outline(page)).toBeVisible()
  // Epics first (open ones and closed ones that still hold open work), then "No epic".
  await expect(keys(page)).toHaveText(['PHAROS-10', 'PHAROS-30', 'PHAROS-14'])
  await expect(outline(page).locator('.outline-group')).toContainText('No epic')
  await expect(row(page, 'PHAROS-30')).toHaveClass(/dimmed/)
  await expect(row(page, 'PHAROS-10')).not.toHaveClass(/dimmed/)
  // Filters keep the view; Group by does not apply.
  await page.getByRole('toolbar').getByRole('button', { name: 'Priority', exact: true }).click()
  await page.getByRole('dialog', { name: 'Filter by Priority' }).getByText('High', { exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(/view=outline/)
  await expect(page).toHaveURL(/priority=high/)
  await page.getByRole('button', { name: 'Display' }).click()
  await expect(page.getByRole('dialog', { name: 'Display options' }).getByRole('radio', { name: 'Status' })).toHaveCount(0)
  await expect(page.getByRole('dialog', { name: 'Display options' }).getByRole('button', { name: 'Expand all' })).toBeVisible()
  await page.keyboard.press('Escape')
  await ticketViews(page).getByRole('tab', { name: 'List' }).click()
  await expect(page).toHaveURL(url => url.pathname === '/p/PHAROS/tickets' && url.searchParams.get('view') === 'list' && url.searchParams.get('priority') === 'high')
  await expect(page.getByRole('grid', { name: 'Tickets' })).toBeVisible()
  expect(errors).toEqual([])
})

test('epics show subtree progress; children indent under their parents with guide lines', async ({ page }) => {
  const calls = await mockWork(page, tree())
  await page.goto('/p/PHAROS?view=outline&closed=1')
  await expect(row(page, 'PHAROS-10')).toBeVisible()
  // With closed work shown the outline loads lazily: epics and loose work, then children on demand.
  const roots = lists(calls).filter(call => call.query.get('parent_id') === 'p-pharos')
  expect(roots.map(call => call.query.get('kind')).sort()).toEqual(['epic', 'ticket,task'])
  await expect(row(page, 'PHAROS-10').locator('.epic-progress')).toHaveText('0/3')
  await expect(row(page, 'PHAROS-30').locator('.epic-progress')).toHaveText('1/2')
  // Chevrons only where there are children.
  await expect(row(page, 'PHAROS-14').locator('.twisty')).toHaveCount(0)
  await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
  await expect(row(page, 'PHAROS-11')).toHaveAttribute('aria-level', '2')
  expect(lists(calls).some(call => call.query.get('parent_id') === 'n-epic')).toBe(true)
  await row(page, 'PHAROS-12').getByRole('button', { name: 'Expand PHAROS-12' }).click()
  await expect(row(page, 'PHAROS-13')).toHaveAttribute('aria-level', '3')
  // Guide lines: PHAROS-12 is the epic's last child, PHAROS-13 its only task.
  await expect(row(page, 'PHAROS-12').locator('.guide')).toHaveClass(['guide elbow last'])
  await expect(row(page, 'PHAROS-13').locator('.guide')).toHaveClass(['guide none', 'guide elbow last'])
  await expect(row(page, 'PHAROS-11').locator('.guide')).toHaveClass(['guide elbow'])
  // Same columns and cells as the list.
  await expect(row(page, 'PHAROS-11').locator('.status-btn')).toHaveText('In progress')
  await expect(outline(page).getByRole('columnheader')).toHaveText(['Key', 'Title', 'Status', 'Priority', 'Assignee', 'Updated'])
})

test('children show skeleton rows while they load', async ({ page }) => {
  await mockWork(page, tree(), { delayChildren: 1500 })
  await page.goto('/p/PHAROS?view=outline&closed=1')
  await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
  await expect(outline(page).locator('tr.ghost.tree-row')).toHaveCount(2)
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  await expect(outline(page).locator('tr.ghost.tree-row')).toHaveCount(0)
})

test('search shows matches with their ancestors, dimmed and opened, and counts matches', async ({ page }) => {
  await mockWork(page, tree())
  await page.goto('/p/PHAROS?view=outline&q=hetzner')
  await expect(keys(page)).toHaveText(['PHAROS-10', 'PHAROS-11', 'PHAROS-12', 'PHAROS-13'])
  await expect(row(page, 'PHAROS-10')).toHaveClass(/dimmed/)
  await expect(row(page, 'PHAROS-12')).toHaveClass(/dimmed/)
  await expect(row(page, 'PHAROS-11')).not.toHaveClass(/dimmed/)
  await expect(row(page, 'PHAROS-13')).toHaveAttribute('aria-level', '3')
  await expect(page.getByRole('toolbar').getByText('2 tickets')).toBeVisible()
  await expect(outline(page).locator('mark')).toHaveText(['Hetzner', 'Hetzner'])
  await row(page, 'PHAROS-12').getByRole('button', { name: 'Collapse PHAROS-12' }).click()
  await expect(row(page, 'PHAROS-13')).toHaveCount(0)
})

test('keyboard: arrows open and close, step in and out; Space toggles; Enter opens', async ({ page }) => {
  await mockWork(page, tree())
  await page.goto('/p/PHAROS?view=outline&closed=1')
  await expect(row(page, 'PHAROS-10')).toBeVisible()
  await page.keyboard.press('j')
  await expect(row(page, 'PHAROS-10')).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('ArrowRight')
  await expect(row(page, 'PHAROS-10')).toHaveAttribute('aria-expanded', 'true')
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  await page.keyboard.press('ArrowRight')
  await expect(row(page, 'PHAROS-11')).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('j')
  await expect(row(page, 'PHAROS-12')).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press(' ')
  await expect(row(page, 'PHAROS-13')).toBeVisible()
  await page.keyboard.press(' ')
  await expect(row(page, 'PHAROS-13')).toHaveCount(0)
  await page.keyboard.press('ArrowLeft')
  await expect(row(page, 'PHAROS-10')).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('ArrowLeft')
  await expect(row(page, 'PHAROS-10')).toHaveAttribute('aria-expanded', 'false')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-10?view=outline&closed=1')
  await expect(page.getByRole('complementary', { name: 'Ticket details' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=outline&closed=1')
  await page.keyboard.press('Shift+?')
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toContainText('Expand, or step into the first child')
})

test('n on an epic creates tickets inside it and stays open for the next', async ({ page }) => {
  const calls = await mockWork(page, tree())
  await page.goto('/p/PHAROS?view=outline')
  await expect(row(page, 'PHAROS-10')).toBeVisible()
  await page.keyboard.press('j')
  await page.keyboard.press('n')
  const input = page.getByLabel('New ticket title')
  await expect(input).toBeFocused()
  await expect(input).toHaveAttribute('placeholder', 'Ticket in PHAROS-10')
  await expect(page.getByRole('button', { name: /^Epic: Guarded multi-cloud provisioning/ })).toBeVisible()
  await input.fill('Scaleway connector')
  await page.keyboard.press('Enter')
  await expect(input).toHaveValue('')
  await expect(input).toBeFocused()
  const post = calls.find(call => call.method === 'POST' && call.path === '/api/nodes')!
  expect((post.body as { parent_id: string }).parent_id).toBe('n-epic')
  await expect(outline(page).locator('tr.ticket-row').filter({ hasText: 'Scaleway connector' })).toHaveAttribute('aria-level', '2')
  await page.keyboard.press('Escape')
  await expect(input).toHaveCount(0)
})

test('dragging a ticket onto an epic moves it there, guarded against newer copies', async ({ page }) => {
  const calls = await mockWork(page, tree())
  await page.goto('/p/PHAROS?view=outline')
  await expect(row(page, 'PHAROS-14')).toBeVisible()
  await expect(row(page, 'PHAROS-14')).toHaveAttribute('draggable', 'true')
  await expect(row(page, 'PHAROS-10')).not.toHaveAttribute('draggable', 'true')
  await row(page, 'PHAROS-14').dragTo(row(page, 'PHAROS-10'))
  await expect(page.getByText('PHAROS-14 moved to PHAROS-10 Guarded multi-cloud provisioning')).toBeVisible()
  const move = calls.find(call => call.path.endsWith('/move'))!
  expect(move.body).toEqual({ parent_id: 'n-epic', before_id: null })
  expect(move.headers['if-unmodified-since']).toBe(ago(12))
  expect(calls.filter(call => call.path === '/api/nodes/n-4' && call.method === 'GET')).toHaveLength(0)
  // The ticket sits inside the epic now, which opened to show it.
  await expect(row(page, 'PHAROS-14')).toHaveAttribute('aria-level', '2')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('PHAROS-14 moved out of its epic')).toBeVisible()
  await expect(row(page, 'PHAROS-14')).toHaveAttribute('aria-level', '1')
})

test('a drag onto an epic does not move a ticket that changed elsewhere', async ({ page }) => {
  const data = tree()
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS?view=outline')
  await expect(row(page, 'PHAROS-14')).toBeVisible()
  const ticket = data.nodes.find(node => node.id === 'n-4')!
  ticket.updated_at = new Date().toISOString(); ticket.title = 'Visual acceptance, renamed by Mira'
  await row(page, 'PHAROS-14').dragTo(row(page, 'PHAROS-10'))
  await expect(page.getByText('PHAROS-14 was changed elsewhere, so it was not moved.')).toBeVisible()
  // One guarded move, answered 412 with the current node: no extra read, nothing moved.
  expect(calls.filter(call => call.path.endsWith('/move'))).toHaveLength(1)
  expect(calls.filter(call => call.path === '/api/nodes/n-4' && call.method === 'GET')).toHaveLength(0)
  await expect(row(page, 'PHAROS-14')).toContainText('renamed by Mira')
  await expect(row(page, 'PHAROS-14')).toHaveAttribute('aria-level', '1')
  expect(ticket.parent_id).toBe('p-pharos')
})

test('docked, the toolbar keeps a labelled Closed toggle and epic counts stay readable on hover', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const calls = await mockWork(page, tree())
  await page.goto('/p/PHAROS/PHAROS-14?view=outline')
  const pill = page.getByRole('button', { name: 'Hide closed tickets' })
  await expect(pill).toBeVisible()
  await expect(pill).toHaveText('Closed')
  await expect(pill).toHaveAttribute('aria-pressed', 'true')
  await expect(ticketViews(page).getByRole('tab', { name: 'Outline' })).toHaveAttribute('aria-selected', 'true')
  const toolbar = (await page.getByRole('toolbar', { name: 'Ticket list controls' }).boundingBox())!
  expect(toolbar.height).toBeLessThan(60)
  await row(page, 'PHAROS-10').hover()
  const count = row(page, 'PHAROS-10').locator('.epic-progress .mono')
  const actions = (await row(page, 'PHAROS-10').locator('.row-actions').boundingBox())!
  const numbers = (await count.boundingBox())!
  expect(numbers.x + numbers.width).toBeLessThanOrEqual(actions.x)
  await pill.click()
  await expect(page).toHaveURL(/closed=1/)
  await expect(page.getByRole('button', { name: 'Hide closed tickets' })).toHaveAttribute('aria-pressed', 'false')
  void calls
})

test('a 412 without a node body still settles as a conflict with the current copy', async ({ page }) => {
  await mockWork(page, tree())
  // Defensive path: older servers answer 412 with only an error; the row is then re-read.
  const sent: string[] = []
  await page.route('**/api/nodes/*/move', route => { sent.push(route.request().headers()['if-unmodified-since'] ?? ''); return route.fulfill({ status: 412, json: { error: 'node has changed' } }) })
  await page.goto('/p/PHAROS?view=outline')
  await row(page, 'PHAROS-14').dragTo(row(page, 'PHAROS-10'))
  await expect(page.getByText('PHAROS-14 was changed elsewhere, so it was not moved.')).toBeVisible()
  expect(sent).toEqual([ago(12)])
  await expect(row(page, 'PHAROS-14')).toHaveAttribute('aria-level', '1')
})

test('Expand all and Collapse all from the Display menu', async ({ page }) => {
  await mockWork(page, tree())
  await page.goto('/p/PHAROS?view=outline&closed=1')
  await expect(row(page, 'PHAROS-10')).toBeVisible()
  await page.getByRole('button', { name: 'Display' }).click()
  await page.getByRole('button', { name: 'Expand all' }).click()
  await expect(row(page, 'PHAROS-13')).toBeVisible()
  await expect(row(page, 'PHAROS-31')).toBeVisible()
  await page.getByRole('button', { name: 'Display' }).click()
  await page.getByRole('button', { name: 'Collapse all' }).click()
  await expect(row(page, 'PHAROS-11')).toHaveCount(0)
  await expect(row(page, 'PHAROS-31')).toHaveCount(0)
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`on a phone outline rows are two lines; the chevron expands and the row opens in ${colorScheme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await mockWork(page, tree())
    await page.goto('/p/PHAROS?view=outline&closed=1')
    const epic = row(page, 'PHAROS-10')
    await expect(epic).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await epic.getByRole('button', { name: 'Expand PHAROS-10' }).click()
    await expect(page).toHaveURL('/p/PHAROS/tickets?view=outline&closed=1')
    const child = row(page, 'PHAROS-11')
    const key = (await child.locator('.c-key').boundingBox())!, title = (await child.locator('.title-link').boundingBox())!
    expect(title.y).toBeGreaterThan(key.y + key.height - 1)
    expect(await child.evaluate(el => getComputedStyle(el).paddingLeft)).toBe('24px')
    await child.locator('.title-link').click()
    await expect(page.getByRole('complementary', { name: 'Ticket details' })).toBeVisible()
    expect(errors).toEqual([])
  })
}
