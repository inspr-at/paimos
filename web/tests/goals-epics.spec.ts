// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1395: Goals › Display › Group: Goals · Epics, to Tick's spec v1 (states E1–E6).
// Risks: a group switch that moves the toolbar, a state chip, the Display trigger or a
// header cell (GOAL and EPIC must share one box); an opened epic whose "what's missing"
// leaves the Definitions order, shows a tally or an empty heading; epic items that lose
// the AEON-1494 nesting and widths, their live line (agent note or pull request) or the
// folds; a pen or a chart on a computed percent; a group that is not kept per person
// across a reload, or a ?group= link that rewrites the person's choice; an empty state
// with examples; phones that lose the 44 px targets or overflow. Screenshots and the
// checks (one JSON line per assert, with width, theme and locale) go to each test's
// output directory under AEON-1395/.
import { appendFileSync, mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { test, expect, type Page, type Locator, type TestInfo } from '@playwright/test'
import { fixtures, liveAgent, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

const LOCALE = 'en-GB'
test.use({ timezoneId: 'Europe/Vienna', locale: LOCALE })
const NOW = Date.parse('2026-10-19T08:00:00Z') // 10:00 in Vienna
const HOUR = 3_600_000, MIN = 60_000
const iso = (ms: number) => new Date(ms).toISOString()
const NB = ' '

test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

// ---------- The world: two goals for Group › Goals, two open epics for Group › Epics ----------
type State = 'moving' | 'queued' | 'paused' | 'done'
let next = 0
function item(label: string, state: State, extra: Record<string, unknown> = {}) {
  next++
  return { id: `00000000-0000-4000-8000-${String(next).padStart(12, '0')}`, ref: `o${next}`, label, note: '', position: next, keys: [`AEON-${1529 + next}`], state, session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 1, total_leaves: 1 }, weight: 1, handed_over: false, ...extra }
}
type Item = ReturnType<typeof item>
function area(id: string, kind: 'goal' | 'release' | 'epic', title: string, items: Item[], extra: Record<string, unknown> = {}) {
  const states = { moving: 0, queued: 0, paused: 0, done: 0 }
  for (const outcome of items) states[outcome.state as State]++
  return {
    id, key: id.toUpperCase(), title, kind, project_id: 'p-aeon', workspace: false, expansion: 'full', counts: { outcomes: items.length, tickets: items.length },
    items, steps: [], next_step: null, projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 0, question_ids: [] },
    progress: { pct: 0, computed_pct: 0, source: 'computed', basis: 'estimate', total: items.length, done: states.done, items_without_estimate: 0, items_partially_estimated: 0, remaining_hours: 0, remaining_unestimated: 0 },
    eta: { source: 'none', partial: false }, passed: false, states, done: false, read_through: 10, generation: 1, ...extra,
  }
}
type Area = ReturnType<typeof area>
// Tick's examples: the Definitions labels verbatim, a person's step with its owner.
const MISSING = [
  { id: 'estimate-set', text: 'Estimate set', state: 'now' },
  { id: 'visual-check-ok', text: 'Visual check ok', state: 'later' },
  { id: 'waiting-for-markus-barta-release', text: 'Waiting for Markus Barta: Release notes reviewed', state: 'later' },
]
const SERVER = 'Server: epics in the goal shape, from the figures and the Definitions engine'
const NOTE = 'Wiring the epics read into the shared list finish'
function world() {
  next = 0
  const data = fixtures()
  data.live.push(liveAgent({ project_id: 'p-aeon', session_id: 's-camy', principal_id: '44444444-4444-4444-8444-444444444444', name: 'camy', harness: 'codex', ticket: { id: 'n-a1', key: 'AEON-1531', title: 'Epics read', project_id: 'p-aeon' }, since: iso(NOW - 30 * MIN), heartbeat_at: iso(NOW - 30_000) }))
  const goals = [
    area('g-r137', 'release', 'Release 137 · better lists and definitions', [item('Group figures in grouped lists', 'moving'), item('Definitions page polish', 'queued'), item('Desk empty state', 'done')], { eta: { source: 'agent', text: 'Wed 21 Oct', at: '2026-10-21T16:00:00Z', partial: false } }),
    area('g-joed', 'goal', 'JoeDesk rebuild', [item('Results producer and sign-off', 'queued')], { projects: [{ id: 'p-pharos', key: 'PHAROS', name: 'Pharos', group_ids: [] }] }),
  ]
  const today = (h: number) => iso(NOW - h * HOUR)
  const groupBy = area('e-1395', 'epic', 'Projects › Progress at a glance: group the Goals view by epics', [
    item(SERVER, 'moving', { keys: ['AEON-1530', 'AEON-1531', 'AEON-1532'], session_ids: ['s-camy'], note: NOTE, note_by: 'p-camy', note_by_name: 'camy', note_at: iso(NOW - 8 * MIN), eta: { source: 'worker', at: iso(NOW + HOUR), by: 'camy', partial: false } }),
    item('Display › Group segment and the header swap', 'moving', { delivery: { state: 'in_queue', pull_request: 770 }, eta: { source: 'worker', at: iso(NOW + 2 * HOUR), by: 'camy', partial: false } }),
    ...Array.from({ length: 7 }, (_, i) => item(`Queued package ${i + 1}: the next slice of the epic`, 'queued')),
    item('Phone sheet polish', 'paused'),
    ...Array.from({ length: 7 }, (_, i) => item(`Done today ${i + 1}`, 'done', { merged_at: today(i + 1) })),
    item('Done before today', 'done', { merged_at: iso(NOW - 30 * HOUR) }),
  ], {
    steps: MISSING, next_step: { id: 'item-aeon-1530', text: SERVER, state: 'now' },
    eta: { source: 'outcomes', at: iso(NOW + 2 * HOUR), partial: true },
  })
  Object.assign(groupBy.progress, { pct: 50, computed_pct: 50, basis: 'mixed', remaining_hours: 39.6, remaining_unestimated: 2 })
  const sessions = area('e-1259', 'epic', 'Owned sessions program', [
    item('Owned session start', 'queued', { keys: ['AEON-1260'], eta: { source: 'release', text: '137', partial: false } }),
    item('Session handover', 'queued', { keys: ['AEON-1261'] }),
  ], { next_step: { id: 'item-aeon-1260', text: 'Owned session start', state: 'now' }, eta: { source: 'release', text: '137', partial: false } })
  Object.assign(sessions.progress, { remaining_hours: 12 })
  return { data, goals, epics: [groupBy, sessions] as Area[], calls: [] as string[] }
}
type World = ReturnType<typeof world>
const totals = (areas: Area[]) => areas.reduce((sum, a) => ({ moving: sum.moving + a.states.moving, queued: sum.queued + a.states.queued, paused: sum.paused + a.states.paused, done: sum.done + a.states.done }), { moving: 0, queued: 0, paused: 0, done: 0 })
const NOTE_TEXT = 'Nine items merged since this morning; the epics grouping is in review.'

