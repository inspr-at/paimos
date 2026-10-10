// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors, type Call } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

// setSystemTime lets time flow (setFixedTime would freeze Vue's event timestamps).
test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const writes = (calls: Call[], method = 'PATCH') => calls.filter(call => call.method === method && call.path.startsWith('/api/nodes'))
const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
const hour = (h: number) => new Date(Date.parse('2026-09-23T12:00:00Z') - h * 3_600_000).toISOString()

test('the panel shows title, properties, Markdown sections, relations and the timeline', async ({ page }) => {
  const errors = watchErrors(page)
  const data = fixtures()
  data.nodes.find(n => n.id === 'n-1')!.fields.acceptance_criteria = '- [ ] Cleanup leaves nothing behind'
  await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-11')
  const ws = panel(page)
  await expect(ws.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await expect(ws.getByRole('button', { name: /Status: In progress/ })).toBeVisible()
  await expect(ws.getByRole('button', { name: /Priority: High/ })).toBeVisible()
  await expect(ws.getByRole('button', { name: /Assignee: Markus Barta/ })).toBeVisible()
  await expect(ws.getByRole('button', { name: /PHAROS-10/ })).toContainText('Guarded multi-cloud provisioning')
  await expect(ws.getByRole('region', { name: 'Description' }).locator('.task-box')).toHaveCount(2)
  await expect(ws.getByRole('region', { name: 'Acceptance criteria' })).toContainText('Cleanup leaves nothing behind')
  await expect(ws.getByRole('region', { name: 'Notes' })).toHaveCount(0)
  const relations = ws.getByRole('region', { name: 'Relations' })
  await expect(relations).toContainText('Blocked by')
  await expect(relations.getByRole('button', { name: /^Blocked by PHAROS-14/ })).toBeVisible()
  await expect(relations).toContainText('Relates to')
  const activity = ws.getByRole('region', { name: 'Activity' })
  // Two changes by the same person within a minute read as one line, net of the intermediate status.
  await expect(activity.locator('.entry.changes')).toHaveCount(1)
  await expect(activity.locator('.entry.changes')).toContainText('changed status New to In progress, changed priority Medium to High')
  await expect(activity.locator('.entry.comment').first()).toContainText('Picked this up.')
  await expect(activity.locator('.entry.comment').last().locator('strong')).toHaveText(['Mira Holm', 'done'])
  expect(await activity.locator('.entry').evaluateAll(entries => entries.map(entry => entry.className.split(' ')[1]))).toEqual(['created', 'changes', 'marker', 'comment', 'comment'])
  // Agent work markers read as one system line; the rest of the comment stays a comment.
  const marker = activity.locator('.entry.marker')
  await expect(marker.locator('.system-line')).toContainText('cursor-harbor-fleet started as builder')
  await expect(marker.locator('.comment-card')).toHaveText('Delegated via Cursor CLI; coordinator owns the merge.')
  await expect(marker.getByText('70648dfe-5a0c-4a6f-86f4-dab0870dde5c')).toHaveCount(0)
  await marker.getByRole('button', { name: /cursor-harbor-fleet started as builder/ }).click()
  await expect(marker.getByText('70648dfe-5a0c-4a6f-86f4-dab0870dde5c')).toBeVisible()
  await expect(marker.locator('.marker-detail')).toContainText('grok-4.6')
  // No comment card sits inside another framed block.
  expect(await activity.locator('.comment-card .comment-card, .entry.comment > .entry').count()).toBe(0)
  await relations.getByRole('button', { name: /^Blocked by PHAROS-14/ }).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-14')
  expect(errors).toEqual([])
})

test('titles edit inline on click, save with Enter under a precondition, and cancel with Esc', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = panel(page)
  await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
  const input = ws.getByLabel('Title', { exact: true })
  await expect(input).toBeFocused()
  await input.fill('Add an Oracle Cloud Always Free connector')
  await page.keyboard.press('Escape')
  await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  expect(writes(calls)).toHaveLength(0)
  await ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
  await input.fill('Add an Oracle Cloud Always Free connector')
  await page.keyboard.press('Enter')
  await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud Always Free connector' })).toBeVisible()
  expect(writes(calls)[0].body).toEqual({ title: 'Add an Oracle Cloud Always Free connector' })
  expect(writes(calls)[0].headers['if-unmodified-since']).toBe(hour(3))
  await expect(page.locator('#row-n-2')).toContainText('Always Free')
})

