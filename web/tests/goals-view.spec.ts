// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1457: Projects › Goals, the third view (List · Cards · Goals), built on the Goals
// API (AEON-1456) to Tick's approved design (AEON-1452 decisions 1–10, r5 and the r5
// amendment). Risks: a view switch that moves the filter or the chips; counts that
// change with Hide done; an opened goal or chart that moves a control; an ETA
// popover that leaves the card or hides who set it; a rate or projection the rules
// omit; phones that lose the essentials. Screenshots go to each test's output dir.
import { test, expect, type Page, type Locator, type TestInfo } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, liveAgent, me, mockWork, watchErrors, type Fixtures } from './work-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: 'Europe/Vienna', locale: 'en-GB' })
const NOW = Date.parse('2026-09-23T12:00:00Z') // 14:00 in Vienna
const HOUR = 3_600_000
const iso = (ms: number) => new Date(ms).toISOString()
const CAMY = '44444444-4444-4444-8444-444444444444'

test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

// ---------- The world: goals with their outcomes, history, ETA changes and the Desk ----------
type State = 'moving' | 'queued' | 'paused' | 'done'
interface Outcome { id: string; ref: string; label: string; note: string; position: number; keys: string[]; state: State; session_ids: string[]; eta: Record<string, unknown>; passed: boolean; coverage: { estimated_leaves: number; total_leaves: number }; weight: number; handed_over: boolean }
let next = 0
function item(label: string, state: State, extra: Partial<Outcome> = {}): Outcome {
  next++
  return { id: `00000000-0000-4000-8000-${String(next).padStart(12, '0')}`, ref: `o${next}`, label, note: '', position: next, keys: [`AEON-${1400 + next}`], state, session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 1, total_leaves: 1 }, weight: 1, handed_over: false, ...extra }
}
interface GoalMock { id: string; key: string; title: string; kind: 'goal' | 'release'; created: number; hist: [number, number, string[]][]; [field: string]: unknown }
function goal(id: string, title: string, items: Outcome[], extra: Record<string, unknown> & { created: number; hist: [number, number, string[]][] }): GoalMock {
  const states = { moving: 0, queued: 0, paused: 0, done: 0 }
  for (const outcome of items) states[outcome.state]++
  const done = items.length > 0 && states.done === items.length
  const pct = extra.hist.length ? extra.hist[extra.hist.length - 1]![1] : 0
  return {
    id, key: id.toUpperCase(), title, kind: 'goal', project_id: 'p-aeon', workspace: false, expansion: 'full', counts: { outcomes: items.length, tickets: items.length },
    items, steps: [], next_step: null, projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 0, question_ids: [] },
    progress: { pct, computed_pct: pct, source: 'computed', basis: 'count', total: items.length, done: states.done, items_without_estimate: 0, items_partially_estimated: 0 },
    eta: { source: 'none', partial: false }, passed: false, states, done, stall: { stalled: false, since: iso(NOW - HOUR) }, read_through: 10, generation: 1, ...extra,
  }
}
const steps = [{ id: 'cut', text: 'Open: 135 tagged and nightly green', state: 'done' }, { id: 'flicker', text: 'Panel flicker fix and Desk empty state', state: 'done' }, { id: 'chat', text: 'Chat fixes after their designs', state: 'now' }, { id: 'ci', text: 'CI targets met (S3–S5)', state: 'later' }]
function world(): { data: Fixtures; goals: GoalMock[]; desk: { open: number }; hidden: Set<string> } {
  next = 0
  const data = fixtures()
  data.live.push(liveAgent({ project_id: 'p-aeon', session_id: 's-camy', principal_id: CAMY, name: 'camy', harness: 'codex', ticket: { id: 'n-a1', key: 'AEON-1401', title: 'Image builds work again', project_id: 'p-aeon' } }, 30))
  const queued = Array.from({ length: 7 }, (_, i) => item(`Queued outcome number ${i + 1} with a longer label that wraps on narrow screens`, 'queued', { eta: { source: 'agent', text: `~${17 + i}:00`, at: iso(NOW + (3 + i) * HOUR), partial: false } }))
  const release = goal('g-r136', 'Release 136 · steady and complete', [
    item('Image builds work again after the browser package was removed upstream', 'moving', { session_ids: ['s-camy'], note: 'PR #707 · checks running', eta: { source: 'agent', text: '~15:00', at: iso(NOW + HOUR), by: CAMY, partial: false } }),
    item('Browser tests fit the 20-minute CI limit', 'moving', { eta: { source: 'none', partial: false } }),
    ...queued,
    item('Phone chrome: essentials only', 'paused'),
    item('Cut and nightly green', 'done'), item('Flicker fix', 'done'), item('Desk empty state', 'done'),
  ], {
    kind: 'release', steps, next_step: steps[2], projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }, { id: 'p-pharos', key: 'PHAROS', name: 'Pharos', group_ids: [] }],
    waits: { count: 1, question_ids: ['q-gate'] }, eta: { source: 'agent', text: '~16:00', at: iso(NOW + 2 * HOUR), by: CAMY, partial: false },
    created: NOW - 300 * HOUR, // Changes fall inside buckets (never on a boundary): 15:30, 02:30, 08:30 and 10:30 UTC.
    hist: [[-200, 10, ['AEON-1301']], [-20.5, 15, ['AEON-1330']], [-9.5, 20, ['AEON-1445', 'AEON-1441', 'AEON-1439']], [-3.5, 26, ['AEON-1406']], [-1.5, 31, ['AEON-1446']]],
  })
  const joedesk = goal('g-joed', 'JoeDesk rebuild', [
    item('P6a: version scheme, pinned builds, release evidence', 'moving', { keys: ['JOED-26'], note: 'in QA, review round 3 next', eta: { source: 'agent', text: '~13:30', at: iso(NOW - 30 * 60_000), partial: false }, passed: true }),
    item('Results producer and sign-off', 'queued', { keys: ['JOED-20'] }),
    item('the switch to live is yours', 'done', { handed_over: true }),
  ], {
    projects: [{ id: 'p-pharos', key: 'PHAROS', name: 'Pharos', group_ids: [] }], eta: { source: 'agent', text: 'Thu 24 Sep 18:00', at: '2026-09-24T16:00:00Z', partial: false },
    stall: { stalled: true, since: iso(NOW - 6.5 * HOUR) }, created: NOW - 100 * HOUR, hist: [[-50, 40, ['JOED-21']], [-30, 55, ['JOED-22']], [-6.5, 64, ['JOED-25']]],
  })
  joedesk.handed_over = { outcome_id: (joedesk.items as Outcome[])[2]!.id, label: 'the switch to live is yours', detail: 'Agents stop here; the switch happens on your Desk.', at: iso(NOW - HOUR), by: me.id }
  const hausnetz = goal('g-hnet', 'hausnetz · home network map', [item('Small escape fix', 'queued', { keys: ['HNET-230'] }), item('Baseline', 'done')], {
    eta: { source: 'agent', text: 'after the baseline', partial: false }, created: NOW - 100 * HOUR, hist: [[-70, 50, ['HNET-188']], [-20, 74, ['HNET-227']]],
  })
  ;(hausnetz.progress as Record<string, unknown>).source = 'override'
  ;(hausnetz.progress as Record<string, unknown>).override_note = 'the baseline counts double'
  const young = goal('g-r137', 'Release 137 · better lists', [item('Group figures in grouped lists', 'queued')], {
    kind: 'release', eta: { source: 'outcomes', at: '2026-09-25T08:00:00Z', partial: true }, created: NOW - HOUR, hist: [[-1, 0, []]],
  })
  const shipped = goal('g-r135', 'Release 135 · ship faster', [item('Shards from real timings', 'done'), item('Gatekeeper P1', 'done')], {
    kind: 'release', eta: { source: 'agent', text: 'shipped', partial: false }, created: NOW - 400 * HOUR, hist: [[-300, 50, []], [-100, 100, []]],
  })
  return { data, goals: [release, joedesk, hausnetz, young, shipped], desk: { open: 2 }, hidden: new Set() }
}
type World = ReturnType<typeof world>