// A quiet event stream the specs control (mockWork's own stream steps aside for it).
async function installStreams(page: Page) {
  await page.addInitScript(() => {
    class GoalStream extends EventTarget {
      readyState = 0
      onerror: ((event: Event) => void) | null = null
      onopen: ((event: Event) => void) | null = null
      constructor() { super(); queueMicrotask(() => { this.readyState = 1; this.onopen?.(new Event('open')); this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 10, resumed: false }) })) }) }
      close() { this.readyState = 2 }
    }
    Object.assign(window, { EventSource: GoalStream })
  })
}
async function mock(page: Page, w: World) {
  await installStreams(page)
  await mockWork(page, w.data)
  const list = (areas: Area[]) => ({ items: areas, totals: totals(areas), totals_partial: false, quiet_projects: { count: 1, keys: ['STUDIO'] }, truncated: false, read_through: 10, generation: 1, status_note: { text: NOTE_TEXT, by: 'p-lead', by_name: 'LEAD', set_at: iso(NOW - HOUR) }, eta_last_set_at: iso(NOW - 20 * MIN) })
  await page.route(/\/api\/goals(\?.*)?$/, route => { w.calls.push('goals'); return route.fulfill({ json: list(w.goals), headers: { 'Aeon-Contract': 'goals/1.5' } }) })
  await page.route(/\/api\/goals\/epics(\?.*)?$/, route => { w.calls.push('epics'); return route.fulfill({ json: list(w.epics), headers: { 'Aeon-Contract': 'goals/1.5' } }) })
  await page.route(/\/api\/goals\/[^/]+\/series\?/, route => route.fulfill({ json: { range: '24h', expansion: 'full', points: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/goals\/[^/]+\/history/, route => route.fulfill({ json: { items: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/nodes\/g-[a-z0-9]+$/, route => route.fulfill({ json: { id: 'g', key: 'G', kind_id: 'k-goal', title: 'Goal', body: '', fields: {}, state: 'open', parent_id: null, position: 1, created_at: iso(NOW - 300 * HOUR), updated_at: iso(NOW) } }))
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: iso(NOW) } }))
}

// ---------- Helpers ----------
type Box = { x: number; y: number; width: number; height: number }
const box = (locator: Locator): Promise<Box> => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
const lines = (locator: Locator) => locator.evaluate(el => Math.round(el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight)))
const row = (page: Page, id: string) => page.locator(`.goal[data-goal-id="${id}"]`)
const phone = (width: number) => width <= 760
const theme = (dark: boolean) => dark ? 'dark' : 'light'
async function shot(page: Page, info: TestInfo, width: number, dark: boolean, state: string) {
  await page.screenshot({ path: info.outputPath(`AEON-1395/aeon1395-${width}-${theme(dark)}-${state}.png`), fullPage: true })
}
// One JSON line per assert (width, theme, locale), read into checks.json for the evidence.
function check(info: TestInfo, width: number, dark: boolean, name: string, value: unknown) {
  const file = info.outputPath('AEON-1395/checks.md')
  mkdirSync(dirname(file), { recursive: true })
  appendFileSync(file, `${JSON.stringify({ ticket: 'AEON-1395', assert: name, width, theme: theme(dark), locale: LOCALE, value, pass: true })}\n`)
}
async function openGoals(page: Page, w: World, path = '/?view=goals') {
  await mock(page, w)
  await page.goto(path)
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
}
async function chooseGroup(page: Page, group: 'goals' | 'epics') {
  await page.locator('.display-btn').click()
  const option = page.locator('.display-panel [data-group]').filter({ hasText: group === 'epics' ? 'Epics' : 'Goals' })
  await option.click()
  await expect(option).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('Escape')
  await expect(page.locator('.display-panel')).toHaveCount(0)
  await expect(page.locator(`.goal[data-goal-id="${group === 'epics' ? 'e-1395' : 'g-r137'}"]`)).toBeVisible()
}
// The controls that must never move on a switch: the toolbar, every state chip, the Display
// trigger and every visible header cell.
async function stableControls(page: Page) {
  const controls: Record<string, Locator> = {
    filter: page.locator('.project-search'), views: page.locator('.view-seg'), display: page.locator('.display-btn'),
    hide: page.locator('.goals-chips .toggle-btn'), expand: page.locator('.goals-chips .expand-btn'),
  }
  const chips = page.locator('.goals-chips .state-seg [role="radio"]')
  for (let i = 0; i < await chips.count(); i++) controls[`chip ${i}`] = chips.nth(i)
  const cells = page.locator('.goal-head > button')
  for (let i = 0; i < await cells.count(); i++) if (await cells.nth(i).isVisible()) controls[`header ${(await cells.nth(i).getAttribute('aria-label')) ?? i}`.replace(/,.*$/, '')] = cells.nth(i)
  return controls
}
async function boxes(controls: Record<string, Locator>) {
  const out: Record<string, Box> = {}
  for (const [name, locator] of Object.entries(controls)) out[name] = await box(locator)
  return out
}
const movement = (a: Record<string, Box>, b: Record<string, Box>) => Math.max(0, ...Object.keys(a).flatMap(name => (['x', 'y', 'width', 'height'] as const).map(d => Math.abs(a[name]![d] - b[name]![d]))))

// ---------- E5: switching Goals ↔ Epics moves nothing but the list ----------
for (const size of [{ width: 1440, height: 900 }, { width: 1024, height: 800 }] as const) {
  test(`E5 ${size.width}: Goals → Epics → Goals moves no toolbar control, chip, Display trigger or header cell; GOAL and EPIC share one box`, async ({ page }, info) => {
    await page.setViewportSize(size)
    const errors = watchErrors(page)
    const w = world()
    await openGoals(page, w)
    const name = page.locator('.goal-head [data-head="name"]')
    await expect(name).toHaveAccessibleName('Sort by goal')
    await expect(name.locator('.swap > :not(.off)')).toHaveText('Goal')
    const controls = await stableControls(page)
    const guard = await controlStability(page, controls)
    const start = await boxes(controls), swap = await box(name.locator('.swap'))

    await guard.check(() => chooseGroup(page, 'epics'))
    const epics = await boxes(controls)
    await expect(name).toHaveAccessibleName('Sort by epic')
    await expect(name.locator('.swap > :not(.off)')).toHaveText('Epic')
    // Upper case by style: the header reads EPIC.
    expect(await name.locator('.swap > :not(.off)').evaluate(el => getComputedStyle(el).textTransform)).toBe('uppercase')
    const epicSwap = await box(name.locator('.swap'))
    await guard.check(() => chooseGroup(page, 'goals'))
    const back = await boxes(controls)
    await expect(name).toHaveAccessibleName('Sort by goal')

    const moved = Math.max(movement(start, epics), movement(epics, back))
    expect(moved).toBeLessThanOrEqual(.5)
    expect(Math.abs(epicSwap.width - swap.width)).toBeLessThanOrEqual(.5)
    check(info, size.width, false, 'E5 movementPx toolbar, chips, Display trigger and every header cell, Goals → Epics → Goals', { movementPx: Math.round(moved * 100) / 100, controls: Object.keys(controls).length })
    check(info, size.width, false, 'E5 GOAL/EPIC box width identical', { goal: swap.width, epic: epicSwap.width })
    // The sorts stay as they were.
    await page.locator('.display-btn').click()
    await expect(page.locator('.display-panel .sort-option[data-sort]')).toHaveText(['ETA', 'Name', 'Progress', 'Project', 'Moving', 'Queued', 'Done'])
    await page.keyboard.press('Escape')
    expect(w.calls).toEqual(['goals', 'epics', 'goals'])
    expect(errors).toEqual([])
  })
}

// ---------- E1, E2: epics folded and one opened ----------
for (const look of [{ width: 1440, height: 900, dark: false }, { width: 1440, height: 900, dark: true }, { width: 1024, height: 800, dark: false }] as const) {
  const wide = look.width >= 1200
  test(`E1/E2 ${look.width} ${theme(look.dark)}: epics in the goal anatomy; an opened epic lists what's missing in Definitions order, then its items with live lines and folds`, async ({ page }, info) => {
    await page.setViewportSize(look)
    await page.emulateMedia({ colorScheme: look.dark ? 'dark' : 'light' })
    const errors = watchErrors(page)
    const w = world()
    await openGoals(page, w, '/?view=goals&group=epics')
    const epic = row(page, 'e-1395'), quiet = row(page, 'e-1259')
    await expect(page.locator('.goal-head [data-head="name"]')).toHaveAccessibleName('Sort by epic')
    // The goal-row anatomy: project key, the epic icon, the name, Next; computed percent, no pen, no chart.
    await expect(epic.locator('.goal-keys .key-badge')).toHaveText(['AEON'])
    await expect(epic.locator('.goal-title .kind')).toHaveAttribute('aria-label', 'Epic')
    await expect(epic.locator('.goal-name')).toHaveText('Projects › Progress at a glance: group the Goals view by epics')
    await expect(epic.locator('.goal-next .txt')).toHaveText(SERVER)
    await expect(epic.locator('.pct')).toHaveText('50%')
    await expect(epic.locator('.goal-progress .pct svg')).toHaveCount(0)
    await expect(epic.locator('.goal-progress button')).toHaveCount(0)
    await expect(epic.locator('.goal-progress [role="progressbar"]')).toHaveAttribute('aria-valuenow', '50')
    check(info, look.width, look.dark, 'Progress shows the computed leaf-hours rollup (server test TestGoalEpicsAreasRollupAndMissingChecks: 50 % = 4 of 8 leaf hours, a parent’s 50 h ignored); no pen, no chart', { pct: 50, pen: false })
    if (wide) await expect(epic.locator('.stat')).toHaveText(['2', '7', '8'])
    // toHaveText folds NBSP into spaces; the exact characters are read from the DOM.
    await expect(epic.locator('.goal-eta .left')).toHaveText('~40 h left')
    expect(await epic.locator('.goal-eta .left').textContent()).toBe(`~40${NB}h${NB}left`)
    await expect(epic.locator('.goal-eta .t')).toHaveText('~12:00')
    await expect(quiet.locator('.goal-eta .t')).toHaveText('137')
    check(info, look.width, look.dark, 'LANDS: latest worker ETA over open items, else the target release; "~40 h left" with NBSP', { epic: '~12:00', quiet: '137', left: `~40${NB}h${NB}left` })
    // The menu has no chart for epics.
    await epic.locator('.goal-row').hover()
    await epic.locator('.row-more').click()
    await expect(page.getByRole('menuitem', { name: /progress over time/ })).toHaveCount(0)
    await page.keyboard.press('Escape')
    if (!look.dark && look.width === 1440) await shot(page, info, look.width, look.dark, 'e2')

    // Opening the epic pushes only the rows below it.
    const above = await controlStability(page, { toggle: epic.locator('.goal-toggle'), name: epic.locator('.goal-name'), eta: epic.locator('.goal-eta'), filter: page.locator('.project-search') })
    const below = (await box(quiet.locator('.goal-toggle'))).y
    await above.check(async () => { await epic.locator('.goal-toggle').click(); await expect(epic).toHaveAttribute('data-open', '1') })
    expect((await box(quiet.locator('.goal-toggle'))).y).toBeGreaterThan(below)

    // What's missing: the failing checks in Definitions order, with the step icons, and no tally.
    await expect(epic.locator('.steps-head')).toHaveText('What’s missing')
    await expect(epic.locator('.steps .step')).toHaveText(MISSING.map(s => `${s.state === 'now' ? 'Now: ' : 'Later: '}${s.text}`))
    await expect(epic.locator('.steps .step .st')).toHaveCount(3)
    check(info, look.width, look.dark, 'Steps list the failing entry checks in Definitions order (server: TestGoalEpicsAreasRollupAndMissingChecks)', MISSING.map(s => s.text))

    // Items: GoalItem unchanged. Queued five, then "2 more queued · show"; the Done fold with today's merges.
    const items = epic.locator('.item')
    await expect(items).toHaveCount(2 + 5 + 1)
    const pkg = epic.locator('.item', { hasText: SERVER }), queue = epic.locator('.item', { hasText: 'Display › Group segment' })
    await expect(pkg.locator('.i-keys a')).toHaveText('AEON-1530')
    await expect(pkg.locator('.i-keys .more')).toHaveText('+2')
    await expect(pkg.locator('.i-keys .more')).toHaveAttribute('data-tip', 'AEON-1531, AEON-1532')
    await expect(pkg.locator('.i-live.is-note')).toHaveText(`${NOTE} · camy 09:52`)
    const pr = queue.locator('.i-live.is-pr')
    await expect(pr).toHaveText('#770 · In queue')
    expect(await pr.locator('.tx').textContent()).toBe(`#770${NB}·${NB}In${NB}queue`)
    await expect(pr.locator('svg')).toBeVisible()
    check(info, look.width, look.dark, 'Item live lines: the agent note, else the pull request state', { note: `${NOTE} · camy 09:52`, pr: '#770 · In queue' })
    const folds = epic.locator('.fold-btn')
    expect(await folds.first().textContent()).toBe(`2${NB}more${NB}queued·${NB}show·${NB}hide`)
    expect(await folds.last().textContent()).toBe(`8${NB}done·${NB}7${NB}today·${NB}show·${NB}hide`)
    check(info, look.width, look.dark, 'Folds: "2 more queued · show" and "8 done · 7 today · show", NBSP inside, "·" bound to its words', { queued: '2 more queued · show', done: '8 done · 7 today · show' })

    // AEON-1494 widths under epics: icon x = step x ±1, one line at ≥ 1200, titles ≥ 320 px beside a live line, ≥ 280 px at 1024.
    const step = await box(epic.locator('.step .s-ic svg').first())
    let widest = Infinity
    for (let i = 0; i < await items.count(); i++) {
      const icon = await box(items.nth(i).locator('.i-state > :first-child'))
      expect(Math.abs(icon.x - step.x), `item ${i} icon x`).toBeLessThanOrEqual(1)
      const label = items.nth(i).locator('.i-label')
      if (wide) expect(await lines(label), `item ${i} one line`).toBe(1)
      widest = Math.min(widest, (await box(label)).width)
    }
    if (wide) expect((await box(pkg.locator('.i-label'))).width).toBeGreaterThanOrEqual(320)
    else expect(widest).toBeGreaterThanOrEqual(280)
    check(info, look.width, look.dark, `Item rows hold the AEON-1494 asserts under epics (icon x = step x ±1; ${wide ? 'one line, title ≥ 320 px beside a live line' : 'title ≥ 280 px'})`, { narrowestLabel: Math.round(widest), oneLine: wide })

    // An epic with nothing missing renders no steps block and no heading.
    await quiet.locator('.goal-toggle').click()
    await expect(quiet).toHaveAttribute('data-open', '1')
    await expect(quiet.locator('.steps')).toHaveCount(0)
    await expect(quiet.locator('.steps-head')).toHaveCount(0)
    check(info, look.width, look.dark, 'An epic with no failing checks renders no steps block', { steps: 0 })
    await quiet.locator('.goal-toggle').click()
    await expect(quiet).toHaveAttribute('data-open', '0')
    await shot(page, info, look.width, look.dark, 'e1')
    // Never: italics, coloured edge accents.
    const style = await epic.evaluate(el => { const s = getComputedStyle(el); return { left: s.borderLeftWidth, top: s.borderTopWidth } })
    expect(style).toEqual({ left: '0px', top: '0px' })
    expect(await page.locator('.goal-grid').evaluate(grid => [...grid.querySelectorAll('*')].filter(el => getComputedStyle(el).fontStyle === 'italic').length)).toBe(0)
    expect(errors).toEqual([])
  })
}

// ---------- E3: Display with the Group segment first ----------
test('E3 1440: Display leads with Group: Goals · Epics, inside the viewport', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  await openGoals(page, w)
  await page.locator('.display-btn').click()
  const panel = page.locator('.display-panel')
  await expect(panel.locator('.eyebrow').first()).toHaveText('Group')
  const group = panel.getByRole('radiogroup').first()
  await expect(group).toHaveAccessibleName('Group')
  await expect(group.getByRole('radio')).toHaveText(['Goals', 'Epics'])
  await expect(group.getByRole('radio', { name: 'Goals' })).toHaveAttribute('aria-checked', 'true')
  const frame = await box(page.locator('.floating').first())
  expect(frame.x).toBeGreaterThanOrEqual(0); expect(frame.y).toBeGreaterThanOrEqual(0)
  expect(frame.x + frame.width).toBeLessThanOrEqual(1440); expect(frame.y + frame.height).toBeLessThanOrEqual(900)
  check(info, 1440, false, 'E3 Display: the first control is Group: Goals · Epics; the menu stays inside the viewport', { first: 'Group', options: ['Goals', 'Epics'] })
  await shot(page, info, 1440, false, 'e3')
})

