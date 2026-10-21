// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1614: Goals › Hide for me and Display › Show, to Tick's spec v1 + v1.1 + v1.2.
// Risks: a hidden goal that stays on screen, or an Undo that puts it elsewhere; a
// Display switch that does not persist across reloads, or moves the toolbar or its
// neighbours; hidden goals that come back without the muted "Hidden" word or without
// Unhide; Waiting on you that paints and then disappears, or takes the header's Desk
// count with it; H that stops toggling Done goals; phones without the Show rows or a
// way to hide; a toolbar Waiting on you toggle that disagrees with Display › Show,
// loses its count of open Desk items while the band is hidden, or moves the toolbar
// when that count appears or changes; phones without that count in the ⋯ row.
// The mock server keeps one hidden set, as the API does. Screenshots and
// one checks.json line per assert go to each test's output directory under AEON-1614/.
import { appendFileSync, mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { test, expect, type Page, type TestInfo } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'

const LOCALE = 'en-GB'
test.use({ timezoneId: 'Europe/Vienna', locale: LOCALE })
const NOW = Date.parse('2026-10-21T08:00:00Z')
const iso = (ms: number) => new Date(ms).toISOString()
test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

type State = 'moving' | 'queued' | 'paused' | 'done'
function goal(id: string, title: string, state: State) {
  const states = { moving: 0, queued: 0, paused: 0, done: 0 }
  states[state] = 1
  return {
    id, key: id.toUpperCase(), title, kind: 'goal', project_id: 'p-aeon', workspace: false, expansion: 'full', counts: { outcomes: 1, tickets: 1 },
    items: [], steps: [], next_step: null, projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 0, question_ids: [] },
    progress: { pct: state === 'done' ? 100 : 40, computed_pct: 40, source: 'computed', basis: 'count', total: 1, done: state === 'done' ? 1 : 0, items_without_estimate: 0, items_partially_estimated: 0 },
    eta: { source: 'none', partial: false }, passed: false, states, done: state === 'done', read_through: 10, generation: 1,
  }
}
function world() {
  const data = fixtures()
  const goals = [goal('g-r138', 'Release 138 · hide what is not relevant', 'moving'), goal('g-joed', 'JoeDesk rebuild', 'queued'), goal('g-crm', 'CRM import from the old system', 'queued'), goal('g-done', 'Theme picker', 'done')]
  return { data, goals, hidden: new Set<string>(), generation: 1, writes: [] as string[], desk: 1 }
}
type World = ReturnType<typeof world>

async function mock(page: Page, w: World) {
  await page.addInitScript(() => {
    class QuietStream extends EventTarget {
      readyState = 0
      onerror: ((event: Event) => void) | null = null
      onopen: ((event: Event) => void) | null = null
      constructor() { super(); queueMicrotask(() => { this.readyState = 1; this.onopen?.(new Event('open')) }) }
      close() { this.readyState = 2 }
    }
    Object.assign(window, { EventSource: QuietStream })
  })
  await mockWork(page, w.data)
  await page.route(/\/api\/goals(\?.*)?$/, route => {
    const url = new URL(route.request().url())
    const include = url.searchParams.get('hidden') === 'include', hideDone = url.searchParams.get('hide_done') === 'true'
    const totals = { moving: 0, queued: 0, paused: 0, done: 0 }
    for (const g of w.goals) for (const s of Object.keys(totals) as State[]) totals[s] += g.states[s]
    const items = w.goals.filter(g => (include || !w.hidden.has(g.id)) && !(hideDone && g.done)).map(g => w.hidden.has(g.id) ? { ...g, hidden: true } : g)
    return route.fulfill({ json: { items, totals, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 10, generation: ++w.generation, hidden_count: w.hidden.size }, headers: { 'Aeon-Contract': 'goals/1.6' } })
  })
  await page.route(/\/api\/me\/goals\/[^/]+\/hidden$/, route => {
    const id = decodeURIComponent(route.request().url().split('/me/goals/')[1]!.split('/')[0]!), hide = route.request().method() === 'PUT'
    const changed = hide ? !w.hidden.has(id) : w.hidden.has(id)
    if (hide) w.hidden.add(id); else w.hidden.delete(id)
    w.writes.push(`${route.request().method()} ${id}`)
    return route.fulfill({ json: { goal_id: id, key: id.toUpperCase(), hidden: hide, changed } })
  })
  await page.route(/\/api\/goals\/[^/]+\/series\?/, route => route.fulfill({ json: { range: '24h', expansion: 'full', points: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/nodes\/g-[a-z0-9]+$/, route => route.fulfill({ json: { id: 'g', key: 'G', kind_id: 'k-goal', title: 'Goal', body: '', fields: {}, state: 'open', parent_id: null, position: 1, created_at: iso(NOW - 100 * 3_600_000), updated_at: iso(NOW) } }))
  // Open Desk questions (one unless a test changes w.desk), so Waiting on you is a real band and the header shows the count.
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: {
    items: Array.from({ length: w.desk }, (_, i) => ({ id: `q-${i + 1}`, kind: 'question', project_id: 'p-aeon', revision: 1, title: i ? `Open question ${i + 1}` : 'Ship the hide switch in r138?', created_at: iso(NOW - 3_600_000), held: false, href: '/decision-desk', source: 'question' })),
    counts: { open: w.desk, held: 0, chores: 0 }, has_more: false, as_of: iso(NOW),
  } }))
}

