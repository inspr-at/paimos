// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1618 (Tick spec v1): people create a goal and link work to it. Risks: the
// toolbar or the rows move when the New goal sheet opens or closes; the new goal
// is not opened with Link work focused; the picker links something else than the
// three items picked, or the rollup ignores the links; Unlink or its Undo loses
// work; the word "outcome" reaches people; a person who may not create goals sees
// the button. Tick spec v1.1 (part a): Rename moves the row or its controls, Esc
// does not cancel, an empty name is sent, Delete asks for confirmation instead of
// offering Undo, Undo does not bring the goal back, the menu does not fit a phone,
// or a person without edit rights can rename, link or delete by key.
// Tick round 1: a Lands day reads "30 Oct 23:59" instead of "Fri 30 Oct"; a goal
// without work reads "0%" and "0 of 0 done", or changes height once work arrives;
// "Link {n}" leaves the search row; the phone ⋯ sheet shows key hints.
// Screenshots go to each test's output directory.
import { test, expect, type Page, type Locator, type TestInfo } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: 'Europe/Vienna', locale: 'en-GB' })
const NOW = Date.parse('2026-10-21T08:00:00Z')
const HOUR = 3_600_000
const iso = (ms: number) => new Date(ms).toISOString()
test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

type Item = Record<string, unknown> & { id: string; keys: string[]; state: string; linked?: boolean }
type GoalMock = Record<string, unknown> & { id: string; title: string; project_id: string | null; linked: string[]; outcomes: Item[]; eta: Record<string, unknown>; deleted?: number }
function world() {
  const data = fixtures()
  const agentGoal: GoalMock = {
    id: 'g-agent', title: 'Fleet hosts report health on their own', project_id: 'p-pharos', linked: [], eta: { source: 'agent', text: 'Fri 23 Oct', at: '2026-10-23T15:00:00Z', partial: false },
    outcomes: [{ id: '00000000-0000-4000-8000-000000000001', ref: 'probes', label: 'Probes report every minute', note: '', position: 1, keys: ['PHAROS-14'], state: 'queued', session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 0, total_leaves: 1 }, weight: 1, handed_over: false }],
  }
  return { data, goals: [agentGoal], writes: [] as { method: string; path: string; body: unknown }[], next: 1, pageLimit: 0, etaEvents: [] as Record<string, unknown>[] }
}
type World = ReturnType<typeof world>
const STATE: Record<string, string> = { done: 'done', 'in-progress': 'moving', qa: 'moving' }
function shape(w: World, g: GoalMock) {
  const linked: Item[] = g.linked.map(id => {
    const node = w.data.nodes.find(n => n.id === id)!
    return { id, ref: '', label: node.title, note: '', position: 100, keys: [node.key], state: STATE[node.state] ?? 'queued', session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 0, total_leaves: 1 }, weight: 1, handed_over: false, linked: true }
  })
  const items = [...g.outcomes, ...linked]
  const states = { moving: 0, queued: 0, paused: 0, done: 0 }
  for (const item of items) states[item.state as keyof typeof states]++
  const pct = items.length ? Math.round(100 * states.done / items.length) : 0
  const home = w.data.projects.find(p => p.id === g.project_id)
  return {
    id: g.id, key: g.id.toUpperCase(), title: g.title, kind: 'goal', project_id: g.project_id, workspace: !g.project_id, expansion: 'full', counts: { outcomes: items.length, tickets: items.length },
    items, steps: [], next_step: null, projects: home ? [{ id: home.id, key: home.classic || home.key, name: home.title, group_ids: [] }] : [], waits: { count: 0, question_ids: [] },
    progress: { pct, computed_pct: pct, source: 'computed', basis: 'count', total: items.length, done: states.done, items_without_estimate: items.length, items_partially_estimated: 0, remaining_hours: 0, remaining_unestimated: 0 },
    eta: g.eta, passed: false, states, done: false, stall: { stalled: false, since: iso(NOW - HOUR) }, read_through: 10 + w.writes.length, generation: 1 + w.writes.length,
  }
}
async function mockGoals(page: Page, w: World, options: { readOnly?: boolean } = {}) {
  await mockWork(page, w.data, { readOnly: options.readOnly })
  const written = (route: Parameters<Parameters<Page['route']>[1]>[0], extra: Record<string, unknown> = {}) => route.fulfill({ json: { changed: true, read_through: 10 + w.writes.length, generation: 1 + w.writes.length, ...extra }, headers: { 'Aeon-Contract': 'goals/1.7' } })
  await page.route(/\/api\/goals(\?.*)?$/, async route => {
    const request = route.request()
    if (request.method() === 'POST') {
      const body = request.postDataJSON() as { title: string; project_id: string | null; eta?: { at: string; text?: string } }
      w.writes.push({ method: 'POST', path: '/goals', body })
      const id = `g-new${w.next++}`
      w.goals.push({ id, title: body.title, project_id: body.project_id, linked: [], outcomes: [], eta: { source: 'none', partial: false } })
      if (body.eta) setEta(w, w.goals.at(-1)!, body.eta)
      return written(route, { id, key: id.toUpperCase() })
    }
    const url = new URL(request.url())
    let items = w.goals.filter(g => !g.deleted).map(g => shape(w, g))
    const q = (url.searchParams.get('q') ?? '').trim().toLowerCase()
    if (q) items = items.filter(g => `${g.title} ${g.key}`.toLowerCase().includes(q) || g.items.some(item => `${item.label} ${item.keys.join(' ')}`.toLowerCase().includes(q)))
    const state = url.searchParams.get('state')
    if (state === 'moving' || state === 'queued' || state === 'paused' || state === 'done') items = items.filter(g => g.states[state] > 0)
    const matched = items
    let next_cursor: string | undefined
    if (w.pageLimit > 0 && items.length > w.pageLimit) { next_cursor = 'page-2'; items = items.slice(0, w.pageLimit) }
    const totals = { moving: 0, queued: 0, paused: 0, done: 0 }
    for (const g of matched) for (const s of ['moving', 'queued', 'paused', 'done'] as const) totals[s] += g.states[s]
    return route.fulfill({ json: { items, totals, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, next_cursor, read_through: 10 + w.writes.length, generation: 1 + w.writes.length }, headers: { 'Aeon-Contract': 'goals/1.7' } })
  })
  await page.route(/\/api\/goals\/g-[a-z0-9]+\/work$/, async route => {
    const request = route.request(), id = new URL(request.url()).pathname.split('/')[3]!
    const goal = w.goals.find(g => g.id === id)!, body = request.postDataJSON() as { node_ids: string[] }
    w.writes.push({ method: request.method(), path: `/goals/${id}/work`, body })
    const before = [...goal.linked]
    if (request.method() === 'POST') for (const node of body.node_ids) { if (!goal.linked.includes(node)) goal.linked.push(node) }
    else goal.linked = goal.linked.filter(node => !body.node_ids.includes(node))
    const delta = request.method() === 'POST' ? goal.linked.filter(node => !before.includes(node)) : before.filter(node => !goal.linked.includes(node))
    return written(route, { id, key: id.toUpperCase(), changed: delta.length > 0, node_ids: [...goal.linked], delta, event_id: delta.length ? 40 + w.writes.length : null })
  })
  // Rename (PATCH title) and Delete; a delete's event undoes through the events API.
  await page.route(/\/api\/goals\/g-[a-z0-9-]+$/, async route => {
    const request = route.request(), id = new URL(request.url()).pathname.split('/')[3]!
    const goal = w.goals.find(g => g.id === id)
    if (request.method() === 'GET') {
      if (!goal || goal.deleted) return route.fulfill({ status: 404, json: { error: 'not found' } })
      return route.fulfill({ json: shape(w, goal), headers: { 'Aeon-Contract': 'goals/1.7' } })
    }
    if (!goal) return route.fulfill({ status: 404, json: { error: 'not found' } })
    if (request.method() === 'PATCH') {
      const body = request.postDataJSON() as { title?: string; eta?: { at: string; text?: string } | null }
      w.writes.push({ method: 'PATCH', path: `/goals/${id}`, body })
      if (body.title !== undefined) goal.title = body.title
      if (body.eta !== undefined) setEta(w, goal, body.eta)
      return written(route, { id, key: id.toUpperCase() })
    }
    if (request.method() !== 'DELETE') return route.fallback()
    w.writes.push({ method: 'DELETE', path: `/goals/${id}`, body: null })
    goal.deleted = 900 + w.writes.length
    return written(route, { id, key: id.toUpperCase(), node_ids: [], event_id: goal.deleted })
  })
  await page.route(/\/api\/events\/9\d\d\/undo$/, async route => {
    const event = Number(new URL(route.request().url()).pathname.split('/')[3])
    w.writes.push({ method: 'POST', path: `/events/${event}/undo`, body: null })
    const goal = w.goals.find(g => g.deleted === event)
    if (!goal) return route.fulfill({ status: 409, json: { error: 'conflict' } })
    goal.deleted = undefined
    return route.fulfill({ status: 201, json: { id: event + 1, type: 'goal.restored', undo_of: event } })
  })
  await page.route(/\/api\/goals\/[^/]+\/series\?/, route => route.fulfill({ json: { range: '24h', expansion: 'full', points: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/goals\/[^/]+\/history/, route => {
    const id = new URL(route.request().url()).pathname.split('/')[3]
    return route.fulfill({ json: { items: w.etaEvents.filter(event => event.node_id === id), read_through: 10, generation: 1 } })
  })
  await page.route(/\/api\/nodes\/g-[a-z0-9]+$/, route => route.fulfill({ json: { id: 'g', key: 'G', kind_id: 'k-goal', title: 'Goal', body: '', fields: {}, state: 'open', parent_id: null, position: 1, created_at: iso(NOW - 300 * HOUR), updated_at: iso(NOW) } }))
}
// A person's Lands, kept as the server keeps it, with its value-bearing history event (newest first).
function setEta(w: World, goal: GoalMock, eta: { at: string; text?: string } | null) {
  const before = goal.eta
  goal.eta = eta ? { source: 'agent', ...eta, by: me.id, partial: false } : { source: 'none', partial: false }
  w.etaEvents.unshift({ id: 500 + w.etaEvents.length, actor_principal_id: me.id, node_id: goal.id, type: 'goal.eta_changed', before, after: goal.eta, at: iso(NOW) })
}
async function openGoals(page: Page, w: World, options: { readOnly?: boolean } = {}) {
  await mockGoals(page, w, options)
  await page.goto('/?view=goals')
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
}
async function shot(page: Page, info: TestInfo, name: string) { await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true }) }
// Every word a person can read in the app, the open sheet and menus included.
const noOutcome = async (page: Page, where: string) => expect(await page.locator('body').innerText(), `${where}: no "outcome" for people`).not.toMatch(/outcome/i)
const goalRow = (page: Page, id: string) => page.locator(`.goal[data-goal-id="${id}"]`)
async function newGoalEntry(page: Page, phone: boolean): Promise<Locator> {
  if (!phone) return page.locator('.new-goal')
  await page.locator('.projects-toolbar .more-btn').click()
  return page.getByRole('menuitem', { name: 'New goal' })
}

for (const [width, height] of [[1440, 900], [1024, 800], [390, 844]] as const) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`${width} ${colorScheme}: New goal opens and closes without moving the toolbar or rows; create, link three, unlink and Undo`, async ({ page }, info) => {
      await page.setViewportSize({ width, height })
      await page.emulateMedia({ colorScheme })
      const errors = watchErrors(page)
      const w = world()
      await openGoals(page, w)
      const phone = width <= 760
      const name = `${width}-${colorScheme}`
      if (!phone) await expect(page.locator('.new-goal')).toBeVisible()
      await shot(page, info, `toolbar-${name}`)

      // movementPx 0: the toolbar and the rows stay where they are while the sheet opens and closes.
      const controls: Record<string, Locator> = { search: page.locator('.project-search'), display: page.locator('.display-btn'), row: goalRow(page, 'g-agent').locator('.goal-row') }
      if (phone) controls.more = page.locator('.projects-toolbar .more-btn')
      else { controls.newGoal = page.locator('.new-goal'); controls.view = page.locator('.view-seg') }
      const guard = await controlStability(page, controls)
      const sheet = page.locator('dialog.project-settings-sheet')
      await guard.check(async () => { await (await newGoalEntry(page, phone)).click(); await expect(sheet).toBeVisible() })
      await expect(page.locator('#goal-title')).toBeFocused()
      await expect(page.locator('#goal-title')).toHaveAttribute('placeholder', 'What should be true when this is done?')
      // "Lands · optional" says it once; the hint only says what progress does.
      await expect(sheet.locator('.gs-f > small')).toHaveText('Progress fills in from linked work.')
      await noOutcome(page, 'sheet')
      await shot(page, info, `sheet-empty-${name}`)
      await guard.check(async () => { await sheet.getByRole('button', { name: /^Cancel/ }).click(); await expect(sheet).toHaveCount(0) })
      guard.done()

      // Create: title, a project, a Lands day.
      await (await newGoalEntry(page, phone)).click()
      await page.locator('#goal-title').fill('Managed provisioning works end to end')
      await page.locator('#goal-where').selectOption('p-pharos')
      await page.locator('dialog input.native[type="date"]').evaluate((input: HTMLInputElement) => { input.value = '2026-10-30'; input.dispatchEvent(new Event('change', { bubbles: true })) })
      await expect(page.locator('#np-date')).toContainText('30 Oct 2026')
      await shot(page, info, `sheet-filled-${name}`)
      await page.locator('#goal-go').click()
      await expect(sheet).toHaveCount(0)
      await expect(page.getByText('Goal created')).toBeVisible()
      const created = w.writes.find(write => write.method === 'POST' && write.path === '/goals')!.body as { title: string; project_id: string; eta: { at: string; text: string } }
      expect(created).toMatchObject({ title: 'Managed provisioning works end to end', project_id: 'p-pharos', eta: { text: 'Fri 30 Oct' } })
      expect(new Date(created.eta.at).toISOString()).toBe('2026-10-30T22:59:00.000Z')
      const goal = goalRow(page, 'g-new1')
      await expect(goal).toHaveAttribute('data-open', '1')
      const linkWork = goal.locator('.link-work')
      await expect(linkWork).toBeFocused()
      await shot(page, info, `created-${name}`)

      // Link work: three picked, one confirm.
      await page.keyboard.press('Enter')
      const picker = page.locator('.picker.multi')
      await expect(picker).toBeVisible()
      await picker.locator('input').fill('PHAROS-1')
      for (const key of ['PHAROS-11', 'PHAROS-12', 'PHAROS-15']) await picker.locator('.option', { hasText: key }).click()
      await expect(picker.locator('.option[aria-selected="true"]')).toHaveCount(3)
      await expect(picker.getByRole('button', { name: /^Link 3/ })).toBeEnabled()
      await noOutcome(page, 'picker')
      await shot(page, info, `picker-three-${name}`)
      await picker.getByRole('button', { name: /^Link 3/ }).click()
      await expect(picker).toHaveCount(0)
      await expect(page.getByText('3 linked')).toBeVisible()
      expect(w.writes.at(-1)).toEqual({ method: 'POST', path: '/goals/g-new1/work', body: { node_ids: ['n-1', 'n-2', 'n-5'] } })
      // The done one folds into "1 done · show", as every done item does.
      await expect(goal.locator('.item[data-linked]')).toHaveCount(2)
      await goal.locator('.fold-btn', { hasText: 'done' }).click()
      await expect(goal.locator('.item[data-linked]')).toHaveCount(3)
      await expect(goal.locator('.goal-row')).toContainText('33')
      await noOutcome(page, 'linked goal')
      await shot(page, info, `linked-${name}`)

      // The goal's ⋯: Link work, Edit goal, Delete goal.
      if (!phone) {
        await goal.locator('.goal-row').hover()
        await goal.locator('.row-more').click()
        for (const label of ['Link work', 'Edit goal', 'Delete goal']) await expect(page.getByRole('menuitem', { name: label })).toBeVisible()
        await shot(page, info, `menu-${name}`)
        await page.keyboard.press('Escape')
      }

      // Unlink from an item's ⋯, then Undo restores it and the rollup.
      const done = goal.locator('.item[data-linked]', { hasText: 'Beacon health probes' })
      if (!phone) await done.hover()
      await done.locator('.i-more').click()
      await page.getByRole('menuitem', { name: 'Unlink from goal' }).click()
      await expect(page.getByText('PHAROS-15 unlinked')).toBeVisible()
      await expect(goal.locator('.item[data-linked]')).toHaveCount(2)
      expect(w.writes.at(-1)).toEqual({ method: 'DELETE', path: '/goals/g-new1/work', body: { node_ids: ['n-5'] } })
      await page.locator('.toast', { hasText: 'PHAROS-15 unlinked' }).getByRole('button', { name: 'Undo' }).click()
      await expect(goal.locator('.item[data-linked]')).toHaveCount(3)
      expect(w.writes.at(-1)).toEqual({ method: 'POST', path: '/goals/g-new1/work', body: { node_ids: ['n-5'] } })
      // The agent's goal keeps its own item; nothing was linked there.
      expect(w.goals.find(g => g.id === 'g-agent')!.linked).toEqual([])
      expect(errors).toEqual([])
    })
  }
}