// The series as the server builds it: buckets aligned to UTC, each the value at its end.
function series(g: GoalMock, range: string) {
  const [count, step] = range === '7d' ? [28, 6 * HOUR] : range === '30d' ? [30, 24 * HOUR] : [24, HOUR]
  const end = Math.floor(NOW / step) * step
  const value = (at: number) => at < g.created ? 0 : g.hist.filter(([ago]) => NOW + ago * HOUR <= at).reduce((v, [, pct]) => pct, 0)
  return Array.from({ length: count }, (_, i) => {
    const t = end - (count - 1 - i) * step, close = Math.min(t + step, NOW)
    const keys = g.hist.filter(([ago]) => NOW + ago * HOUR > t && NOW + ago * HOUR <= close).flatMap(([, , k]) => k)
    return { t: iso(t), pct: value(close), basis: 'count', delta: value(close) - value(t), keys, changed_keys: keys, keys_truncated: false }
  })
}
async function mockGoals(page: Page, w: World, options: { failList?: boolean } = {}) {
  const calls = await mockWork(page, w.data)
  const listCalls: URLSearchParams[] = []
  await page.route(/\/api\/goals(\?.*)?$/, route => {
    const query = new URL(route.request().url()).searchParams
    listCalls.push(query)
    if (options.failList) return route.fulfill({ status: 500, json: { error: 'goals: the database did not answer' } })
    const totals = { moving: 0, queued: 0, paused: 0, done: 0 }
    for (const g of w.goals) for (const s of ['moving', 'queued', 'paused', 'done'] as const) totals[s] += (g.states as typeof totals)[s]
    const q = (query.get('q') ?? '').toLowerCase(), state = query.get('state') as State | null
    let items = w.goals.filter(g => !(query.get('hide_done') === 'true' && g.done))
      .filter(g => !state || (g.states as typeof totals)[state] > 0)
      .filter(g => !q || [g.title, (g.next_step as { text: string } | null)?.text ?? '', ...(g.projects as { key: string }[]).map(p => p.key), ...(g.items as Outcome[]).flatMap(i => [i.label, i.note, ...i.keys])].join(' ').toLowerCase().includes(q))
    const sort = query.get('sort') ?? 'eta', dir = query.get('dir') === 'desc' ? -1 : 1
    const key = (g: GoalMock): number | string | null => sort === 'eta' ? ((g.eta as { at?: string }).at ? Date.parse((g.eta as { at: string }).at) : null)
      : sort === 'name' ? g.title.toLowerCase() : sort === 'progress' ? (g.progress as { pct: number }).pct : sort === 'project' ? (g.projects as { key: string }[])[0]!.key : (g.states as Record<string, number>)[sort]!
    items = [...items].sort((a, b) => { const x = key(a), y = key(b); if (x === y) return 0; if (x === null) return 1; if (y === null) return -1; return (x < y ? -1 : 1) * dir })
    const strip = (g: GoalMock) => Object.fromEntries(Object.entries(g).filter(([k]) => k !== 'created' && k !== 'hist'))
    return route.fulfill({ json: { items: items.map(strip), totals, totals_partial: false, quiet_projects: { count: 2, keys: ['GLINT', 'STUDIO'] }, truncated: false, read_through: 10, generation: 1 }, headers: { 'Aeon-Contract': 'goals/1.4' } })
  })
  const seriesCalls: string[] = []
  await page.route(/\/api\/goals\/[^/]+\/series\?/, route => {
    const url = new URL(route.request().url()), id = url.pathname.split('/')[3]!, range = url.searchParams.get('range') ?? '24h'
    seriesCalls.push(`${id}:${range}`)
    const g = w.goals.find(x => x.id === id)!
    return route.fulfill({ json: { range, expansion: 'full', points: series(g, range), read_through: 10, generation: 1 } })
  })
  await page.route(/\/api\/goals\/[^/]+\/history/, route => {
    const url = new URL(route.request().url()), id = url.pathname.split('/')[3]!, outcome = url.searchParams.get('outcome')
    const items = id === 'g-r136' && !outcome ? [
      { id: 92, actor_principal_id: CAMY, node_id: 'g-r136', type: 'goal.eta_changed', before: { source: 'agent', text: '~15:30' }, after: { source: 'agent', text: '~16:00', at: iso(NOW + 2 * HOUR), by: CAMY }, at: iso(NOW - 62 * 60_000) },
      { id: 91, actor_principal_id: CAMY, node_id: 'g-r136', type: 'goal.items_changed', after: {}, at: iso(NOW - 90 * 60_000) },
      { id: 90, actor_principal_id: CAMY, node_id: 'g-r136', type: 'goal.eta_changed', after: { source: 'agent', text: '~15:30', by: CAMY }, at: iso(NOW - 228 * 60_000) },
    ] : []
    return route.fulfill({ json: { items, read_through: 10, generation: 1 } })
  })
  // Each goal's creation, read once for the chart's "History since".
  await page.route(/\/api\/nodes\/g-[a-z0-9]+$/, route => {
    const id = new URL(route.request().url()).pathname.split('/').pop()!, g = w.goals.find(x => x.id === id)
    return g ? route.fulfill({ json: { id, key: g.key, kind_id: 'k-goal', title: g.title, body: '', fields: {}, state: 'open', parent_id: null, position: 1, created_at: iso(g.created), updated_at: iso(NOW) } }) : route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: {
    items: w.desk.open ? [
      { id: 'q-gate', kind: 'question', project_id: 'p-aeon', revision: 1, title: 'Which identity should agent reviews run as for the gatekeeper go-live?', created_at: iso(NOW - 3 * HOUR), held: true, href: '', source: '' },
      { id: 'q-arp', kind: 'question', project_id: 'p-pharos', revision: 1, title: 'Allow privileged ARP through an Apple entitlement, or stay without it?', created_at: iso(NOW - 2 * HOUR), held: false, href: '', source: '' },
    ].slice(0, w.desk.open) : [],
    counts: { open: w.desk.open, held: w.desk.open ? 1 : 0, chores: 0 }, has_more: false, as_of: iso(NOW),
  } }))
  return { calls, listCalls, seriesCalls }
}
async function openGoals(page: Page, w: World, path = '/?view=goals') {
  const mocked = await mockGoals(page, w)
  await page.goto(path)
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
  return mocked
}
const goalRow = (page: Page, id: string) => page.locator(`.goal[data-goal-id="${id}"]`)
const box = (locator: Locator) => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, right: r.right, bottom: r.bottom } })
// The words a person can see in a control: text whose element is rendered and not visibility-hidden.
const visibleWords = (locator: Locator) => locator.evaluate(el => {
  const words: string[] = []
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node.textContent?.trim(), parent = node.parentElement!
    if (text && parent.getClientRects().length && getComputedStyle(parent).visibility === 'visible') words.push(text)
  }
  return words
})
async function shot(page: Page, info: TestInfo, name: string) { await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true }) }