test('a title conflict shows the newer title and keeps the draft', async ({ page }) => {
  await mockWork(page, fixtures(), { conflictAlways: 'n-2' })
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = panel(page)
  await ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
  await ws.getByLabel('Title', { exact: true }).fill('My better title')
  await page.keyboard.press('Enter')
  await expect(page.getByText('PHAROS-12 was changed elsewhere. The newer version is shown; your draft is kept.')).toBeVisible()
  await expect(ws.getByRole('alert')).toContainText('Changed elsewhere to “Renamed by Mira”')
  await expect(ws.getByLabel('Title', { exact: true })).toHaveValue('My better title')
})

test('description edits with preview, Cmd/Ctrl+Enter saves, Esc asks before discarding', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = panel(page)
  await ws.getByRole('button', { name: 'Add a description' }).click()
  const area = ws.getByLabel('Description, Markdown')
  await expect(area).toBeFocused()
  await area.fill('## Plan\n\n- [ ] Free tier check')
  await ws.getByRole('radio', { name: 'Preview' }).click()
  await expect(ws.getByRole('region', { name: 'Description' }).getByRole('heading', { name: 'Plan' })).toBeVisible()
  await ws.getByRole('radio', { name: 'Write' }).click()
  await expect(area).toBeFocused()
  await page.keyboard.press('Escape')
  const confirm = page.getByRole('dialog', { name: 'Discard your description changes?' })
  await expect(confirm).toBeVisible()
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(area).toHaveValue('## Plan\n\n- [ ] Free tier check')
  await area.focus()
  await page.keyboard.press(`${mod}+Enter`)
  await expect(ws.getByRole('region', { name: 'Description' }).locator('.task-box')).toHaveCount(1)
  expect(writes(calls)[0].body).toEqual({ body: '## Plan\n\n- [ ] Free tier check' })
  // Notes appear only once they have content; adding them starts the editor.
  await ws.getByRole('button', { name: 'Notes' }).click()
  await ws.getByLabel('Notes, Markdown').fill('Check the ARM quota.')
  await ws.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(ws.getByRole('region', { name: 'Notes' })).toContainText('Check the ARM quota.')
  expect((writes(calls)[1].body as { fields: Record<string, unknown> }).fields.notes).toBe('Check the ARM quota.')
})

test('s, p and a open status, priority and assignee from the panel', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-14')
  const ws = panel(page)
  await expect(ws.getByRole('heading', { name: 'Visual acceptance of the version pill' })).toBeVisible()
  await page.keyboard.press('s')
  await page.getByRole('menu', { name: 'Status of PHAROS-14' }).getByRole('menuitemradio', { name: 'Backlog' }).click()
  await expect(ws.getByRole('button', { name: /Status: Backlog/ })).toBeVisible()
  await page.keyboard.press('p')
  await page.getByRole('menu', { name: 'Priority of PHAROS-14' }).getByRole('menuitemradio', { name: 'High' }).click()
  await expect(ws.getByRole('button', { name: /Priority: High/ })).toBeVisible()
  await page.keyboard.press('a')
  await page.getByLabel('Find assignee').fill('mira')
  await page.keyboard.press('Enter')
  await expect(ws.getByRole('button', { name: /Assignee: Mira Holm/ })).toBeVisible()
  const bodies = writes(calls).map(call => call.body as Record<string, unknown>)
  expect(bodies[0]).toEqual({ state: 'backlog' })
  expect((bodies[1].fields as Record<string, unknown>).priority).toBe('high')
  expect((bodies[2].fields as Record<string, unknown>).assignee).toBe('22222222-2222-4222-8222-222222222222')
  expect(writes(calls).every(call => !!call.headers['if-unmodified-since'])).toBe(true)
})