for (const [width, height] of [[1440, 900], [390, 844]] as const) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`${width} ${colorScheme}: Rename in place moves nothing; Esc cancels, an empty name is refused, Enter saves; Delete offers Undo, no confirm`, async ({ page }, info) => {
      await page.setViewportSize({ width, height })
      await page.emulateMedia({ colorScheme })
      const errors = watchErrors(page)
      const w = world()
      w.goals.push({ id: 'g-second', title: 'Probes run from every region', project_id: 'p-pharos', linked: [], outcomes: [], eta: { source: 'none', partial: false } })
      await openGoals(page, w)
      const phone = width <= 760
      const name = `${width}-${colorScheme}`
      const goal = goalRow(page, 'g-agent'), row = goal.locator('.goal-row'), toggle = goal.locator('.goal-toggle'), field = goal.locator('.name-field')
      const openMenu = async () => { if (!phone) await row.hover(); await goal.locator('.row-more').click(); await expect(page.getByRole('menu')).toBeVisible() }

      // The ⋯ in its groups: change the goal, work, for me, state. A sheet on phones, inside the viewport.
      await openMenu()
      for (const label of ['Rename', 'Edit goal', 'Link work…', 'Collapse|Expand', 'Copy link', 'Delete goal']) await expect(page.getByRole('menuitem', { name: new RegExp(`^(${label})`) })).toBeVisible()
      await expect(page.getByRole('menu').locator('.row-menu-sep')).toHaveCount(3)
      await expect(page.getByRole('menuitem', { name: 'Delete goal' })).toHaveClass(/danger/)
      const panel = page.locator('.floating').filter({ has: page.getByRole('menu') })
      const box = (await panel.boundingBox())!
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.y).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(width + 0.5)
      expect(box.y + box.height).toBeLessThanOrEqual(height + 0.5)
      if (phone) await expect(panel).toHaveClass(/\bbottom\b/)
      // Key hints only where there are keys: the phone sheet shows none.
      const keys = page.getByRole('menu').locator('.row-menu-keys')
      await expect(keys).toHaveCount(3)
      for (const hint of await keys.all()) await (phone ? expect(hint).toBeHidden() : expect(hint).toBeVisible())
      await shot(page, info, `menu-${name}`)

      // Rename in place: the row, its controls and the next row stay put (±0.5 px).
      const guard = await controlStability(page, { row, toggle, more: goal.locator('.row-more'), lands: goal.locator('.goal-eta'), next: goalRow(page, 'g-second').locator('.goal-row') })
      await guard.check(async () => { await page.getByRole('menuitem', { name: /^Rename/ }).click(); await expect(field).toBeFocused() })
      await expect(field).toHaveValue('Fleet hosts report health on their own')
      // The field's text starts where the name's did.
      const [nameBox, fieldBox] = [(await goal.locator('.goal-name').boundingBox())!, (await field.boundingBox())!]
      expect(Math.abs(fieldBox.x + 4 - nameBox.x)).toBeLessThanOrEqual(0.5)
      await shot(page, info, `rename-${name}`)
      await guard.check(async () => { await field.fill('Not this name'); await page.keyboard.press('Escape'); await expect(field).toHaveCount(0) })
      await expect(toggle).toBeFocused()
      await expect(goal.locator('.goal-name')).toHaveText('Fleet hosts report health on their own')
      expect(w.writes.filter(write => write.method === 'PATCH')).toEqual([])

      // F2 on the focused row (keyboards), else the ⋯; an empty name stays in the field, refused.
      if (phone) { await openMenu(); await page.getByRole('menuitem', { name: /^Rename/ }).click() }
      else await page.keyboard.press('F2')
      await expect(field).toBeFocused()
      await guard.check(async () => { await field.fill('   '); await page.keyboard.press('Enter'); await expect(field).toHaveAttribute('aria-invalid', 'true') })
      await expect(field).toHaveAttribute('placeholder', 'A goal needs a name')
      await expect(field).toHaveValue('')
      expect(w.writes.filter(write => write.method === 'PATCH')).toEqual([])
      await shot(page, info, `rename-refused-${name}`)
      await guard.check(async () => { await field.fill('Fleet hosts report their own health'); await page.keyboard.press('Enter'); await expect(field).toHaveCount(0) })
      expect(w.writes.at(-1)).toEqual({ method: 'PATCH', path: '/goals/g-agent', body: { title: 'Fleet hosts report their own health' } })
      await expect(goal.locator('.goal-name')).toHaveText('Fleet hosts report their own health')
      await expect(toggle).toBeFocused()
      guard.done()
      await page.locator('.toast', { hasText: 'Goal renamed' }).getByRole('button', { name: 'Undo' }).click()
      await expect(goal.locator('.goal-name')).toHaveText('Fleet hosts report health on their own')
      expect(w.writes.at(-1)).toEqual({ method: 'PATCH', path: '/goals/g-agent', body: { title: 'Fleet hosts report health on their own' } })

      // Delete: Del on the focused row (keyboards), else the ⋯ › Delete goal. No confirm; Undo brings it back.
      if (phone) { await openMenu(); await page.getByRole('menuitem', { name: 'Delete goal' }).click() }
      else { await toggle.focus(); await page.keyboard.press('Delete') }
      await expect(goal).toHaveCount(0)
      await expect(page.locator('dialog[open], [role="alertdialog"]')).toHaveCount(0)
      expect(w.writes.at(-1)).toEqual({ method: 'DELETE', path: '/goals/g-agent', body: null })
      if (!phone) await expect(goalRow(page, 'g-second').locator('.goal-toggle')).toBeFocused()
      const deletedToast = page.locator('.toast', { hasText: 'Goal deleted' })
      await expect(deletedToast).toBeVisible()
      await shot(page, info, `deleted-${name}`)
      await deletedToast.getByRole('button', { name: 'Undo' }).click()
      await expect(goal).toHaveCount(1)
      await expect(goal.locator('.goal-name')).toHaveText('Fleet hosts report health on their own')
      expect(w.writes.at(-1)!.path).toMatch(/^\/events\/9\d\d\/undo$/)
      await noOutcome(page, 'rename and delete')
      expect(errors).toEqual([])
    })
  }
}