// ---------- E4: no open epics ----------
test('E4 1440: no open epics says so, without examples', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  w.epics = []
  await mock(page, w)
  await page.goto('/?view=goals&group=epics')
  const state = page.locator('.goals-view .state')
  await expect(state.locator('h2')).toHaveText('No open epics in these projects.')
  await expect(state.locator('p')).toHaveCount(0)
  await expect(page.locator('.goal-list .goal')).toHaveCount(0)
  check(info, 1440, false, 'E4 empty: "No open epics in these projects." and nothing else', { title: 'No open epics in these projects.' })
  await shot(page, info, 1440, false, 'e4')
})

// ---------- Persistence: per person across a reload; ?group= opens it once ----------
test('the group is kept per person across a reload; ?view=goals&group=epics opens it once and changes nothing', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  await openGoals(page, w)
  await chooseGroup(page, 'epics')
  await expect.poll(() => (w.data.preferences.goals as { group?: string } | undefined)?.group).toBe('epics')
  w.calls.length = 0
  await page.reload()
  await expect(row(page, 'e-1395')).toBeVisible()
  // The remembered group is the first list read: Goals never show first.
  expect(w.calls[0]).toBe('epics')
  expect(w.calls).not.toContain('goals')
  check(info, 1440, false, 'Persistence: the group persists per person across a reload (the first read is the remembered group)', { calls: [...w.calls] })

  // Back to Goals, then a link asks for Epics: it opens them, and the person's choice stays Goals.
  await chooseGroup(page, 'goals')
  await expect.poll(() => (w.data.preferences.goals as { group?: string } | undefined)?.group).toBe('goals')
  await page.goto('/?view=goals&group=epics')
  await expect(row(page, 'e-1395')).toBeVisible()
  expect((w.data.preferences.goals as { group?: string }).group).toBe('goals')
  await page.goto('/?view=goals')
  await expect(row(page, 'g-r137')).toBeVisible()
  await expect(page.locator('.goal-head [data-head="name"]')).toHaveAccessibleName('Sort by goal')
  check(info, 1440, false, '?view=goals&group=epics opens Epics once; the remembered group stays Goals', { stored: 'goals' })
})