test('an epic lists its tickets with progress and adds new ones inline', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-10')
  const ws = panel(page)
  const children = ws.getByRole('region', { name: 'Tickets in this epic' })
  await expect(children.locator('.child-row')).toHaveCount(2)
  await expect(children.locator('.pct')).toHaveText('0/2')
  await children.getByRole('button', { name: 'Add ticket' }).click()
  const input = children.getByLabel('New ticket title')
  await input.fill('Add a Scaleway connector')
  await page.keyboard.press('Enter')
  expect(writes(calls, 'POST')).toHaveLength(0)
  await page.keyboard.press(`${mod}+Enter`)
  await expect(children.locator('.child-row')).toHaveCount(3)
  await expect(input).toHaveValue('')
  await expect(input).toBeFocused()
  const post = writes(calls, 'POST').find(call => call.path === '/api/nodes')!
  expect(post.body).toEqual({ kind_id: 'k-work', title: 'Add a Scaleway connector', parent_id: 'n-epic', state: 'new', key_prefix: 'PHAROS' })
  await expect(page.locator('tr.ticket-row').filter({ hasText: 'Add a Scaleway connector' })).toHaveCount(1)
  await children.locator('.child-row').first().click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
})

test('comments post with Cmd/Ctrl+Enter from c, and own comments can be edited and deleted', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-13')
  const ws = panel(page)
  await expect(ws.getByRole('heading', { name: 'Run the disposable Hetzner end-to-end check' })).toBeVisible()
  await page.keyboard.press('c')
  const composer = ws.getByLabel('Add a comment')
  await expect(composer).toBeFocused()
  await page.keyboard.type('Ran it on **hsb-lab**.')
  await page.keyboard.press(`${mod}+Enter`)
  const comment = ws.locator('.entry.comment').filter({ hasText: 'Ran it on' })
  await expect(comment.locator('strong').last()).toHaveText('hsb-lab')
  await expect(composer).toHaveValue('')
  expect(writes(calls, 'POST').find(call => call.path.endsWith('/comments'))?.body).toEqual({ body_markdown: 'Ran it on **hsb-lab**.' })
  await comment.getByRole('button', { name: 'Edit comment' }).click()
  await ws.getByLabel('Comment, Markdown').fill('Ran it twice on **hsb-lab**.')
  await page.keyboard.press(`${mod}+Enter`)
  await expect(ws.locator('.entry.comment').filter({ hasText: 'Ran it twice' })).toBeVisible()
  await ws.locator('.entry.comment').filter({ hasText: 'Ran it twice' }).getByRole('button', { name: 'Delete comment' }).click()
  await page.getByRole('dialog', { name: 'Delete this comment?' }).getByRole('button', { name: 'Delete comment' }).click()
  await expect(ws.locator('.entry.comment')).toHaveCount(0)
  expect(calls.some(call => call.method === 'DELETE' && /\/comments\/\d+$/.test(call.path))).toBe(true)
})

test('comments by others or older than 15 minutes cannot be edited', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(panel(page).locator('.entry.comment')).toHaveCount(2)
  await expect(panel(page).getByRole('button', { name: 'Edit comment' })).toHaveCount(0)
})

test('full page shows the same ticket in two columns; f and Back return to the panel', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(panel(page)).toBeVisible()
  await page.keyboard.press('f')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11?view=full')
  const article = page.getByRole('article', { name: 'Ticket details' })
  await expect(article.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await expect(article.getByRole('complementary', { name: 'Properties' })).toContainText('Assignee')
  await expect(page.getByRole('grid', { name: 'Tickets' })).toBeHidden()
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('PHAROS-11')
  const measure = await article.locator('.markdown-body').first().evaluate(el => el.getBoundingClientRect().width / parseFloat(getComputedStyle(el).fontSize))
  expect(measure).toBeLessThanOrEqual(46)
  await page.goBack()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
  await expect(panel(page)).toBeVisible()
  await panel(page).getByRole('button', { name: 'Open as full page' }).click()
  await page.getByRole('article', { name: 'Ticket details' }).getByRole('button', { name: 'Show beside the list' }).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
})