test('a person who may not create goals sees no New goal, and no goal offers Link work, Edit or Delete', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  await openGoals(page, w, { readOnly: true })
  await expect(page.locator('.new-goal')).toHaveCount(0)
  const goal = goalRow(page, 'g-agent')
  await goal.locator('.goal-toggle').click()
  await expect(goal.locator('.link-work')).toHaveCount(0)
  await goal.locator('.goal-row').hover()
  await goal.locator('.row-more').click()
  await expect(page.getByRole('menuitem', { name: 'Copy link' })).toBeVisible()
  for (const label of ['Rename', 'Link work', 'Edit goal', 'Delete goal']) await expect(page.getByRole('menuitem', { name: label })).toHaveCount(0)
  // Their keys do nothing either: no field, no picker, no write.
  await page.keyboard.press('Escape')
  await goal.locator('.goal-toggle').focus()
  for (const key of ['F2', 'l', 'Delete']) await page.keyboard.press(key)
  await expect(goal.locator('.name-field')).toHaveCount(0)
  await expect(page.locator('.picker.multi')).toHaveCount(0)
  await expect(goal).toHaveCount(1)
  expect(w.writes).toEqual([])
})

async function createGoal(page: Page, phone = false) {
  await (await newGoalEntry(page, phone)).click()
  await page.locator('#goal-title').fill('Managed provisioning works end to end')
  await page.locator('#goal-where').selectOption('p-pharos')
  await page.locator('#goal-go').click()
  await expect(page.locator('dialog.project-settings-sheet')).toHaveCount(0)
}

