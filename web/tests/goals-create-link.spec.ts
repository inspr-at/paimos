// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1618 (Tick spec v1): people create a goal and link work to it. Risks: the
// toolbar or the rows move when the New goal sheet opens or closes; the new goal
// is not opened with Link work focused; the picker links something else than the
// three items picked, or the rollup ignores the links; Unlink or its Undo loses
// work; the word "outcome" reaches people; a person who may not create goals sees
// the button. Screenshots go to each test's output directory.
import { test, expect, type Page, type Locator, type TestInfo } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: 'Europe/Vienna', locale: 'en-GB' })
const NOW = Date.parse('2026-10-21T08:00:00Z')
const HOUR = 3_600_000
const iso = (ms: number) => new Date(ms).toISOString()
test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

type Item = Record<string, unknown> & { id: string; keys: string[]; state: string; linked?: boolean }
type GoalMock = Record<string, unknown> & { id: string; title: string; project_id: string | null; linked: string[]; outcomes: Item[]; eta: Record<string, unknown> }
function world() {
  const data = fixtures()
  const agentGoal: GoalMock = {
    id: 'g-agent', title: 'Fleet hosts report health on their own', project_id: 'p-pharos', linked: [], eta: { source: 'agent', text: 'Fri 23 Oct', at: '2026-10-23T15:00:00Z', partial: false },
    outcomes: [{ id: '00000000-0000-4000-8000-000000000001', ref: 'probes', label: 'Probes report every minute', note: '', position: 1, keys: ['PHAROS-14'], state: 'queued', session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 0, total_leaves: 1 }, weight: 1, handed_over: false }],
  }
  return { data, goals: [agentGoal], writes: [] as { method: string; path: string; body: unknown }[], next: 1 }
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
  const written = (route: Parameters<Parameters<Page['route']>[1]>[0], extra: Record<string, unknown> = {}) => route.fulfill({ json: { changed: true, read_through: 10 + w.writes.length, generation: 1 + w.writes.length, ...extra }, headers: { 'Aeon-Contract': 'goals/1.6' } })
  await page.route(/\/api\/goals(\?.*)?$/, async route => {
    const request = route.request()
    if (request.method() === 'POST') {
      const body = request.postDataJSON() as { title: string; project_id: string | null; eta?: { at: string } }
      w.writes.push({ method: 'POST', path: '/goals', body })
      const id = `g-new${w.next++}`
      w.goals.push({ id, title: body.title, project_id: body.project_id, linked: [], outcomes: [], eta: body.eta ? { source: 'agent', at: body.eta.at, partial: false } : { source: 'none', partial: false } })
      return written(route, { id, key: id.toUpperCase() })
    }
    const items = w.goals.map(g => shape(w, g))
    const totals = { moving: 0, queued: 0, paused: 0, done: 0 }
    for (const g of items) for (const s of ['moving', 'queued', 'paused', 'done'] as const) totals[s] += g.states[s]
    return route.fulfill({ json: { items, totals, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 10 + w.writes.length, generation: 1 + w.writes.length }, headers: { 'Aeon-Contract': 'goals/1.6' } })
  })
  await page.route(/\/api\/goals\/g-[a-z0-9]+\/work$/, async route => {
    const request = route.request(), id = new URL(request.url()).pathname.split('/')[3]!
    const goal = w.goals.find(g => g.id === id)!, body = request.postDataJSON() as { node_ids: string[] }
    w.writes.push({ method: request.method(), path: `/goals/${id}/work`, body })
    if (request.method() === 'POST') for (const node of body.node_ids) { if (!goal.linked.includes(node)) goal.linked.push(node) }
    else goal.linked = goal.linked.filter(node => !body.node_ids.includes(node))
    return written(route, { id, key: id.toUpperCase(), node_ids: [...goal.linked], event_id: 40 + w.writes.length })
  })
  await page.route(/\/api\/goals\/[^/]+\/series\?/, route => route.fulfill({ json: { range: '24h', expansion: 'full', points: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/goals\/[^/]+\/history/, route => route.fulfill({ json: { items: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/nodes\/g-[a-z0-9]+$/, route => route.fulfill({ json: { id: 'g', key: 'G', kind_id: 'k-goal', title: 'Goal', body: '', fields: {}, state: 'open', parent_id: null, position: 1, created_at: iso(NOW - 300 * HOUR), updated_at: iso(NOW) } }))
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
      await expect(sheet).toContainText('Optional. Progress fills in from linked work.')
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
      const created = w.writes.find(write => write.method === 'POST' && write.path === '/goals')!.body as { title: string; project_id: string; eta: { at: string } }
      expect(created).toMatchObject({ title: 'Managed provisioning works end to end', project_id: 'p-pharos' })
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
  for (const label of ['Link work', 'Edit goal', 'Delete goal']) await expect(page.getByRole('menuitem', { name: label })).toHaveCount(0)
})