test('move to another parent uses a searchable picker; delete asks and then leaves the list', async ({ page }) => {
  const data = fixtures()
  data.nodes.push({ id: 'n-epic-2', key: 'PHAROS-20', kind_slug: 'epic', title: 'Oracle and Scaleway connectors', body: '', state: 'backlog', fields: {}, parent_id: 'p-pharos', project: 'p-pharos', created_at: hour(100), updated_at: hour(50) })
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-14')
  const ws = panel(page)
  await ws.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Move to another parent…' }).click()
  await page.getByLabel('Find a parent').fill('oracle')
  await expect(page.getByRole('listbox', { name: 'Parents' }).getByRole('option')).toHaveCount(1)
  await page.keyboard.press('Enter')
  await expect(ws.getByRole('button', { name: /PHAROS-20/ })).toBeVisible()
  const move = calls.find(call => call.path.endsWith('/move'))!
  expect(move.body).toEqual({ parent_id: 'n-epic-2', before_id: null })
  expect(move.headers['if-unmodified-since']).toBe(hour(12))
  await ws.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Delete ticket…' }).click()
  const confirm = page.getByRole('dialog', { name: 'Delete PHAROS-14?' })
  await expect(confirm.getByRole('button', { name: 'Cancel' })).toBeFocused()
  await confirm.getByRole('button', { name: 'Delete ticket' }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets')
  await expect(panel(page)).toHaveCount(0)
  await expect(page.locator('tr.ticket-row').filter({ hasText: 'PHAROS-14' })).toHaveCount(0)
  expect(calls.some(call => call.method === 'DELETE' && call.path === '/api/nodes/n-4')).toBe(true)
})

test('n opens a quick-create row; Cmd/Ctrl+Enter creates and keeps going; Esc leaves the field then closes', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
  expect(await page.locator('.btn.primary:visible').count()).toBe(1)
  await page.keyboard.press('n')
  const title = page.getByLabel('New ticket title')
  await expect(title).toBeFocused()
  await title.fill('Hetzner snapshot retention')
  await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: 'Type: Ticket' })).toBeFocused()
  await page.keyboard.press('Tab'); await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: 'Priority: No priority' })).toBeFocused()
  await page.keyboard.press('Enter')
  await page.getByRole('menuitemradio', { name: 'High' }).click()
  await title.focus()
  await page.keyboard.press('Enter')
  expect(writes(calls, 'POST')).toHaveLength(0)
  await page.keyboard.press(`${mod}+Enter`)
  await expect(page.locator('tr.ticket-row').filter({ hasText: 'Hetzner snapshot retention' })).toHaveCount(1)
  await expect(title).toHaveValue('')
  await expect(title).toBeFocused()
  expect(writes(calls, 'POST')[0].body).toEqual({ kind_id: 'k-work', title: 'Hetzner snapshot retention', state: 'new', fields: { priority: 'high' }, parent_id: 'p-pharos', key_prefix: 'PHAROS' })
  await expect(page.getByText(/Created PHAROS-\d+/)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(title).not.toBeFocused()
  await expect(title).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(title).toHaveCount(0)
})

test('a ticket that no longer exists says so; read-only principals cannot edit', async ({ page }) => {
  await mockWork(page, fixtures(), { readOnly: true })
  await page.route('**/api/nodes/n-5', route => route.fulfill({ status: 404, json: { error: 'node not found' } }))
  await page.goto('/p/PHAROS/PHAROS-15?closed=1')
  await expect(panel(page).getByRole('heading', { name: 'PHAROS-15 is no longer here' })).toBeVisible()
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = panel(page)
  await expect(ws.getByRole('note')).toContainText('You can read this ticket but not change it.')
  await expect(ws.getByRole('button', { name: 'Edit description' })).toHaveCount(0)
  await expect(ws.getByRole('button', { name: /Status: Backlog/ })).toBeDisabled()
  await expect(ws.getByLabel('Add a comment')).toBeDisabled()
  await page.keyboard.press('e')
  await expect(ws.getByLabel('Title', { exact: true })).toHaveCount(0)
})

test('unsaved edits are guarded when moving to another ticket', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = panel(page)
  await ws.getByRole('button', { name: 'Add a description' }).click()
  await ws.getByLabel('Description, Markdown').fill('Half a thought')
  await ws.getByRole('button', { name: 'Next ticket' }).click()
  const confirm = page.getByRole('dialog', { name: 'Discard unsaved changes?' })
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-12')
  await expect(ws.getByLabel('Description, Markdown')).toHaveValue('Half a thought')
  await ws.getByRole('button', { name: 'Next ticket' }).click()
  await page.getByRole('dialog', { name: 'Discard unsaved changes?' }).getByRole('button', { name: 'Discard changes' }).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-13')
})

test('the panel counts the whole list and next loads the following page', async ({ page }) => {
  await mockWork(page, fixtures({ bigProject: 450 }))
  await page.goto('/p/AEON/AEON-298')
  const ws = panel(page)
  await expect(ws.locator('.position')).toHaveText('200 / 451')
  await page.keyboard.press('j')
  await expect(page).toHaveURL('/p/AEON/AEON-299')
  await expect(ws.locator('.position')).toHaveText('201 / 451')
})