test('Link stays put across search results and a refusal, on a desktop and a phone', async ({ page }) => {
  // Desktop and phone in one case. The timeout only bounds a hang; 30s was the
  // whole budget once a disabled row made the refusal click wait forever.
  test.setTimeout(60_000)
  for (const [width, height] of [[1440, 900], [390, 844]] as const) {
    await page.setViewportSize({ width, height })
    const w = world()
    await openGoals(page, w)
    await createGoal(page, width <= 760)
    const goal = goalRow(page, 'g-new1')
    await expect(goal.locator('.link-work')).toBeFocused()
    await page.keyboard.press('Enter')
    const picker = page.locator('.picker.multi')
    await expect(picker).toBeVisible()
    await picker.locator('input').fill('PHAROS-1')
    await picker.locator('.option', { hasText: 'PHAROS-11' }).click()
    const link = picker.locator('.link-picked')
    await expect(link).toHaveAccessibleName(/^Link 1\b/)
    const [field, linkBox, body] = [(await picker.locator('.search .field').boundingBox())!, (await link.boundingBox())!, (await picker.locator('.picker-body').boundingBox())!]
    if (width > 760) {
      // On the search row: right of the field, as tall as it; the results start right under the search.
      expect(Math.abs(linkBox.y - field.y)).toBeLessThanOrEqual(0.5)
      expect(Math.abs(linkBox.height - field.height)).toBeLessThanOrEqual(0.5)
      expect(linkBox.x).toBeGreaterThanOrEqual(field.x + field.width)
      expect(body.y - (field.y + field.height)).toBeLessThanOrEqual(8.5)
    } else {
      // Phones keep it pinned under the results.
      expect(linkBox.y).toBeGreaterThanOrEqual(body.y + body.height)
    }
    const guard = await controlStability(page, { link })
    await guard.check(async () => {
      await picker.locator('input').fill('PHAROS-12')
      await expect(picker.locator('.option', { hasText: 'PHAROS-12' })).toBeVisible()
    })
    await guard.check(async () => {
      await picker.locator('input').fill('zzz-no-such-ticket')
      await expect(picker.getByText(/Nothing matches/)).toBeVisible()
    })
    guard.done()
    await link.click()
    await expect(picker).toHaveCount(0)
    await goal.locator('.link-work').click()
    const again = page.locator('.picker.multi')
    await expect(again).toBeVisible()
    await again.locator('input').fill('PHAROS-11')
    const linked = again.locator('.option', { hasText: 'PHAROS-11' })
    await expect(linked).toBeVisible()
    // Pick mode keeps the row enabled so a click can say it is already linked.
    // aria-disabled would leave this click waiting until the test times out.
    await expect(linked).not.toHaveAttribute('aria-disabled', 'true')
    const held = await controlStability(page, { link: again.locator('.link-picked') })
    await held.check(async () => {
      await linked.click()
      await expect(again.locator('.refusal')).toContainText('PHAROS-11 is already linked to this goal.')
    })
    expect(w.writes.filter(write => write.path.endsWith('/work'))).toHaveLength(1)
    held.done()
    await page.keyboard.press('Escape')
  }
})