const theme = (dark: boolean) => dark ? 'dark' : 'light'
const ids = (page: Page) => page.locator('.goal-list > .goal').evaluateAll(rows => rows.map(row => row.getAttribute('data-goal-id')))
async function shot(page: Page, info: TestInfo, width: number, dark: boolean, state: string) {
  await page.screenshot({ path: info.outputPath(`AEON-1614/aeon1614-${width}-${theme(dark)}-${state}.png`), fullPage: true })
}
function check(info: TestInfo, width: number, dark: boolean, name: string, value: unknown) {
  const file = info.outputPath('AEON-1614/checks.json')
  mkdirSync(dirname(file), { recursive: true })
  appendFileSync(file, `${JSON.stringify({ ticket: 'AEON-1614', assert: name, width, theme: theme(dark), locale: LOCALE, value, pass: true })}\n`)
}
async function openGoals(page: Page, w: World) {
  await mock(page, w)
  await page.goto('/?view=goals')
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
}
async function rowMenu(page: Page, id: string) {
  const row = page.locator(`.goal[data-goal-id="${id}"] .goal-row`)
  await row.hover()
  await row.locator('.row-more').click()
  return page.getByRole('menu')
}

for (const dark of [false, true]) {
  test(`1440 ${theme(dark)}: Hide for me, Undo, Display › Show (Done goals, Hidden goals, Waiting on you), Unhide`, async ({ page }, info) => {
    const w = world()
    await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
    await page.setViewportSize({ width: 1440, height: 1000 })
    await openGoals(page, w)
    const before = await ids(page)
    expect(before).toEqual(['g-r138', 'g-joed', 'g-crm'])
    // The separate Hide done button is gone; the toolbar keeps the chips, Expand all and Display.
    await expect(page.locator('.projects-toolbar .toggle-btn')).toHaveCount(0)
    const toolbar = { search: page.locator('.project-search'), chips: page.locator('.state-seg'), expand: page.locator('.expand-btn'), display: page.locator('.display-btn') }
    const guard = await controlStability(page, toolbar)

    // ⋯ › Hide for me → the row leaves, the toast offers Undo.
    const menu = await rowMenu(page, 'g-joed')
    await expect(menu.getByRole('menuitem', { name: 'Hide for me' })).toBeVisible()
    if (!dark) await shot(page, info, 1440, dark, 'row-menu')
    await guard.check(() => menu.getByRole('menuitem', { name: 'Hide for me' }).click())
    await expect(page.locator('.goal[data-goal-id="g-joed"]')).toHaveCount(0)
    const toast = page.locator('.toast').filter({ hasText: 'Goal hidden' })
    await expect(toast.getByRole('button', { name: 'Undo' })).toBeVisible()
    if (!dark) await shot(page, info, 1440, dark, 'toast-undo')
    // Undo puts the goal back in its place.
    await guard.check(() => toast.getByRole('button', { name: 'Undo' }).click())
    await expect.poll(() => ids(page)).toEqual(before)
    expect(w.writes).toEqual(['PUT g-joed', 'DELETE g-joed'])
    check(info, 1440, dark, 'undo restores the goal in place', before)

    // Hide it again; Display › Show counts it and shows it on request, muted, with the word Hidden.
    await (await rowMenu(page, 'g-joed')).getByRole('menuitem', { name: 'Hide for me' }).click()
    await expect(page.locator('.goal[data-goal-id="g-joed"]')).toHaveCount(0)
    await page.locator('.display-btn').click()
    const show = page.locator('.display-panel .show-rows')
    const done = show.locator('[data-show="done"]'), hidden = show.locator('[data-show="hidden"]'), waiting = show.locator('[data-show="waiting"]')
    await expect(show.locator('.show-label')).toHaveText('Show')
    await expect(done).toContainText('Done goals')
    await expect(hidden).toContainText('Hidden goals (1)')
    await expect(hidden).toBeEnabled()
    await expect(waiting).toContainText('Waiting on you')
    await expect(done).toHaveAttribute('aria-checked', 'false')
    await expect(waiting).toHaveAttribute('aria-checked', 'true')
    await shot(page, info, 1440, dark, 'display-open')
    const panel = await controlStability(page, { ...toolbar, done, hidden, waiting })
    await panel.check(() => hidden.click())
    await expect(hidden).toHaveAttribute('aria-checked', 'true')
    const muted = page.locator('.goal[data-goal-id="g-joed"]')
    await expect(muted).toHaveAttribute('data-hidden', '1')
    await expect(muted.locator('.hidden-mark')).toHaveText('Hidden')
    await panel.check(() => done.click())
    await expect(page.locator('.goal[data-goal-id="g-done"]')).toBeVisible()
    await panel.check(() => done.click())
    await expect(page.locator('.goal[data-goal-id="g-done"]')).toHaveCount(0)
    panel.done()
    await page.keyboard.press('Escape')
    if (!dark) await shot(page, info, 1440, dark, 'hidden-shown')
    check(info, 1440, dark, 'hidden goal shown muted with the word Hidden', true)

    // H toggles Done goals with the panel closed (focus is back on Display, outside any field).
    await expect(page.locator('.display-panel')).toHaveCount(0)
    await guard.check(() => page.keyboard.press('h'))
    await expect(page.locator('.goal[data-goal-id="g-done"]')).toBeVisible()
    await guard.check(() => page.keyboard.press('h'))
    await expect(page.locator('.goal[data-goal-id="g-done"]')).toHaveCount(0)
    guard.done()

    // The switches persist across a reload (the person's preferences on the server).
    await page.reload()
    await expect(page.locator('.goal[data-goal-id="g-joed"]')).toHaveAttribute('data-hidden', '1')
    // ⋯ › Unhide brings it back as a normal row.
    await (await rowMenu(page, 'g-joed')).getByRole('menuitem', { name: 'Unhide' }).click()
    await expect(page.locator('.goal[data-goal-id="g-joed"]')).not.toHaveAttribute('data-hidden', '1')
    expect(w.hidden.size).toBe(0)
    check(info, 1440, dark, 'switches persist across reload; Unhide restores', true)
  })
}