// ---------- Decisions 1 and 2: the third segment and its toolbar ----------
test('Goals is the third segment; the filter and group chips stay put; chips count items and add up; Hide done and E never move a control', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const w = world()
  const { listCalls } = await mockGoals(page, w)
  await page.goto('/')
  const views = page.getByRole('radiogroup', { name: 'View' })
  await expect(views.getByRole('radio')).toHaveText(['List', 'Cards', 'Goals'])
  const filter = page.locator('.project-search'), firstChip = page.locator('.group-chip, .chip-add').first()
  await expect(firstChip).toBeVisible()
  const listFilter = await box(filter), listChip = await box(firstChip)
  await views.getByRole('radio', { name: 'Goals view' }).click()
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
  await expect(page.getByRole('searchbox', { name: 'Filter goals' })).toBeVisible()
  const goalsFilter = await box(filter), goalsChip = await box(page.locator('.group-chip, .chip-add').first())
  // Decision 2: one filter width in all three views (clamp 160–280), so the chips never shift.
  expect(Math.abs(goalsFilter.width - listFilter.width)).toBeLessThanOrEqual(.5)
  expect(Math.abs(goalsFilter.x - listFilter.x)).toBeLessThanOrEqual(.5)
  expect(Math.abs(goalsChip.x - listChip.x)).toBeLessThanOrEqual(.5)
  expect(goalsFilter.width).toBeGreaterThanOrEqual(160); expect(goalsFilter.width).toBeLessThanOrEqual(280)

  // The chips count items over every goal and add up; done goals start hidden.
  const chips = page.getByRole('radiogroup', { name: 'Show items' })
  const counts = await chips.getByRole('radio').evaluateAll(buttons => buttons.map(b => Number(b.querySelector('.n')?.textContent)))
  expect(counts).toEqual([21, 3, 10, 1, 7])
  expect(counts[0]).toBe(counts.slice(1).reduce((a, b) => a + b, 0))
  await expect(page.locator('.goal-list .goal')).toHaveCount(4)
  const hide = page.getByRole('button', { name: 'Hide done' }), expand = page.locator('.expand-btn')
  await expect(hide).toHaveAttribute('aria-pressed', 'true')
  const guard = await controlStability(page, { filter, hide, expand, all: chips.getByRole('radio', { name: /^All/ }), views: page.locator('.view-seg'), display: page.locator('.display-btn') })
  await guard.check(async () => { await page.keyboard.press('h'); await expect(page.locator('.goal-list .goal')).toHaveCount(5) })
  expect(await chips.getByRole('radio').evaluateAll(buttons => buttons.map(b => Number(b.querySelector('.n')?.textContent)))).toEqual(counts)
  expect(listCalls.at(-1)?.get('hide_done')).toBeNull()
  await guard.check(async () => { await page.keyboard.press('e'); await expect(expand.locator('.lbl > span:not(.off)')).toHaveText('Collapse all') })
  await expect(page.locator('.goal[data-open="1"]')).toHaveCount(5)
  await guard.check(async () => { await page.keyboard.press('e'); await expect(page.locator('.goal[data-open="1"]')).toHaveCount(0) })
  await guard.check(async () => { await page.keyboard.press('2'); await expect(chips.getByRole('radio', { name: /^Moving/ })).toHaveAttribute('aria-checked', 'true') })
  expect(listCalls.at(-1)?.get('state')).toBe('moving')
  await expect(page.locator('.goal-list .goal')).toHaveCount(2)
  await shot(page, info, 'goals-1440-light-moving')
  await page.keyboard.press('1')
  await expect(page.locator('.goal-list .goal')).toHaveCount(5)
  // The keyboard line under the card names every key.
  await expect(page.locator('.keys-line')).toContainText('all, moving, queued, paused, done')
  expect(errors).toEqual([])
})