// ---------- E6: phones ----------
for (const dark of [false, true]) {
  test.describe(() => {
    test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
    test(`E6 390 ${theme(dark)}: phones keep the epic rows and their essentials, 44 px Group targets, no overflow`, async ({ page }, info) => {
      await page.emulateMedia({ colorScheme: dark ? 'dark' : 'light' })
      const errors = watchErrors(page)
      const w = world()
      await openGoals(page, w, '/?view=goals&group=epics')
      const epic = row(page, 'e-1395')
      await expect(epic.locator('.goal-name')).toBeVisible()
      await expect(epic.locator('.pct')).toHaveText('50%')
      await epic.locator('.goal-toggle').click()
      await expect(epic).toHaveAttribute('data-open', '1')
      await expect(epic.locator('.phone-next')).toContainText(SERVER)
      const pkg = epic.locator('.item', { hasText: SERVER })
      await expect(pkg.locator('.phone-tickets a')).toHaveText(['AEON-1530', 'AEON-1531', 'AEON-1532'])
      expect((await box(epic.locator('.fold-btn').first())).height).toBeGreaterThanOrEqual(44)
      const overflow = await page.evaluate(() => { const main = document.getElementById('main')!; return main.scrollWidth - main.clientWidth })
      expect(overflow).toBeLessThanOrEqual(0)
      await shot(page, info, 390, dark, 'e6')
      await page.locator('.display-btn').click()
      const options = page.locator('.display-panel [data-group]')
      for (let i = 0; i < 2; i++) expect((await box(options.nth(i))).height, `group option ${i}`).toBeGreaterThanOrEqual(44)
      check(info, 390, dark, 'E6 phone: rows keep name, percent and ETA; Group targets ≥ 44 px; no horizontal overflow', { overflow, groupTarget: Math.round((await box(options.first())).height) })
      expect(errors).toEqual([])
    })
  })
}