test('1440 light: Waiting on you off leaves no band, keeps the header Desk count, and stays off from the first paint', async ({ page }, info) => {
  const w = world()
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openGoals(page, w)
  await expect(page.locator('.goals-view .waiting')).toHaveCount(1)
  const badge = page.locator('.needs-badge').first()
  const count = await badge.textContent()
  const toolbar = { search: page.locator('.project-search'), chips: page.locator('.state-seg'), display: page.locator('.display-btn') }
  const guard = await controlStability(page, toolbar)
  await page.locator('.display-btn').click()
  await guard.check(() => page.locator('.display-panel [data-show="waiting"]').click())
  await expect(page.locator('.display-panel [data-show="waiting"]')).toHaveAttribute('aria-checked', 'false')
  await shot(page, info, 1440, false, 'display-three-switches')
  await page.keyboard.press('Escape')
  await expect(page.locator('.goals-view .waiting')).toHaveCount(0)
  await expect(badge).toHaveText(count ?? '')
  guard.done()
  // After a reload the band never appears, not even for a frame.
  await page.addInitScript(() => {
    const seen = { band: false }
    Object.assign(window, { __band: seen })
    new MutationObserver(() => { if (document.querySelector('.goals-view .waiting')) seen.band = true }).observe(document, { subtree: true, childList: true })
  })
  await page.reload()
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
  expect(await page.evaluate(() => (window as unknown as { __band: { band: boolean } }).__band.band)).toBe(false)
  await expect(badge).toHaveText(count ?? '')
  await shot(page, info, 1440, false, 'band-off')
  check(info, 1440, false, 'Waiting on you off: no band from the first paint, Desk badge unchanged', count)

  // Everything hidden: the empty state points to Display, and the link opens it.
  for (const g of w.goals) w.hidden.add(g.id)
  await page.reload()
  const empty = page.locator('.goals-view .state')
  await expect(empty.locator('h2')).toHaveText('All goals here are hidden for you.')
  await expect(empty.locator('.display-hint')).toHaveText('Show them in Display.')
  await shot(page, info, 1440, false, 'all-hidden')
  await empty.getByRole('button', { name: 'Display' }).click()
  await expect(page.locator('.display-panel .show-rows')).toBeVisible()
  check(info, 1440, false, 'all-hidden empty state links to Display', true)
})