// ---------- Decisions 3, 4 and 9: Waiting on you, the header, the footer ----------
test('Waiting on you lists live Desk items; header labels sort with LANDS first; the footer names the quiet projects', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world()
  const { listCalls } = await openGoals(page, w)
  const waiting = page.getByRole('region', { name: 'Waiting on you' })
  await expect(waiting).toContainText('Waiting on you')
  await expect(waiting.locator('.n-badge')).toHaveText('2')
  const rows = waiting.locator('.waiting-item')
  await expect(rows).toHaveCount(2)
  await expect(rows.first()).toContainText('AEON')
  await expect(rows.first()).toContainText('Which identity should agent reviews run as')
  await expect(rows.first().locator('.wgoal')).toHaveText('Release 136 · steady and complete')
  await expect(rows.nth(1).locator('.wgoal')).toHaveText('Pharos')
  await expect(waiting.getByRole('link', { name: /Open Desk/ })).toHaveAttribute('href', '/decision-desk')
  // A warm full tint, never an edge bar.
  const tint = await waiting.evaluate(el => { const s = getComputedStyle(el, '::before'), own = getComputedStyle(el); return { bg: s.backgroundColor, left: own.borderLeftWidth, top: own.borderTopWidth } })
  expect(tint.bg).not.toBe('rgba(0, 0, 0, 0)'); expect(tint.left).toBe('0px'); expect(tint.top).toBe('0px')

  // Decision 4: labels are the sort controls; LANDS (ETA) leads, ascending; workspace order from the server.
  const head = page.locator('.goal-head')
  await expect(head.getByRole('button', { name: /^Sort by lands/ })).toHaveAttribute('data-sort-dir', 'asc')
  await expect(head.getByRole('button', { name: /^Sort by lands/ })).toHaveAccessibleName('Sort by lands, now ascending')
  await expect(page.locator('.goal-list .goal-name')).toHaveText(['Release 136 · steady and complete', 'JoeDesk rebuild', 'Release 137 · better lists', 'hausnetz · home network map'])
  expect(listCalls.at(-1)?.get('sort')).toBe('eta')
  await head.getByRole('button', { name: /^Sort by progress/ }).click()
  await expect(head.getByRole('button', { name: /^Sort by progress/ })).toHaveAttribute('data-sort-dir', 'desc')
  await expect(page.locator('.goal-list .goal-name').first()).toHaveText('hausnetz · home network map')
  expect(listCalls.at(-1)?.get('dir')).toBe('desc')
  await head.getByRole('button', { name: /^Sort by goal/ }).click()
  await expect(page.locator('.goal-list .goal-name').first()).toHaveText('hausnetz · home network map')
  await head.getByRole('button', { name: /^Sort by goal/ }).click()
  await expect(head.getByRole('button', { name: /^Sort by goal/ })).toHaveAttribute('data-sort-dir', 'desc')
  await expect(page.locator('.display-btn')).toHaveAttribute('aria-label', 'Display, sorted by name')

  // Decision 5: counts right-aligned, zero muted, the waits marker and the hand-set pen.
  const release = goalRow(page, 'g-r136')
  await expect(release.locator('.goal-keys .key-badge')).toHaveText(['AEON', 'PHAROS'])
  await expect(release.locator('.stat')).toHaveText(['2', '7', '3'])
  await expect(goalRow(page, 'g-r137').locator('.stat').first()).toHaveAttribute('data-zero', '1')
  await expect(release.locator('.waits')).toHaveAttribute('aria-label', '1 open Desk question waits on you')
  await expect(goalRow(page, 'g-hnet').locator('.pct')).toHaveAttribute('data-tip', /Progress set by hand/)
  await expect(release.locator('.of')).toHaveText('3 of 13 done')
  await expect(release.locator('.goal-next')).toContainText('Chat fixes after their designs')
  // Decision 9: one quiet line under the list.
  await expect(page.locator('.quiet-line')).toHaveText(/2 projects with nothing moving:\s*GLINT\s*STUDIO/)
  await shot(page, info, 'goals-1440-light')
})