test('Tab walks the panel in reading order; Esc closes a popover before the panel', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-12')
  const ws = panel(page)
  await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await ws.getByRole('button', { name: 'Copy PHAROS-12' }).focus()
  const order: string[] = []
  for (let i = 0; i < 13; i++) {
    order.push(await page.evaluate(() => { const el = document.activeElement as HTMLElement; return el.getAttribute('aria-label') ?? el.textContent!.trim().slice(0, 30) }))
    await page.keyboard.press('Tab')
  }
  expect(order).toEqual([
    'Copy PHAROS-12', 'Previous ticket', 'Next ticket', 'Edit', 'Open as full page', 'Open in a new tab', 'More actions', 'Close ticket details',
    'Add an Oracle Cloud connector', 'Status: Backlog. Change status', 'Priority: Medium. Change priority', 'Assignee: nobody. Change assignee', 'PHAROS-10 Guarded multi-cloud provisioning. Open it',
  ])
  await page.keyboard.press('Shift+Tab'); await page.keyboard.press('Shift+Tab'); await page.keyboard.press('Shift+Tab'); await page.keyboard.press('Shift+Tab')
  await expect(ws.getByRole('button', { name: /Status: Backlog/ })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('menu', { name: 'Status of PHAROS-12' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu', { name: 'Status of PHAROS-12' })).toHaveCount(0)
  await expect(ws).toBeVisible()
  await expect(ws.getByRole('button', { name: /Status: Backlog/ })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/p/PHAROS/tickets')
})

test('quick create shows unset priority and parent as dimmed values', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
  await page.keyboard.press('n')
  await expect(page.getByRole('button', { name: 'Priority: No priority' })).toContainText('No priority')
  await expect(page.getByRole('button', { name: 'Parent: none' })).toContainText('No parent')
  expect(await page.getByRole('button', { name: 'Parent: none' }).locator('.chip-text').evaluate(el => el.classList.contains('unset'))).toBe(true)
})

test('a selected row hides its parent chip rather than clipping it under the row actions', async ({ page }) => {
  // The panel lookup wins the race with the list page. They resolve to the
  // same store object, so the later page must still establish the row cursor.
  await mockWork(page, fixtures(), { hold: call => call.method === 'GET' && call.path === '/api/nodes' && !call.query.get('q')
    ? { until: new Promise(resolve => setTimeout(resolve, 200)) } : undefined })
  await page.goto('/p/PHAROS/PHAROS-11')
  const row = page.locator('#row-n-1')
  await expect(row.locator('.row-actions')).toBeVisible()
  await expect(row.locator('.parent-chip')).toHaveCSS('opacity', '0')
  await expect(page.locator('#row-n-2 .parent-chip')).toHaveCSS('opacity', '1')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`on a phone the ticket is a full-screen sheet with wrapping chips and a bottom composer in ${colorScheme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/PHAROS-11')
    const ws = panel(page)
    await expect(ws.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
    expect(await ws.boundingBox()).toEqual({ x: 0, y: 0, width: 390, height: 844 })
    await expect(ws.getByRole('button', { name: 'Close ticket details' })).toBeInViewport()
    const chips = ws.locator('.props.row')
    // The chips wrap onto more lines; none is cut at the edge.
    expect(await chips.evaluate(el => getComputedStyle(el).flexWrap)).toBe('wrap')
    expect(await chips.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
    const composer = (await ws.getByLabel('Add a comment').boundingBox())!
    expect(composer.y + composer.height).toBeGreaterThan(790)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await ws.getByRole('button', { name: 'Close ticket details' }).click()
    await expect(page).toHaveURL('/p/PHAROS/tickets')
    expect(errors).toEqual([])
  })
}

void me

function linkedKnowledge() {
  const data = fixtures()
  const ticket = data.nodes.find(node => node.key === 'PHAROS-11')!
  data.nodes.unshift({ ...ticket, id: 'knowledge-18', key: 'GUI-18', kind_slug: 'guideline', title: 'Concept PHAROS-11 · Ein Arbeitsknoten mit langen deutschen Erklärungen', body: 'Search 11 also matches this knowledge entry.', fields: {} })
  return data
}

function barrier() {
  let release!: () => void
  const until = new Promise<void>(resolve => { release = resolve })
  return { until, release }
}

for (const first of ['lookup', 'list'] as const) {
  test(`deep link wins over a knowledge peek with ${first} answering first`, async ({ page }) => {
    const delayed = barrier()
    const arrived = barrier()
    const calls = await mockWork(page, linkedKnowledge(), {
      hold: call => {
        const lookup = call.path === '/api/nodes' && call.query.get('q') === 'PHAROS-11'
        const list = call.path === '/api/nodes' && call.query.get('limit') === '200'
        if (first === 'lookup' ? list : lookup) return { until: delayed.until, computed: arrived.release }
      },
    })
    try {
      await page.goto('/p/PHAROS/PHAROS-11?q=11&type=ticket&peek=GUI-18')
      await arrived.until
      const ws = panel(page)
      if (first === 'list') await expect(page.locator('#row-n-1')).toBeVisible()
      await expect(ws).toHaveCount(1)
      await expect(ws.getByRole('button', { name: 'Copy PHAROS-11' })).toBeVisible()
      await expect(ws.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
      delayed.release()
      await expect(page.locator('#row-n-1')).toBeVisible()
      await expect(page).toHaveURL('/p/PHAROS/PHAROS-11?q=11&type=ticket')
      await expect(ws).toHaveCount(1)
      await expect(ws).not.toContainText('GUI-18')
      await ws.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' }).click()
      await ws.getByLabel('Title', { exact: true }).fill('The routed ticket was edited')
      await page.keyboard.press('Enter')
      await expect(ws.getByRole('heading', { name: 'The routed ticket was edited' })).toBeVisible()
      expect(writes(calls).map(call => call.path)).toEqual(['/api/nodes/n-1'])
    } finally { delayed.release() }
  })
}

test('the routed panel keeps usable list space and stable controls', async ({ page }) => {
  test.setTimeout(120_000)
  const data = linkedKnowledge()
  const title = 'Ein Arbeitsknoten: verlässliche Zuordnung und nachvollziehbare Änderungen im gesamten Projekt'
  data.nodes.find(node => node.id === 'n-1')!.title = title
  data.nodes.find(node => node.id === 'n-1')!.body += '\n\nFollow PHAROS-14.'
  data.nodes.find(node => node.id === 'n-4')!.body = 'Continue to PHAROS-12.'
  await mockWork(page, data)
  for (const savedWidth of [null, 1000]) {
    data.preferences.layout = savedWidth ? { panel: savedWidth } : {}
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      for (const search of [true, false]) {
        for (const width of [1440, 1024, 390]) {
          await page.setViewportSize({ width, height: 900 })
          await page.goto(`/p/PHAROS/PHAROS-11?${search ? 'q=11&' : ''}type=ticket&peek=GUI-18`)
          const ws = panel(page)
          await expect(ws).toHaveCount(1)
          await expect(ws.getByRole('heading', { name: title })).toBeVisible()
          if (width >= 1024) {
            const grid = page.getByRole('grid', { name: 'Tickets' })
            await expect(page.locator('#row-n-1')).toBeVisible()
            const listBox = (await grid.boundingBox())!
            const panelBox = (await ws.boundingBox())!
            expect(listBox.width).toBeGreaterThan(400)
            expect(listBox.x + listBox.width).toBeLessThanOrEqual(panelBox.x)
          }
          // Measure the rendered buttons, including crumbs that can overflow
          // their shrinking nav. scrollWidth alone misses overlapping siblings.
          const expectHeaderFits = async () => {
            const geometry = await ws.locator('.panel-bar-main').evaluate(el => {
              const frame = el.getBoundingClientRect()
              const buttons = [...el.querySelectorAll('button')].map(button => {
                const rect = button.getBoundingClientRect()
                return { name: button.getAttribute('aria-label') ?? button.textContent?.trim(), left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width, height: rect.height }
              }).filter(button => button.width > 0 && button.height > 0)
              return { left: frame.left, right: frame.right, buttons }
            })
            expect(geometry.buttons.length).toBeGreaterThan(0)
            for (const [index, button] of geometry.buttons.entries()) {
              expect(button.left, `${button.name} inside header`).toBeGreaterThanOrEqual(geometry.left - 0.5)
              expect(button.right, `${button.name} inside header`).toBeLessThanOrEqual(geometry.right + 0.5)
              for (const other of geometry.buttons.slice(index + 1)) {
                if (Math.min(button.bottom, other.bottom) > Math.max(button.top, other.top)) {
                  const overlap = Math.min(button.right, other.right) - Math.max(button.left, other.left)
                  expect(overlap, `${button.name} overlaps ${other.name}`).toBeLessThanOrEqual(0.5)
                }
              }
            }
          }
          await expectHeaderFits()
          await expectStableControls({
            controls: { close: ws.getByRole('button', { name: 'Close ticket details' }), edit: ws.getByRole('button', { name: 'Edit', exact: true }), more: ws.getByRole('button', { name: 'More actions' }) },
            scrollAreas: { panel: ws },
            interactions: [{ name: 'open and close actions', run: async () => {
              await ws.getByRole('button', { name: 'More actions' }).click()
              await expect(page.getByRole('menu')).toBeVisible()
              await expect(page.getByRole('menuitem', { name: 'Open as full page' })).toBeVisible()
              await expect(page.getByRole('menuitem', { name: 'Open in a new tab' })).toBeVisible()
              await page.keyboard.press('Escape')
              await expect(page.getByRole('menu')).toHaveCount(0)
            } }, { name: 'follow a ticket link', run: async () => {
              await ws.getByRole('region', { name: 'Description' }).getByRole('link', { name: 'PHAROS-14: Visual acceptance of the version pill', exact: true }).click()
              await expect(ws.getByRole('button', { name: 'Copy PHAROS-14' })).toBeVisible()
              await expect(ws.getByRole('heading', { name: 'Visual acceptance of the version pill' })).toBeVisible()
              await expect(ws.getByRole('button', { name: 'Back to PHAROS-11', exact: true })).toBeVisible()
              await expectHeaderFits()
              await page.screenshot({ path: `test-results/aeon-687-wrongrec/followed-${savedWidth ? 'wide' : 'default'}-${search ? 'search' : 'plain'}-${colorScheme}-${width}.png` })
            } }, { name: 'follow another link and return through the trail', run: async () => {
              // Back is present throughout these series steps; measure it too.
              await expectStableControls({
                controls: { back: ws.locator('.back-btn'), close: ws.getByRole('button', { name: 'Close ticket details' }), edit: ws.getByRole('button', { name: 'Edit', exact: true }), more: ws.getByRole('button', { name: 'More actions' }) },
                scrollAreas: { panel: ws },
                interactions: [{ name: 'follow a second ticket link', run: async () => {
                  await ws.getByRole('region', { name: 'Description' }).getByRole('link', { name: 'PHAROS-12: Add an Oracle Cloud connector', exact: true }).click()
                  await expect(ws.getByRole('button', { name: 'Copy PHAROS-12' })).toBeVisible()
                  await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
                  await expect(ws.getByRole('button', { name: 'Back to PHAROS-14', exact: true })).toBeVisible()
                  await expectHeaderFits()
                } }, { name: 'Back to the first followed ticket', run: async () => {
                  await ws.getByRole('button', { name: 'Back to PHAROS-14', exact: true }).click()
                  await expect(ws.getByRole('button', { name: 'Copy PHAROS-14' })).toBeVisible()
                  await expect(ws.getByRole('heading', { name: 'Visual acceptance of the version pill' })).toBeVisible()
                  await expect(ws.getByRole('button', { name: 'Back to PHAROS-11', exact: true })).toBeVisible()
                  await expectHeaderFits()
                } }],
              })
            } }, { name: 'Back to the original routed ticket', run: async () => {
              await ws.getByRole('button', { name: 'Back to PHAROS-11', exact: true }).click()
              await expect(ws.getByRole('button', { name: 'Copy PHAROS-11' })).toBeVisible()
              await expect(ws.getByRole('heading', { name: title })).toBeVisible()
              await expect(ws.locator('.back-btn')).toHaveCount(0)
              await expect(page).toHaveURL(`/p/PHAROS/PHAROS-11?${search ? 'q=11&' : ''}type=ticket`)
              await expectHeaderFits()
            } }],
          })
          await page.screenshot({ path: `test-results/aeon-687-wrongrec/${savedWidth ? 'wide' : 'default'}-${search ? 'search' : 'plain'}-${colorScheme}-${width}.png` })
        }
      }
    }
  }
})