for (const dark of [false, true]) {
test(`390 ${theme(dark)}: the ⋯ Goal display menu has the three Show rows; the opened goal hides, Undo restores, Hidden goals shows it and Unhide brings it back`, async ({ page }, info) => {
  const w = world()
  await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
  await page.setViewportSize({ width: 390, height: 844 })
  await openGoals(page, w)
  const before = await ids(page)
  await page.locator('.more-btn').click()
  const menu = page.getByRole('menu', { name: 'Goal display' })
  for (const [key, label] of [['done', 'Done goals'], ['hidden', 'Hidden goals (0)'], ['waiting', 'Waiting on you']] as const) {
    const rowItem = menu.locator(`[data-show="${key}"]`)
    await expect(rowItem).toContainText(label)
    await expect(rowItem).toHaveAttribute('role', 'menuitemcheckbox')
    expect((await rowItem.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  }
  // No hidden goals yet: the row is disabled in place.
  await expect(menu.locator('[data-show="hidden"]')).toBeDisabled()
  await shot(page, info, 390, dark, 'goal-display-menu')
  await page.keyboard.press('Escape')
  // Phones have no row ⋯: the opened goal ends with Hide for me.
  await page.locator('.goal[data-goal-id="g-crm"] .goal-toggle').click()
  const hide = page.locator('.goal[data-goal-id="g-crm"] .hide-btn')
  await expect(hide).toHaveText('Hide for me')
  expect((await hide.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  await hide.click()
  await expect(page.locator('.goal[data-goal-id="g-crm"]')).toHaveCount(0)
  await page.locator('.toast').filter({ hasText: 'Goal hidden' }).getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => ids(page)).toEqual(before)
  // Hide again, show hidden goals from the ⋯ menu: the row is muted and its opened goal offers Unhide.
  await page.locator('.goal[data-goal-id="g-crm"] .hide-btn').click()
  await expect(page.locator('.goal[data-goal-id="g-crm"]')).toHaveCount(0)
  await page.locator('.more-btn').click()
  await expect(menu.locator('[data-show="hidden"]')).toContainText('Hidden goals (1)')
  await menu.locator('[data-show="hidden"]').click()
  await expect(menu.locator('[data-show="hidden"]')).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('Escape')
  const muted = page.locator('.goal[data-goal-id="g-crm"]')
  await expect(muted).toHaveAttribute('data-hidden', '1')
  await expect(muted.locator('.hidden-mark')).toHaveText('Hidden')
  if (!(await muted.getAttribute('data-open') === '1')) await muted.locator('.goal-toggle').click()
  await shot(page, info, 390, dark, 'hidden-shown')
  await muted.locator('.hide-btn').click()
  await expect(muted).not.toHaveAttribute('data-hidden', '1')
  expect(w.hidden.size).toBe(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
  check(info, 390, dark, 'phone menu rows; opened-goal hide, Undo, show hidden, Unhide', true)
})
}

// v1.2: the toolbar toggle and Display › Show › Waiting on you are one setting.
for (const dark of [false, true]) {
  test(`1440 ${theme(dark)}: the toolbar's Waiting on you toggle hides and shows the band, Display › Show follows, and the count stays without moving anything`, async ({ page }, info) => {
    const w = world()
    await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
    await page.setViewportSize({ width: 1440, height: 1000 })
    await openGoals(page, w)
    const toggle = page.locator('.projects-toolbar .wait-toggle'), count = toggle.locator('.wait-count'), band = page.locator('.goals-view .waiting')
    const view = page.locator('.projects-toolbar .view-seg'), display = page.locator('.display-btn')
    // Just left of the view controls, centred on their line.
    const [t, v] = [(await toggle.boundingBox())!, (await view.boundingBox())!]
    expect(t.x + t.width).toBeLessThanOrEqual(v.x)
    expect(v.x - (t.x + t.width)).toBeLessThanOrEqual(12)
    expect(Math.abs(t.y + t.height / 2 - (v.y + v.height / 2))).toBeLessThanOrEqual(0.5)
    await expect(toggle).toHaveAttribute('aria-pressed', 'true')
    await expect(toggle).toHaveAttribute('aria-label', 'Waiting on you, 1 open')
    await expect(toggle).toHaveAttribute('data-tip', 'Waiting on you · 1 open')
    await expect(count).toHaveText('1')
    await expect(band).toHaveCount(1)
    await shot(page, info, 1440, dark, 'waiting-toggle-pressed')
    const guard = await controlStability(page, { search: page.locator('.project-search'), chips: page.locator('.state-seg'), expand: page.locator('.expand-btn'), toggle, view, display })

    // Off from the toolbar: no band, the count stays on the icon, Display › Show says off.
    await guard.check(() => toggle.click())
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
    await expect(band).toHaveCount(0)
    await expect(count).toBeVisible()
    await expect(count).toHaveText('1')
    await shot(page, info, 1440, dark, 'waiting-toggle-unpressed')
    await display.click()
    const sw = page.locator('.display-panel [data-show="waiting"]')
    await expect(sw).toHaveAttribute('aria-checked', 'false')
    // On from Display › Show: the toolbar toggle follows.
    await guard.check(() => sw.click())
    await expect(sw).toHaveAttribute('aria-checked', 'true')
    await expect(toggle).toHaveAttribute('aria-pressed', 'true')
    await page.keyboard.press('Escape')
    await expect(band).toHaveCount(1)
    check(info, 1440, dark, 'toolbar toggle and Display › Show stay in sync; count visible while the band is hidden', true)

    // The count changes and goes away with the Desk (the header's poll) and moves nothing.
    w.desk = 2
    await guard.check(async () => { await page.clock.runFor(31_000); await expect(count).toHaveText('2') })
    await expect(toggle).toHaveAttribute('aria-label', 'Waiting on you, 2 open')
    w.desk = 0
    await guard.check(async () => { await page.clock.runFor(31_000); await expect(count).toHaveCount(0) })
    guard.done()
    check(info, 1440, dark, 'count appears, changes and goes without moving the toolbar', 'movementPx 0')

    // The setting is the person's: off survives a reload, in both controls.
    w.desk = 1
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
    await page.reload()
    await expect(page.locator('.goal-list .goal').first()).toBeVisible()
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
    await expect(band).toHaveCount(0)
    await expect(count).toHaveText('1')
  })

  test(`390 ${theme(dark)}: the toolbar toggle steps aside; the ⋯ row reads "Waiting on you · 1", hides and shows the band, and keeps the count while hidden`, async ({ page }, info) => {
    const w = world()
    await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
    await page.setViewportSize({ width: 390, height: 844 })
    await openGoals(page, w)
    const band = page.locator('.goals-view .waiting')
    await expect(page.locator('.projects-toolbar .wait-toggle')).toBeHidden()
    await expect(band).toHaveCount(1)
    await page.locator('.more-btn').click()
    const menu = page.getByRole('menu', { name: 'Goal display' })
    const row = menu.locator('[data-show="waiting"]')
    await expect(row).toHaveText(/Waiting on you\s·\s1/)
    await expect(row).toHaveAttribute('aria-checked', 'true')
    await row.click()
    await expect(row).toHaveAttribute('aria-checked', 'false')
    await expect(row).toHaveText(/Waiting on you\s·\s1/)
    await shot(page, info, 390, dark, 'waiting-row-off')
    await page.keyboard.press('Escape')
    await expect(band).toHaveCount(0)
    await page.locator('.more-btn').click()
    await expect(row).toHaveAttribute('aria-checked', 'false')
    await row.click()
    await expect(row).toHaveAttribute('aria-checked', 'true')
    await page.keyboard.press('Escape')
    await expect(band).toHaveCount(1)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
    check(info, 390, dark, 'phone ⋯ row carries the count and toggles the band', true)
  })
}