// ---------- Decision 6: the opened goal ----------
test('an opened goal lists what’s missing, then Moving, Queued (five, then a fold), Paused and Done folded; one agent column; nothing moves', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openGoals(page, world())
  const release = goalRow(page, 'g-r136'), toggle = release.locator('.goal-toggle')
  const guard = await controlStability(page, { toggle, lands: page.getByRole('button', { name: /^Sort by lands/ }), expand: page.locator('.expand-btn'), eta: release.locator('.goal-eta') })
  await guard.check(async () => { await toggle.click(); await expect(release).toHaveAttribute('data-open', '1') })
  const body = release.locator('.goal-body')
  await expect(body.locator('.steps-head')).toHaveText(/What’s missing\s*2 of 4 steps done/)
  await expect(body.locator('.step.now')).toHaveText(/Chat fixes after their designs/)
  // The opened goal is a full tint, never an edge accent.
  const panel = await release.evaluate(el => { const s = getComputedStyle(el, '::after'); return { bg: s.backgroundColor, left: s.borderLeftWidth } })
  expect(panel.bg).not.toBe('rgba(0, 0, 0, 0)'); expect(panel.left).toBe('0px')
  const labels = body.locator('.item .i-label')
  await expect(labels).toHaveCount(2 + 5 + 1)
  await expect(labels.first()).toContainText('Image builds work again')
  await expect(body.locator('.item').nth(2).locator('.i-state')).toHaveAttribute('data-tip', 'Queued')
  await expect(body.locator('.item').nth(7).locator('.i-state')).toHaveAttribute('data-tip', 'Paused')
  const more = body.getByRole('button', { name: /2 more queued/ }), done = body.getByRole('button', { name: /3 done/ })
  const folds = await controlStability(page, { more, toggle })
  await folds.check(async () => { await more.click(); await expect(labels).toHaveCount(10) })
  await expect(more).toHaveAttribute('aria-expanded', 'true')
  await done.click()
  await expect(labels).toHaveCount(13)
  // One agent-badge column: the goal's badge and its item's badge start at the same x.
  const goalBadge = release.locator('.goal-row .agents .live-chip'), itemBadge = body.locator('.i-badge .live-chip').first()
  await expect(goalBadge).toBeVisible(); await expect(itemBadge).toBeVisible()
  expect(Math.abs((await box(goalBadge)).x - (await box(itemBadge)).x)).toBeLessThanOrEqual(.5)
  await expect(body.locator('.item').first().locator('.i-live')).toHaveText('PR #707 · checks running')
  // The handed-over line closes the agents' part.
  const joed = goalRow(page, 'g-joed')
  await joed.locator('.goal-toggle').click()
  await expect(joed.locator('.item.handed .i-label')).toHaveText('Handed over: the switch to live is yours')
  await expect(joed.locator('.item.handed .i-detail')).toHaveText('Agents stop here; the switch happens on your Desk.')
  await shot(page, info, 'goals-1440-light-open')
  // A search that finds only items opens their goal and shows just those.
  await page.getByRole('searchbox', { name: 'Filter goals' }).fill('JOED-20')
  await expect(page.locator('.goal-list .goal')).toHaveCount(1)
  await expect(joed).toHaveAttribute('data-open', '1')
  await expect(joed.locator('.item .i-label')).toHaveText(['Results producer and sign-off'])
})

// ---------- Decision 7 and the amendment: the ETA popover ----------
test('the ETA popover says who set it and what it was, where the pace lands it, right-aligned and flipping above; passed ETAs are gold', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openGoals(page, world())
  const release = goalRow(page, 'g-r136'), eta = release.locator('.goal-eta')
  await expect(eta).toHaveText('~16:00')
  await expect(release.locator('.signal .sg-t')).toHaveText('Ø 0.9 %/h · 24 h')
  await eta.hover()
  const tip = page.getByRole('tooltip').filter({ hasText: 'set by camy' })
  await expect(tip).toBeVisible()
  await expect(tip).toContainText('set by camy 12:58 · was ~15:30 (set 10:12)')
  await expect(tip).toContainText(/At Ø 0\.9 %\/h this lands ~Sat 26 Sep\b/)
  await expect(tip).not.toContainText('Sept')
  const [tipBox, etaBox, card] = [await box(tip), await box(eta), await box(page.locator('.projects-card'))]
  expect(Math.abs(tipBox.right - (etaBox.right - 4))).toBeLessThanOrEqual(1.5)
  expect(tipBox.right).toBeLessThanOrEqual(card.right)
  await shot(page, info, 'goals-1440-light-eta')
  await page.mouse.move(10, 10)
  await expect(tip).toHaveCount(0)
  // A passed item ETA: gold clock, "passed · update pending", the minutes in the popover.
  const joed = goalRow(page, 'g-joed')
  await joed.locator('.goal-toggle').click()
  const passed = joed.locator('.item .eta-btn.passed')
  await expect(passed).toContainText('~13:30')
  await expect(passed).toContainText('passed · update pending')
  // Keyboard focus opens it too (the last input was a key, so focus is visible).
  await page.keyboard.press('Shift')
  await passed.focus()
  await expect(page.getByRole('tooltip').filter({ hasText: 'past' })).toContainText('30 min past · update pending')
  await page.mouse.move(10, 10)
  await passed.blur()
  // At the bottom of the scroll area the popover flips above its ETA.
  const last = goalRow(page, 'g-hnet').locator('.goal-eta')
  await last.evaluate(el => el.scrollIntoView({ block: 'end' }))
  await last.hover()
  const flip = page.getByRole('tooltip')
  await expect(flip).toBeVisible()
  await expect(flip).toHaveAttribute('data-side', 'above')
  // Done goals and items have no popover; done never projects.
  await expect(flip).not.toContainText('At Ø')
})