// "Link {n}" is right-aligned and reserves its widest label: picking from 0 to 10
// (9 → 10 adds a digit) and back never changes its size or place.
test('Link keeps its size and place while picking from 0 to 10 and back, on a desktop and a phone', async ({ page }) => {
  test.setTimeout(90_000)
  for (const [width, height] of [[1440, 900], [390, 844]] as const) {
    await page.setViewportSize({ width, height })
    const w = world()
    for (const n of [17, 18, 19, 20, 21, 22]) {
      w.data.nodes.push({ id: `n-x${n}`, key: `PHAROS-${n}`, kind_slug: 'ticket', title: `Probe region number ${n - 16}`, state: 'backlog', project: 'p-pharos', parent_id: 'p-pharos', body: '', fields: {}, created_at: iso(NOW - 400 * HOUR), updated_at: iso(NOW - n * HOUR) })
    }
    await openGoals(page, w)
    await createGoal(page, width <= 760)
    await goalRow(page, 'g-new1').locator('.link-work').click()
    const picker = page.locator('.picker.multi')
    const link = picker.locator('.link-picked')
    await expect(link).toHaveAccessibleName(/^Link 0\b/)
    const guard = await controlStability(page, { link })
    const pick = async (key: string, count: number) => guard.check(async () => {
      await picker.locator('.option', { hasText: key }).click()
      await expect(link).toHaveAccessibleName(new RegExp(`^Link ${count}\\b`))
    })
    await picker.locator('input').fill('PHAROS-1')
    await expect(picker.locator('.option', { hasText: 'PHAROS-17' })).toBeVisible()
    let count = 0
    for (const n of [11, 12, 13, 14, 15, 16, 17]) await pick(`PHAROS-${n}`, ++count)
    await guard.check(async () => {
      await picker.locator('input').fill('PHAROS-2')
      await expect(picker.locator('.option', { hasText: 'PHAROS-22' })).toBeVisible()
    })
    for (const n of [20, 21, 22]) await pick(`PHAROS-${n}`, ++count)
    expect(count).toBe(10)
    // 10 → 9 → 8: unpicking moves nothing either.
    await pick('PHAROS-22', 9)
    await pick('PHAROS-21', 8)
    guard.done()
    await page.keyboard.press('Escape')
    await expect(picker).toHaveCount(0)
    expect(w.writes.filter(write => write.path.endsWith('/work'))).toHaveLength(0)
  }
})

