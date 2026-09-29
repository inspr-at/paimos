// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Call, type MockNode } from './work-fixtures'
import { openOne } from './live-server'

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

// Review 326g: neither a lazy root nor a child level may admit a page read
// before a gap. Hold the retry too, to check the deleted row never flashes.
for (const level of ['root', 'children'] as const) {
  test(`a lazy ${level} page crossing a stream gap is read again before it shows`, async ({ browser }) => {
    let releaseFirst!: () => void, releaseRetry!: () => void, releaseStream!: () => void
    const first = new Promise<void>(resolve => { releaseFirst = resolve })
    const retry = new Promise<void>(resolve => { releaseRetry = resolve })
    const stream = new Promise<void>(resolve => { releaseStream = resolve })
    const parent = level === 'root' ? 'p-pharos' : 'n-epic'
    const victim = level === 'root' ? 'n-4' : 'n-1'
    const key = level === 'root' ? 'PHAROS-14' : 'PHAROS-11'
    let requests = 0, computed = 0
    const h = await openOne(browser, '/p/PHAROS/tickets?view=outline&closed=1', {
      first: stream,
      hold: ({ method, path, query }) => {
        if (method !== 'GET' || path !== '/api/nodes' || query.get('parent_id') !== parent || query.get('kind') === 'epic') return
        const n = ++requests
        return { until: n === 1 ? first : retry, computed: () => { computed++ } }
      },
    })
    try {
      if (level === 'children') await row(h.page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
      await expect.poll(() => computed).toBe(1)
      h.data.nodes.splice(h.data.nodes.findIndex(node => node.id === victim), 1)
      releaseStream()
      await expect.poll(() => h.live.requests.length).toBeGreaterThan(1)
      releaseFirst()
      await expect.poll(() => computed).toBeGreaterThanOrEqual(2)
      await expect(row(h.page, key)).toHaveCount(0)
      releaseRetry()
      await expect(row(h.page, level === 'root' ? 'PHAROS-10' : 'PHAROS-12')).toBeVisible()
      await expect(outline(h.page).locator('tr.ghost.tree-row')).toHaveCount(0)
      await expect(row(h.page, key)).toHaveCount(0)
      expect(requests).toBeGreaterThanOrEqual(2)
      expect(h.errors).toEqual([])
    } finally { releaseFirst(); releaseRetry(); releaseStream(); await h.close() }
  })
}

test('a created child moved away remotely and back locally remains expandable after reload', async ({ browser }) => {
  const data = fixtures()
  data.nodes.push({ ...structuredClone(data.nodes.find(node => node.id === 'n-epic')!), id: 'empty', key: 'PHAROS-30', title: 'Empty epic' })
  const h = await openOne(browser, '/p/PHAROS/PHAROS-30?view=outline&closed=1', { data })
  const page = h.page, panel = page.getByRole('complementary', { name: 'Ticket details' })
  try {
    const children = panel.getByRole('region', { name: 'Tickets in this epic' })
    await children.getByRole('button', { name: 'Add ticket' }).click()
    await children.getByLabel('New ticket title').fill('Return this child')
    await page.keyboard.press('Enter')
    await expect(children.locator('.child-row')).toHaveCount(1)
    const child = data.nodes.find(node => node.title === 'Return this child')!
    Object.assign(child, { parent_id: 'n-epic', updated_at: new Date(Date.parse(child.updated_at) + 60_000).toISOString() })
    await ticketViews(page).getByRole('tab', { name: 'List', exact: true }).click()
    await expect(page.getByRole('grid', { name: 'Tickets' })).toBeVisible()
    await ticketViews(page).getByRole('tab', { name: 'Outline', exact: true }).click()
    await expect(row(page, 'PHAROS-30')).toBeVisible()
    await expect(row(page, 'PHAROS-30').locator('.twisty')).toHaveCount(0)
    await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
    await row(page, child.key).getByText(child.key, { exact: true }).click()
    await panel.getByRole('button', { name: 'More actions' }).click()
    await page.getByRole('menuitem', { name: 'Move to another epic…' }).click()
    await page.getByLabel('Find an epic').fill('Empty epic')
    await expect(page.getByRole('listbox', { name: 'Epics' }).getByRole('option')).toHaveCount(1)
    await page.keyboard.press('Enter')
    await expect(page.getByText(/moved to PHAROS-30 Empty epic/)).toBeVisible()
    await expect(row(page, 'PHAROS-30').getByRole('button', { name: 'Expand PHAROS-30' })).toBeVisible()
    await row(page, 'PHAROS-30').getByRole('button', { name: 'Expand PHAROS-30' }).click()
    await expect(row(page, child.key)).toBeVisible()
    expect(child.parent_id).toBe('empty')
    expect(h.errors).toEqual([])
  } finally { await h.close() }
})

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

// A persistent, controllable stream: a real loss can be observed before any
// reconnect, independent of the chunked SSE fixture used by the two-tab specs.
async function controlledStream(page: Page) {
  await page.addInitScript(() => {
    class Source {
      readyState = 1
      onerror: (() => void) | null = null
      listeners = new Map<string, ((event: unknown) => void)[]>()
      constructor() {
        ;(window as unknown as { outlineStream: Source }).outlineStream = this
        queueMicrotask(() => this.emit('stream.ready', { after: 500, resumed: false }))
      }
      addEventListener(type: string, listener: (event: unknown) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
      emit(type: string, data: unknown) { for (const listener of this.listeners.get(type) ?? []) listener({ data: JSON.stringify(data), lastEventId: '501' }) }
      fail() { this.readyState = 0; this.onerror?.() }
      close() { this.readyState = 2 }
    }
    window.EventSource = Source as unknown as typeof EventSource
  })
}
async function outlineEvent(page: Page, node: MockNode, fields: string[], change = 'updated') {
  await page.evaluate(({ node, fields, change }) => {
    const stream = (window as unknown as { outlineStream: { emit: (type: string, data: unknown) => void } }).outlineStream
    stream.emit(`node.${change}`, { id: 501, type: `node.${change}`, actor_principal_id: 'another-person', node_changes: [{ id: node.id, project_id: node.project, fields, change, revision: node.updated_at }] })
  }, { node, fields, change })
}
async function outlineGap(page: Page, loss = false) {
  await page.evaluate(loss => {
    const stream = (window as unknown as { outlineStream: { emit: (type: string, data: unknown) => void; fail: () => void } }).outlineStream
    if (loss) stream.fail()
    else stream.emit('stream.ready', { after: 900, resumed: false })
  }, loss)
}

for (const scheme of ['light', 'dark'] as const) {
  test(`AEON-385: live Outline patches fields and holds moves, additions and deletion until Show (${scheme})`, async ({ page }) => {
    await controlledStream(page)
    await page.emulateMedia({ colorScheme: scheme })
    const errors = watchErrors(page), data = fixtures()
    await mockWork(page, data)
    await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
    await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
    await expect(row(page, 'PHAROS-11')).toBeVisible()
    await row(page, 'PHAROS-14').getByRole('checkbox').check()
    const n = data.nodes.find(n => n.id === 'n-1')!
    Object.assign(n, { title: 'Hetzner title changed live', updated_at: ago(-1) })
    await outlineEvent(page, n, ['title'])
    await expect(row(page, n.key)).toContainText(n.title)
    await expect(row(page, n.key)).toHaveClass(/live-flash/)
    const before = await keys(page).allTextContents()
    Object.assign(n, { parent_id: 'p-pharos', updated_at: ago(-2) })
    await outlineEvent(page, n, ['parent_id'])
    const removed = data.nodes.find(n => n.id === 'n-2')!
    removed.updated_at = ago(-3)
    data.nodes.splice(data.nodes.indexOf(removed), 1)
    await outlineEvent(page, removed, ['deleted_at'], 'deleted')
    const added = { ...structuredClone(n), id: 'live-new', key: 'PHAROS-99', title: 'New work from another tab', updated_at: ago(-4) }
    data.nodes.push(added)
    await outlineEvent(page, added, [], 'created')
    await expect(row(page, n.key).locator('.live-label')).toHaveText('Moved')
    await expect(row(page, removed.key).locator('.live-label')).toHaveText('Deleted')
    await expect(page.getByRole('button', { name: '3 updates · Show' })).toBeVisible()
    await expect(keys(page)).toHaveText(before)
    await expect(row(page, n.key)).toHaveAttribute('aria-level', '2')
    await page.waitForTimeout(2200)
    await expect(keys(page)).toHaveText(before)
    if (process.env.OUTLINE_SHOTS_DIR) {
      const { mkdirSync } = await import('node:fs')
      mkdirSync(process.env.OUTLINE_SHOTS_DIR, { recursive: true })
      for (const width of [1600, 390]) {
        await page.setViewportSize({ width, height: 950 })
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
        await page.screenshot({ path: `${process.env.OUTLINE_SHOTS_DIR}/outline-live-${width}-${scheme}.png` })
      }
    }
    await page.getByRole('button', { name: '3 updates · Show' }).click()
    await expect(row(page, n.key)).toHaveAttribute('aria-level', '1')
    await expect(row(page, removed.key)).toHaveCount(0)
    await expect(row(page, added.key)).toBeVisible()
    expect(errors).toEqual([])
  })
}

test('AEON-385: filtered ancestor values and ancestry follow events without reloading', async ({ page }) => {
  await controlledStream(page)
  const data = fixtures(), errors = watchErrors(page)
  await mockWork(page, data)
  await page.goto('/p/PHAROS/tickets?view=outline&q=Hetzner')
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  await row(page, 'PHAROS-11').getByRole('checkbox').check()
  const epic = data.nodes.find(n => n.id === 'n-epic')!
  Object.assign(epic, { title: 'Ancestor renamed live', updated_at: ago(-1) })
  await outlineEvent(page, epic, ['title'])
  await expect(row(page, epic.key)).toContainText(epic.title)
  const ancestor = { ...structuredClone(epic), id: 'ancestor', key: 'PHAROS-98', title: 'New ancestor', parent_id: 'p-pharos' }
  data.nodes.push(ancestor)
  Object.assign(epic, { parent_id: ancestor.id, updated_at: ago(-2) })
  await outlineEvent(page, epic, ['parent_id'])
  await expect(row(page, epic.key).locator('.live-label')).toHaveText('Moved')
  await expect(row(page, 'PHAROS-11')).toHaveAttribute('aria-level', '2')
  await expect(row(page, ancestor.key)).toHaveCount(0)
  await page.getByRole('button', { name: '1 update · Show' }).click()
  await expect(row(page, ancestor.key)).toBeVisible()
  await expect(row(page, 'PHAROS-11')).toHaveAttribute('aria-level', '3')
  Object.assign(ancestor, { title: 'Fresh ancestor title', updated_at: ago(-3) })
  await outlineEvent(page, ancestor, ['title'])
  await expect(row(page, ancestor.key)).toContainText(ancestor.title)
  await expect(row(page, ancestor.key)).toHaveClass(/dimmed/)
  Object.assign(ancestor, { title: 'Hetzner ancestor', updated_at: ago(-4) })
  await outlineEvent(page, ancestor, ['title'])
  await expect(row(page, ancestor.key)).not.toHaveClass(/dimmed/)
  expect(errors).toEqual([])
})

test('AEON-385: a child page is distrusted at stream loss, before reconnect', async ({ page }) => {
  await controlledStream(page)
  const data = fixtures(), errors = watchErrors(page)
  let firstRelease!: () => void, retryRelease!: () => void, computed = 0, requests = 0
  const first = new Promise<void>(resolve => { firstRelease = resolve })
  const retry = new Promise<void>(resolve => { retryRelease = resolve })
  await mockWork(page, data, { hold: call => {
    if (call.method === 'GET' && call.path === '/api/nodes' && call.query.get('parent_id') === 'n-epic') {
      return { until: ++requests === 1 ? first : retry, computed: () => { computed++ } }
    }
  } })
  try {
    await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
    await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
    await expect.poll(() => computed).toBe(1)
    data.nodes.splice(data.nodes.findIndex(n => n.id === 'n-1'), 1)
    await outlineGap(page, true)
    firstRelease()
    await expect.poll(() => computed).toBeGreaterThanOrEqual(2)
    await expect(row(page, 'PHAROS-11')).toHaveCount(0)
    retryRelease()
    await expect(row(page, 'PHAROS-12')).toBeVisible()
    await expect(row(page, 'PHAROS-11')).toHaveCount(0)
    expect(errors).toEqual([])
  } finally { firstRelease(); retryRelease() }
})

test('AEON-385: an old parent page cannot restore the count after moving its last child', async ({ page }) => {
  await controlledStream(page)
  const data = fixtures(), errors = watchErrors(page)
  data.nodes = data.nodes.filter(n => !['n-2', 'n-3'].includes(n.id))
  let hold = false, computed = 0, release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  await mockWork(page, data, { hold: call => {
    if (hold && call.method === 'GET' && call.path === '/api/nodes' && call.query.get('kind') === 'epic' && call.query.get('parent_id') === 'p-pharos') return { until: wait, computed: () => { computed++ } }
  } })
  try {
    await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
    await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
    await expect(row(page, 'PHAROS-11')).toBeVisible()
    hold = true
    await outlineGap(page)
    await expect.poll(() => computed).toBe(1)
    await row(page, 'PHAROS-11').dragTo(outline(page).locator('.outline-group'))
    await expect(row(page, 'PHAROS-11')).toHaveAttribute('aria-level', '1')
    await expect(row(page, 'PHAROS-10').locator('.twisty')).toHaveCount(0)
    release()
    await expect.poll(() => page.locator('.live-pill').count()).toBe(0)
    await page.waitForTimeout(300)
    await expect(row(page, 'PHAROS-10').locator('.twisty')).toHaveCount(0)
    expect(errors).toEqual([])
  } finally { release() }
})

test('AEON-385: bulk move relocates lazy Outline rows immediately and sends displayed revisions', async ({ page }) => {
  const data = fixtures(), errors = watchErrors(page)
  const revisions = Object.fromEntries(data.nodes.filter(n => ['n-1', 'n-2'].includes(n.id)).map(n => [n.id, n.updated_at]))
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
  await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
  for (const key of ['PHAROS-11', 'PHAROS-12']) await row(page, key).getByRole('checkbox').check()
  await row(page, 'PHAROS-10').getByRole('button', { name: 'Collapse PHAROS-10' }).click()
  await page.getByRole('toolbar', { name: '2 selected tickets' }).getByRole('button', { name: 'Move' }).click()
  await page.getByRole('dialog', { name: 'Epic for 2 tickets' }).getByRole('option', { name: 'No epic' }).click()
  for (const key of ['PHAROS-11', 'PHAROS-12']) await expect(row(page, key)).toHaveAttribute('aria-level', '1')
  await expect(row(page, 'PHAROS-10').locator('.twisty')).toHaveCount(0)
  expect(calls.find(call => call.path === '/api/nodes/bulk')?.body).toMatchObject({ parent_id: 'p-pharos', if_unmodified_since: revisions })
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await row(page, 'PHAROS-10').getByRole('button', { name: 'Expand PHAROS-10' }).click()
  for (const key of ['PHAROS-11', 'PHAROS-12']) await expect(row(page, key)).toHaveAttribute('aria-level', '2')
  expect(errors).toEqual([])
})


test('AEON-385: filtered field patches keep sibling order', async ({ page }) => {
  await controlledStream(page)
  const data = fixtures(), errors = watchErrors(page)
  await mockWork(page, data)
  await page.goto('/p/PHAROS/tickets?view=outline&q=Cloud')
  await expect(row(page, 'PHAROS-12')).toBeVisible()
  const before = await keys(page).allTextContents()
  const ticket = data.nodes.find(n => n.id === 'n-2')!
  Object.assign(ticket, { title: 'Cloud connector changed live', updated_at: ago(-1) })
  await outlineEvent(page, ticket, ['title'])
  await expect(row(page, ticket.key)).toContainText(ticket.title)
  await expect(keys(page)).toHaveText(before)
  await expect(page.locator('.live-pill')).toHaveCount(0)
  expect(errors).toEqual([])
})