// ---------- r5: progress over time ----------
test('the bar opens a stepped chart in place; one global range; the signal row and its stall rule; nothing moves', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const w = world()
  const { seriesCalls, calls } = await openGoals(page, w)
  const release = goalRow(page, 'g-r136'), bar = release.getByRole('button', { name: /percent done\. Show progress over time/ })
  const row = release.locator('.goal-row'), pct = release.locator('.pct'), eta = release.locator('.goal-eta')
  const guard = await controlStability(page, { row, pct, eta, toggle: release.locator('.goal-toggle'), next: goalRow(page, 'g-joed').locator('.goal-toggle') })
  await guard.check(async () => { await bar.click(); await expect(release.locator('.chart')).toBeFocused() })
  await expect(release.getByRole('group', { name: 'Range' }).getByRole('button')).toHaveText(['24 h', '7 d', '30 d'])
  await expect(release.locator('.range [aria-pressed="true"]')).toHaveText('24 h')
  // The point line: arrow keys walk the buckets, bucket start · % (change) · keys.
  await page.keyboard.press('ArrowLeft')
  await expect(page.getByRole('tooltip')).toHaveText('13:00 · 31 % · no change')
  await page.keyboard.press('ArrowLeft')
  await expect(page.getByRole('tooltip')).toHaveText('12:00 · 31 % (+5) · AEON-1446 changed')
  await page.keyboard.press('ArrowLeft')
  await expect(page.getByRole('tooltip')).toHaveText('11:00 · 26 % · no change')
  await shot(page, info, 'goals-1440-light-chart')
  // Esc swaps back; focus returns to the bar.
  await guard.check(async () => { await page.keyboard.press('Escape'); await expect(bar).toBeFocused() })
  await page.keyboard.press('Enter')
  await expect(release.locator('.chart')).toBeFocused()
  // One global range, the one used last: 7 d reads per day everywhere and is kept on the server.
  await guard.check(async () => { await release.locator('.range').getByRole('button', { name: '7 d' }).click(); await expect(release.locator('.range [aria-pressed="true"]')).toHaveText('7 d') })
  await expect.poll(() => seriesCalls.includes('g-r136:7d')).toBe(true)
  await expect(release.locator('.signal .sg-t')).toHaveText(/^Ø \d+\.\d %\/day · 7 d$/)
  await expect(goalRow(page, 'g-hnet').locator('.signal .sg-t')).toContainText('%/day · 7 d')
  await expect.poll(() => calls.some(c => c.method === 'PUT' && c.path === '/api/preferences/goals' && (c.body as { value: { range?: string } }).value.range === '7d')).toBe(true)
  // The stall rule wins over the rate: moving items and no change for four hours or more.
  await expect(goalRow(page, 'g-joed').locator('.signal.stall .sg-t')).toHaveText('no progress for 6 h')
  // A young goal with fewer than three values says since when it has history.
  const young = goalRow(page, 'g-r137')
  await young.getByRole('button', { name: /Show progress over time/ }).click()
  await expect(young.locator('.nohist')).toHaveText('History since 13:00')
  await expect(young.locator('.range')).toHaveCount(0)
  // The signal slot reserves the longest form, so it never resizes with its text.
  const slot = await release.locator('.signal').evaluate(el => el.getBoundingClientRect().width)
  const sizer = await release.locator('.signal .sizer').evaluate(el => el.scrollWidth)
  expect(slot).toBeGreaterThanOrEqual(Math.min(sizer, 160 + 72) - 1)
  expect(errors).toEqual([])
})