test('a Lands day reads as a day in the row, its label and its history; a goal without work says so at the same height', async ({ page }, info) => {
  test.setTimeout(60_000)
  for (const [width, height] of [[1440, 900], [390, 844]] as const) {
    await page.setViewportSize({ width, height })
    const phone = width <= 760
    const w = world()
    await openGoals(page, w)
    await (await newGoalEntry(page, phone)).click()
    await page.locator('#goal-title').fill('Managed provisioning works end to end')
    await page.locator('dialog input.native[type="date"]').evaluate((input: HTMLInputElement) => { input.value = '2026-10-30'; input.dispatchEvent(new Event('change', { bubbles: true })) })
    await page.locator('#goal-go').click()
    const goal = goalRow(page, 'g-new1'), row = goal.locator('.goal-row'), lands = goal.locator('.goal-eta')
    await expect(goal).toHaveAttribute('data-open', '1')

    // A day stays a day, like the agent's "Fri 23 Oct" beside it.
    await expect(lands.locator('.t')).toHaveText('Fri 30 Oct')
    await expect(lands).toHaveAttribute('aria-label', /^Fri 30 Oct\b/)
    await expect(goalRow(page, 'g-agent').locator('.goal-eta .t')).toHaveText('Fri 23 Oct')

    // No work yet: an empty track, no percent, "No work linked yet" where "N of M done" goes.
    const progress = goal.locator('.goal-progress')
    await expect(progress.locator('.of')).toHaveText('No work linked yet')
    await expect(progress.locator('.pct')).toHaveText('')
    await expect(progress).not.toContainText('%')
    await expect(progress.locator('.bar > i')).toHaveCount(0)
    await shot(page, info, `no-work-${width}`)
    const before = (await row.boundingBox())!.height

    // Linking work brings the percent and the count, and the row keeps its height (0 px).
    await goal.locator('.link-work').click()
    const picker = page.locator('.picker.multi')
    await picker.locator('input').fill('PHAROS-11')
    await picker.locator('.option', { hasText: 'PHAROS-11' }).click()
    await picker.locator('.link-picked').click()
    await expect(picker).toHaveCount(0)
    await expect(progress.locator('.pct')).toHaveText(/^\d+%$/)
    if (!phone) await expect(progress.locator('.of')).toHaveText(/^\d+ of 1 done$/)
    expect(Math.abs((await row.boundingBox())!.height - before)).toBeLessThanOrEqual(0.5)

    // Another day through Edit goal: sent as a day, and its history says what it was.
    if (!phone) await row.hover()
    await goal.locator('.row-more').click()
    await page.getByRole('menuitem', { name: 'Edit goal' }).click()
    await page.locator('dialog input.native[type="date"]').evaluate((input: HTMLInputElement) => { input.value = '2026-10-31'; input.dispatchEvent(new Event('change', { bubbles: true })) })
    await page.locator('#goal-go').click()
    await expect(page.locator('dialog.project-settings-sheet')).toHaveCount(0)
    const patch = w.writes.filter(write => write.method === 'PATCH').at(-1)!.body as { eta: { at: string; text: string } }
    expect(patch.eta.text).toBe('Sat 31 Oct')
    expect(new Date(patch.eta.at).toISOString()).toBe('2026-10-31T22:59:00.000Z')
    await expect(lands.locator('.t')).toHaveText('Sat 31 Oct')
    await lands.hover()
    const tip = page.locator('.goal-tip')
    await expect(tip).toContainText('was Fri 30 Oct')
    await shot(page, info, `lands-tip-${width}`)
    for (const text of [await lands.innerText(), await lands.getAttribute('aria-label') ?? '', await tip.innerText()]) expect(text).not.toContain('23:59')
    await page.mouse.move(0, 0)
  }
})