// ---------- Empty states: no goals, nothing matches, nothing waits ----------
test('empty states replace the list in place and never show examples', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const w = world(), all = w.goals
  w.goals = []; w.desk.open = 0
  await mockGoals(page, w)
  await page.goto('/?view=goals')
  await expect(page.getByRole('heading', { name: 'No goals yet' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Waiting on you' })).toContainText('Nothing waits on you')
  await expect(page.locator('.goal-head')).toHaveCount(0)
  await shot(page, info, 'goals-1440-light-empty')
  w.goals = all
  await page.reload()
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
  await page.getByRole('searchbox', { name: 'Filter goals' }).fill('zzzz')
  await expect(page.getByRole('heading', { name: 'No goal matches “zzzz”' })).toBeVisible()
  await expect(page.getByText('Check the spelling, or search by project or ticket key.')).toBeVisible()
  await shot(page, info, 'goals-1440-light-filter-none')
  await page.getByRole('button', { name: 'Clear filter' }).click()
  await expect(page.locator('.goal-list .goal')).toHaveCount(4)
  await expect(page.getByRole('searchbox', { name: 'Filter goals' })).toBeFocused()
})

test('at 390 a goal item without an agent opens its ticket', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const w = world()
  await openGoals(page, w)
  const release = goalRow(page, 'g-r136')
  await release.locator('.goal-row').click({ position: { x: 120, y: 20 } })
  await expect(release).toHaveAttribute('data-open', '1')
  const item = release.locator('.item', { hasText: 'Browser tests fit the 20-minute CI limit' })
  await expect(item.locator('.i-keys')).toBeHidden()
  const link = item.locator('.phone-tickets a')
  await expect(link).toBeVisible()
  expect((await box(link)).height).toBeGreaterThanOrEqual(44)
  const href = await link.getAttribute('href')
  expect(href).toMatch(/^\/p\/AEON\/AEON-\d+$/)
  const overflow = await page.evaluate(() => { const main = document.getElementById('main')!; return main.scrollWidth - main.clientWidth })
  expect(overflow).toBeLessThanOrEqual(0)
  await link.click()
  await expect(page).toHaveURL(new RegExp(`${href}$`))
})

test('a list that cannot be read says so and offers Try again; the counts never pretend', async ({ page }) => {
  const w = world()
  await mockGoals(page, w, { failList: true })
  await page.goto('/?view=goals')
  const alert = page.locator('.goals-view [role="alert"]')
  await expect(alert).toContainText('Goals could not be loaded')
  await expect(alert).toContainText('the database did not answer')
  await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible()
})

// ---------- Fix round 3: Sep, a flat pace, and sorts whose columns are hidden ----------
test('September lands as Sep, a flat pace says no change, and hidden columns sort from Display', async ({ page }, info) => {
  for (const [width, height] of [[1440, 900], [1024, 800], [390, 844]] as const) {
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height })
      await page.emulateMedia({ colorScheme })
      const w = world()
      delete w.data.preferences.goals
      const flat = w.goals.find(goal => goal.id === 'g-hnet')!
      // Flat from before the 30-day window, so every range is 0 %/h and the goal is not young.
      flat.created = NOW - 800 * HOUR
      flat.hist = [[-900, 40, []], [-40, 40, []], [-10, 40, []]]
      await openGoals(page, w)
      const release = goalRow(page, 'g-r136')
      const haus = goalRow(page, 'g-hnet')
      const joed = goalRow(page, 'g-joed')
      await expect(release.locator('.signal .sg-t')).toHaveText('Ø 0.9 %/h · 24 h')
      await expect(haus.locator('.signal .sg-t')).toHaveText('no change · 24 h')
      await expect(haus.locator('.signal')).not.toHaveClass(/stall/)
      await expect(joed.locator('.signal.stall .sg-t')).toHaveText('no progress for 6 h')
      const ink = await page.evaluate(() => {
        const color = (id: string) => getComputedStyle(document.querySelector(`.goal[data-goal-id="${id}"] .signal`)!).color
        return { flat: color('g-hnet'), pace: color('g-r136'), stall: color('g-joed') }
      })
      expect(ink.flat).toBe(ink.pace)
      expect(ink.flat).not.toBe(ink.stall)
      const signalWidth = () => haus.locator('.signal').evaluate(el => el.getBoundingClientRect().width)
      const reserved = await signalWidth()
      // The list's own date stays "Sep"; the projection uses that formatter, so September is not "Sept".
      await release.locator('.goal-eta').hover()
      const tip = page.getByRole('tooltip').filter({ hasText: 'this lands' })
      await expect(tip).toContainText(/At Ø 0\.9 %\/h this lands ~Sat 26 Sep\b/)
      await expect(tip).not.toContainText('Sept')
      await expect(joed.locator('.goal-eta')).toHaveText('Thu 24 Sep 18:00')
      await page.mouse.move(8, 8)
      await expect(tip).toHaveCount(0)
      // One global range: the flat goal follows it, in the same slot, and the stall still wins.
      await release.locator('.bar-btn').click()
      await release.locator('.range').getByRole('button', { name: '7 d' }).click()
      await expect(haus.locator('.signal .sg-t')).toHaveText('no change · 7 d')
      await expect(joed.locator('.signal.stall .sg-t')).toHaveText('no progress for 6 h')
      expect(Math.abs(await signalWidth() - reserved)).toBeLessThanOrEqual(0.5)
      await release.locator('.range').getByRole('button', { name: '30 d' }).click()
      await expect(haus.locator('.signal .sg-t')).toHaveText('no change · 30 d')
      expect(Math.abs(await signalWidth() - reserved)).toBeLessThanOrEqual(0.5)
      await release.getByRole('button', { name: /Hide progress over time/ }).click()
      await expect(release.locator('.chart')).toHaveCount(0)

      const display = page.locator('.display-btn')
      const search = page.locator('.project-search')
      const views = page.locator('.view-seg')
      const headers = page.locator('.goal-head .h-stat')
      await display.click()
      const menu = page.getByRole('dialog', { name: 'Display options' })
      await expect(menu).toBeVisible()
      const pick = (name: string) => menu.getByRole('radio', { name, exact: true }).click()
      if (width > 1199) {
        await expect(headers).toHaveCount(3)
        await expect(headers.first()).toBeVisible()
        const guard = await controlStability(page, { display, search, views })
        await guard.check(async () => {
          await pick('Moving')
          await expect(display).toHaveAttribute('aria-label', 'Display, sorted by moving')
          await expect(display).not.toHaveAttribute('data-sort-dir')
          await expect(display.locator('.sort-state.shown')).toBeHidden()
          expect(await visibleWords(display)).toEqual(['Display'])
          await expect(page.getByRole('button', { name: 'Sort by moving, now descending' })).toHaveAttribute('data-sort-dir', 'desc')
        })
      } else {
        await expect(headers).toHaveCount(3)
        await expect(headers.first()).toBeHidden()
        const more = page.getByRole('button', { name: 'More: state filter, hide done, expand all' })
        const neighbors = width <= 760 ? { search, views, more } : { search, views }
        // Phones hide every header, so Display names every sort there; wider, only the hidden counts.
        expect(await visibleWords(display)).toEqual([width <= 760 ? 'ETA' : 'Display'])
        const guard = await controlStability(page, { display, ...neighbors })
        await guard.check(async () => {
          await pick('Moving')
          await expect(display).toHaveAttribute('aria-label', 'Display, sorted by moving, now descending')
          await expect(display).toHaveAttribute('data-sort-dir', 'desc')
          await expect(display.locator('.sort-name')).toHaveText('Moving')
          expect(await visibleWords(display)).toEqual(['Moving'])
        })
        for (const name of ['Queued', 'Done']) {
          await guard.check(async () => { await pick(name); await expect(display.locator('.sort-name')).toHaveText(name); expect(await visibleWords(display)).toEqual([name]) })
        }
        expect((await box(display)).height).toBeGreaterThanOrEqual(width <= 760 ? 44 : 28)
        guard.done()
      }
      await expect(menu).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(menu).toHaveCount(0)
      await shot(page, info, `goals-fix3-${width}-${colorScheme}`)
    }
  }
})