test('a created goal is shown through search, the Done filter and a one-goal page', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  await openGoals(page, w)
  await page.locator('.project-search input').fill('fleet')
  await expect(goalRow(page, 'g-agent')).toBeVisible()
  await page.locator('.state-seg [data-state="done"]').click()
  await expect(goalRow(page, 'g-agent')).toHaveCount(0)
  await createGoal(page)
  const goal = goalRow(page, 'g-new1')
  await expect(page.locator('.project-search input')).toHaveValue('')
  await expect(page.locator('.state-seg [data-state="all"]')).toHaveAttribute('aria-checked', 'true')
  await expect(goal).toHaveAttribute('data-open', '1')
  await expect(goal.locator('.link-work')).toBeFocused()

  const paged = world()
  paged.pageLimit = 1
  await openGoals(page, paged)
  await createGoal(page)
  const created = goalRow(page, 'g-new1')
  await expect(created).toHaveAttribute('data-open', '1')
  await expect(created.locator('.link-work')).toBeFocused()
  expect(paged.goals.map(g => g.id)).toEqual(['g-agent', 'g-new1'])
})

test('Undo of a stale picker removes only the ids that call added', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  await openGoals(page, w)
  await createGoal(page)
  const goal = goalRow(page, 'g-new1')
  await goal.locator('.link-work').click()
  const picker = page.locator('.picker.multi')
  await picker.locator('input').fill('PHAROS-1')
  await picker.locator('.option', { hasText: 'PHAROS-11' }).click()
  await picker.locator('.option', { hasText: 'PHAROS-12' }).click()
  // Another client linked PHAROS-11 after this picker read the goal.
  w.goals.find(g => g.id === 'g-new1')!.linked.push('n-1')
  await picker.getByRole('button', { name: /^Link 2/ }).click()
  await expect(page.getByText('1 linked')).toBeVisible()
  await page.locator('.toast', { hasText: '1 linked' }).getByRole('button', { name: 'Undo' }).click()
  expect(w.writes.at(-1)).toEqual({ method: 'DELETE', path: '/goals/g-new1/work', body: { node_ids: ['n-2'] } })
  expect(w.goals.find(g => g.id === 'g-new1')!.linked).toEqual(['n-1'])
})