// ---------- Fix round 4: one Display box through every sort and view ----------
test('Display keeps one box through every sort at 1024, 820 and 390, shows only the active name, and a view switch never moves it', async ({ page }, info) => {
  const SORTS = ['Name', 'Progress', 'Project', 'Moving', 'Queued', 'Done', 'ETA']
  const COUNTS = new Set(['Moving', 'Queued', 'Done'])
  for (const [width, height] of [[1024, 800], [820, 1180], [390, 844]] as const) {
    await page.setViewportSize({ width, height })
    const w = world()
    delete w.data.preferences.goals
    await openGoals(page, w)
    const display = page.locator('.display-btn')
    const search = page.locator('.project-search')
    const views = page.locator('.view-seg')
    const phone = width <= 760
    const neighbors = phone ? { search, views, more: page.getByRole('button', { name: 'More: state filter, hide done, expand all' }) } : { search, views }
    await display.click()
    const menu = page.getByRole('dialog', { name: 'Display options' })
    await expect(menu).toBeVisible()
    const guard = await controlStability(page, { display, ...neighbors })
    for (const name of SORTS) {
      await guard.check(async () => {
        await menu.getByRole('radio', { name, exact: true }).click()
        await expect(menu.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true')
        // Only the active words are visible: never a sizing copy underneath.
        const shows = phone || COUNTS.has(name)
        await expect.poll(() => visibleWords(display), `${width}: ${name}`).toEqual([shows ? name : 'Display'])
        if (shows) await expect(display).toHaveAttribute('aria-label', new RegExp(`^Display, sorted by ${name === 'ETA' ? 'ETA' : name.toLowerCase()}, now (a|de)scending$`))
      })
    }
    guard.done()
    await shot(page, info, `goals-fix4-${width}`)
    await page.keyboard.press('Escape')
    await expect(menu).toHaveCount(0)
    if (phone) continue
    // Below 1200 px Display has the same box in all three views, so the view switch stays under the pointer.
    await guard.check(async () => { await page.getByRole('radio', { name: 'List view' }).click(); await expect(page.getByRole('searchbox', { name: 'Filter projects' })).toBeVisible() })
    expect(await visibleWords(display)).toEqual(['Display'])
    await guard.check(async () => { await page.getByRole('radio', { name: 'Cards view' }).click(); await expect(page.getByRole('radio', { name: 'Cards view' })).toHaveAttribute('aria-checked', 'true') })
    await guard.check(async () => { await page.getByRole('radio', { name: 'Goals view' }).click(); await expect(page.locator('.goal-list .goal').first()).toBeVisible() })
  }
})

// ---------- 1440 · 1024 · 390, light and dark ----------
for (const [width, height] of [[1440, 900], [1024, 800], [390, 844]] as const) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`${width} ${colorScheme}: essentials stay, nothing overflows, axe is clean`, async ({ page }, info) => {
      await page.setViewportSize({ width, height })
      await page.emulateMedia({ colorScheme })
      await openGoals(page, world())
      const release = goalRow(page, 'g-r136')
      await expect(release.locator('.goal-name')).toBeVisible()
      await expect(release.locator('.goal-eta')).toBeVisible()
      await expect(release.locator('.bar-btn')).toBeVisible()
      const overflow = await page.evaluate(() => { const main = document.getElementById('main')!; return main.scrollWidth - main.clientWidth })
      expect(overflow).toBeLessThanOrEqual(0)
      if (width === 390) {
        // Phones: name, ETA and progress; keys, counts, agents and waits stay in the opened goal; the rest is behind ⋯.
        await expect(release.locator('.goal-keys')).toBeHidden()
        await expect(release.locator('.stat').first()).toBeHidden()
        await expect(page.locator('.state-seg')).toBeHidden()
        const expandRow = page.locator('.head-expand')
        await expect(expandRow).toBeVisible()
        expect((await box(expandRow)).height).toBeGreaterThanOrEqual(44)
        const more = page.getByRole('button', { name: 'More: state filter, hide done, expand all' })
        expect((await box(more)).width).toBeGreaterThanOrEqual(44)
        await more.click()
        await expect(page.getByRole('menuitemradio', { name: /Moving/ })).toBeVisible()
        await shot(page, info, `goals-390-${colorScheme}-menu`)
        await page.keyboard.press('Escape')
        await release.locator('.goal-row').click({ position: { x: 120, y: 20 } })
        await expect(release).toHaveAttribute('data-open', '1')
        await expect(release.locator('.phone-next')).toContainText('Chat fixes after their designs')
        await release.locator('.bar-btn').click()
        await expect(release.locator('.chart')).toBeVisible()
        expect((await box(release.locator('.range button').first())).width).toBeGreaterThanOrEqual(42)
      } else {
        await release.locator('.goal-toggle').click()
        await release.locator('.bar-btn').click()
        await expect(release.locator('.chart')).toBeVisible()
      }
      await shot(page, info, `goals-${width}-${colorScheme}`)
      const scan = await new AxeBuilder({ page }).include('.projects-card').analyze()
      expect(scan.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
    })
  }
}
