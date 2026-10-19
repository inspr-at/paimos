// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1494: what Goals needs before In flight retires, to Tick's approved design
// (build-notes decisions 1, 3, 4, 5 and 6; the note per Tick's 10-18 amendment).
// Risks: a status note that moves the list when it arrives or changes, reserves
// more than its one line, or pushes anything but the list (and only on "more");
// hours left that push the row; a dropped stream the footer never admits, or whose
// label moves the footer; an item note without its author; "today" missing from
// the Done fold; a goal showing another goal's percent. Addendum (Markus's item-row
// feedback, Tick's decisions A–D): items that hug the left edge instead of nesting
// under their goal, an empty Moving → Progress span on wide screens, titles squeezed
// into a narrow column at 1024, goal names cut to one line, and any of it moving a
// control when a goal opens or a live update lands. Tick's 10-18 check: wide titles
// cut short while the span to their right stays empty. Screenshots go to each test's
// output directory.
import { test, expect, type Page, type Locator, type TestInfo } from '@playwright/test'
import { fixtures, liveAgent, mockWork, watchErrors } from './work-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: 'Europe/Vienna', locale: 'en-GB' })
const NOW = Date.parse('2026-10-18T17:00:00Z') // 19:00 in Vienna
const HOUR = 3_600_000
const iso = (ms: number) => new Date(ms).toISOString()
const LONG_NOTE = 'Since this morning: 9 items merged, 2 of them for Release 136. The cut is planned for Wednesday, once the CI targets are met. One decision is waiting on you: which identity agent reviews run as for the gatekeeper. Two builders are paused for capacity until the image fix lands. The next note follows the evening hand-check, around 21:00; nothing else needs you before then.'
const VIEWPORTS = [{ width: 1440, height: 900 }, { width: 1024, height: 800 }, { width: 390, height: 844 }] as const

test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

// ---------- The world ----------
type State = 'moving' | 'queued' | 'paused' | 'done'
let next = 0
function item(label: string, state: State, extra: Record<string, unknown> = {}) {
  next++
  return { id: `00000000-0000-4000-8000-${String(next).padStart(12, '0')}`, ref: `o${next}`, label, note: '', position: next, keys: [`AEON-${1440 + next}`], state, session_ids: [], eta: { source: 'none', partial: false }, passed: false, coverage: { estimated_leaves: 1, total_leaves: 1 }, weight: 1, handed_over: false, ...extra }
}
function goal(id: string, title: string, items: ReturnType<typeof item>[], extra: Record<string, unknown> = {}) {
  const states = { moving: 0, queued: 0, paused: 0, done: 0 }
  for (const outcome of items) states[outcome.state as State]++
  // Every item weighs the same here, so the rollup is the share of done items (never another goal's).
  const pct = items.length ? Math.round(100 * states.done / items.length) : 0
  return {
    id, key: id.toUpperCase(), title, kind: 'release', project_id: 'p-aeon', workspace: false, expansion: 'full', counts: { outcomes: items.length, tickets: items.length },
    items, steps: [], next_step: null, projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 0, question_ids: [] },
    progress: { pct, computed_pct: pct, source: 'computed', basis: 'estimate', total: items.length, done: states.done, items_without_estimate: 0, items_partially_estimated: 0, remaining_hours: 0, remaining_unestimated: 0 },
    eta: { source: 'none', partial: false }, passed: false, states, done: false, stall: { stalled: false, since: iso(NOW - HOUR) }, read_through: 10, generation: 1, ...extra,
  }
}
function world() {
  next = 0
  const release = goal('g-r136', 'Release 136 · steady and complete', [
    item('Agent reviews become authenticated review runs the merge gate can rely on', 'moving', { note: 'Hand-check of 5 review runs, then the gate goes live', note_by: 'p-lead', note_by_name: 'AEON-LEAD', note_at: '2026-10-18T16:52:00Z', eta: { source: 'agent', text: '~21:00', at: '2026-10-18T19:00:00Z', partial: false } }),
    item('Chat thread and composer for people', 'queued'),
    item('Panel flicker fix', 'done', { merged_at: '2026-10-18T13:10:00Z' }),
    item('Desk empty state', 'done', { merged_at: '2026-10-16T09:00:00Z' }),
  ], { eta: { source: 'agent', text: 'Mon 19 Oct', at: '2026-10-19T16:00:00Z', partial: false } })
  ;(release.progress as Record<string, unknown>).remaining_hours = 39.6
  const unknown = goal('g-r137', 'Release 137 · better lists', [item('Group figures in grouped lists', 'queued')], { eta: { source: 'agent', text: 'after 136', partial: false } })
  return {
    data: fixtures(), goals: [release, unknown], reads: 0,
    note: { text: LONG_NOTE, by: 'p-lead', by_name: 'LEAD', set_at: iso(NOW - 2 * HOUR) } as { text: string; by: string; by_name: string; set_at: string } | null,
  }
}
type World = ReturnType<typeof world>

// A controllable event stream: every EventSource the app opens is one of these.
// mockWork's quiet stream steps aside for it (its init script checks the name).
async function installStreams(page: Page) {
  await page.addInitScript(() => {
    const streams: GoalStream[] = []
    class GoalStream extends EventTarget {
      readyState = 0
      onerror: ((event: Event) => void) | null = null
      onopen: ((event: Event) => void) | null = null
      closed = false
      timer: ReturnType<typeof setInterval>
      constructor() {
        super()
        streams.push(this)
        this.timer = setInterval(() => { if (this.readyState === 1) this.dispatchEvent(new MessageEvent('stream.ping', { data: '{}' })) }, 5_000)
        queueMicrotask(() => this.ready(false))
      }
      ready(resumed: boolean) {
        if (this.closed) return
        this.readyState = 1
        this.onopen?.(new Event('open'))
        this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 10, resumed }) }))
      }
      close() { this.closed = true; this.readyState = 2; clearInterval(this.timer) }
    }
    const open = () => streams.filter(stream => !stream.closed)
    Object.assign(window, {
      EventSource: GoalStream,
      goalStreams: {
        emit(type: string, id: number) { for (const stream of open()) stream.dispatchEvent(new MessageEvent(type, { data: '{}', lastEventId: String(id) })) },
        // The connection drops and the browser keeps retrying (readyState CONNECTING).
        drop() { for (const stream of open()) { stream.readyState = 0; stream.onerror?.(new Event('error')) } },
        resume() { for (const stream of open()) stream.ready(true) },
      },
    })
  })
}
async function mockGoals(page: Page, w: World) {
  await installStreams(page)
  await mockWork(page, w.data)
  await page.route(/\/api\/goals(\?.*)?$/, route => {
    w.reads++
    return route.fulfill({ json: {
      items: w.goals, totals: { moving: 1, queued: 2, paused: 0, done: 2 }, totals_partial: false, quiet_projects: { count: 2, keys: ['GLINT', 'STUDIO'] }, truncated: false,
      read_through: 10 + w.reads, generation: w.reads, ...(w.note ? { status_note: w.note } : {}), eta_last_set_at: '2026-10-18T16:41:00Z',
    }, headers: { 'Aeon-Contract': 'goals/1.5' } })
  })
  await page.route(/\/api\/goals\/[^/]+\/series\?/, route => route.fulfill({ json: { range: '24h', expansion: 'full', points: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/goals\/[^/]+\/history/, route => route.fulfill({ json: { items: [], read_through: 10, generation: 1 } }))
  await page.route(/\/api\/nodes\/g-[a-z0-9]+$/, route => route.fulfill({ json: { id: 'g', key: 'G', kind_id: 'k-goal', title: 'Goal', body: '', fields: {}, state: 'open', parent_id: null, position: 1, created_at: iso(NOW - 300 * HOUR), updated_at: iso(NOW) } }))
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: {
    items: [{ id: 'q-gate', kind: 'question', project_id: 'p-aeon', revision: 1, title: 'Which identity should agent reviews run as for the gatekeeper go-live?', created_at: iso(NOW - 3 * HOUR), held: true, href: '', source: '' }],
    counts: { open: 1, held: 1, chores: 0 }, has_more: false, as_of: iso(NOW),
  } }))
}
async function openGoals(page: Page, w: World) {
  await mockGoals(page, w)
  await page.goto('/?view=goals')
  await expect(page.locator('.goal-list .goal').first()).toBeVisible()
}
const box = (locator: Locator) => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
async function shot(page: Page, info: TestInfo, name: string) { await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true }) }
const phone = (width: number) => width <= 760
// The line ends in "…": the text is cut by the box, not shortened in the DOM.
const cut = (locator: Locator) => locator.evaluate(el => getComputedStyle(el).textOverflow === 'ellipsis' && el.scrollWidth > el.clientWidth)
const emitNote = (page: Page, id: number) => page.evaluate(id => (window as unknown as { goalStreams: { emit(type: string, id: number): void } }).goalStreams.emit('goal.status_note_changed', id), id)

for (const size of VIEWPORTS) {
  test(`${size.width}: the status note keeps its reserve through a live update, hours left sit under LANDS, items carry their note and the Done fold counts today; a dropped stream says Reconnecting in place`, async ({ page }, info) => {
    await page.setViewportSize(size)
    const errors = watchErrors(page)
    const w = world()
    await openGoals(page, w)
    const note = page.locator('.ws-note'), release = page.locator('.goal[data-goal-id="g-r136"]')
    await expect(note).toBeVisible()
    // One line at every width, reserved and no more: the note cut with "…" and "more"; no fold chevron.
    await expect(note.locator('.n-toggle')).toHaveCount(0)
    await expect(note.locator('.n-more')).toHaveText('more')
    expect((await box(note.locator('.n-more'))).height).toBeGreaterThan(0)
    expect(Math.round((await box(note.locator('.n-body'))).height)).toBe(20)
    await expect(note.locator('.n-text')).toHaveText(LONG_NOTE)
    expect(await cut(note.locator('.n-text'))).toBe(true)
    // Each goal's percent is its own items' rollup; neither is set by hand, so no pen.
    await expect(release.locator('.pct')).toHaveText('50%')
    await expect(page.locator('.goal[data-goal-id="g-r137"] .pct')).toHaveText('0%')
    await expect(page.locator('.goal-progress .pct svg')).toHaveCount(0)
    if (phone(size.width)) {
      // The byline waits for "more".
      await expect(note.locator('.n-meta')).toBeHidden()
      expect((await box(note)).height).toBeGreaterThanOrEqual(44)
      await expect(release.locator('.goal-eta .left')).toBeHidden()
    } else {
      await expect(note.locator('.n-meta')).toHaveText('LEAD · 2 h ago')
      await expect(page.locator('.goal[data-goal-id="g-r137"] .of')).toHaveText('0 of 1 done')
      await expect(release.locator('.goal-eta .left')).toHaveText('~40 h left')
      await expect(page.locator('.goal[data-goal-id="g-r137"] .goal-eta .left')).toHaveText('')
      await expect(release.locator('.goal-eta')).toHaveAccessibleName(/about 40 h left/)
    }
    const fresh = page.locator('.fresh')
    await expect(fresh).toHaveAttribute('data-live', '1')
    await expect(fresh.locator('.f-live')).toHaveText('Live · ETAs last set 18:41')

    // 0 px: a live note change, hovering rows and a dropped stream move no control.
    // The note's line is measured too: a shorter note keeps its one reserved line.
    const controls: Record<string, Locator> = { line: note.locator('.n-body'), toggle: release.locator('.goal-toggle'), desk: page.getByRole('link', { name: /Open Desk/ }).first(), fresh }
    if (!phone(size.width)) Object.assign(controls, { eta: release.locator('.goal-eta') })
    const guard = await controlStability(page, controls)
    // The byline is right-aligned and its age changes with the note: its right edge and line stay.
    const byline = async () => { const b = await box(note.locator('.n-meta')); return { right: b.x + b.width, y: b.y } }
    const bylineBefore = phone(size.width) ? null : await byline()
    await guard.check(async () => {
      const before = w.reads
      w.note = { text: 'Release 136 cut moved to Thursday.', by: 'p-lead', by_name: 'LEAD', set_at: iso(NOW - 60_000) }
      await emitNote(page, 11)
      await expect(note.locator('.n-text')).toHaveText('Release 136 cut moved to Thursday.')
      // It fits its line: no "…", no "more".
      await expect(note.locator('.n-more')).toHaveCount(0)
      expect(await cut(note.locator('.n-text'))).toBe(false)
      expect(w.reads).toBeGreaterThan(before)
    })
    if (bylineBefore) {
      await expect(note.locator('.n-meta')).not.toHaveText('LEAD · 2 h ago')
      const after = await byline()
      expect(Math.abs(after.right - bylineBefore.right)).toBeLessThanOrEqual(0.5)
      expect(Math.abs(after.y - bylineBefore.y)).toBeLessThanOrEqual(0.5)
    }
    await guard.check(async () => { await release.locator('.goal-row').hover({ position: { x: 60, y: 20 } }); await page.locator('.goal[data-goal-id="g-r137"] .goal-row').hover({ position: { x: 60, y: 20 } }) })
    await guard.check(async () => {
      await page.evaluate(() => (window as unknown as { goalStreams: { drop(): void } }).goalStreams.drop())
      await expect(fresh).toHaveAttribute('data-live', '0')
      await expect(fresh.locator('.f-off')).toHaveText(/^Reconnecting · last update \d\d:\d\d$/)
      await expect(fresh.locator('.f-off')).toBeVisible()
    })
    await shot(page, info, `goals-gaps-${size.width}-reconnecting`)
    await guard.check(async () => {
      await page.evaluate(() => (window as unknown as { goalStreams: { resume(): void } }).goalStreams.resume())
      await expect(fresh).toHaveAttribute('data-live', '1')
    })

    // The opened goal: the agent's note with its author and time, and today's merges.
    await release.locator('.goal-toggle').click()
    await expect(release).toHaveAttribute('data-open', '1')
    const noted = release.locator('.item', { hasText: 'Agent reviews become authenticated' })
    await expect(noted.locator('.i-live.is-note')).toHaveText('Hand-check of 5 review runs, then the gate goes live · AEON-LEAD 18:52')
    await expect(noted.locator('.i-live.is-note svg')).toBeVisible()
    await expect(release.locator('.fold-btn', { hasText: 'done' })).toHaveText(/2\s*done\s*· 1 today\s*· show/)
    if (phone(size.width)) {
      expect((await box(release.locator('.fold-btn', { hasText: 'done' }))).height).toBeGreaterThanOrEqual(44)
      const overflow = await page.evaluate(() => { const main = document.getElementById('main')!; return main.scrollWidth - main.clientWidth })
      expect(overflow).toBeLessThanOrEqual(0)
    }
    await shot(page, info, `goals-gaps-${size.width}-open`)
    expect(errors).toEqual([])
  })

  test(`${size.width}: one line is reserved, only "more" pushes the list, "less" folds it back, a new note starts on one line, and #no-note renders nothing`, async ({ page }, info) => {
    await page.setViewportSize(size)
    const w = world()
    await openGoals(page, w)
    const note = page.locator('.ws-note'), filter = page.locator('.project-search'), more = note.locator('.n-more')
    const listTop = async () => (await box(page.locator('.goal[data-goal-id="g-r136"] .goal-toggle'))).y
    const oneLine = await listTop()
    await expect(note).toHaveAttribute('data-full', '0')
    await shot(page, info, `goals-gaps-${size.width}-note-one-line`)

    // "more" expands in place: the person's click may push the list down, never the toolbar (nor, on wide screens, the byline).
    const above = await controlStability(page, phone(size.width) ? { filter } : { filter, meta: note.locator('.n-meta') })
    await above.check(async () => {
      await more.click()
      await expect(note).toHaveAttribute('data-full', '1')
    })
    // The whole note wraps over several lines, uncut.
    await expect(note.locator('.n-text')).toHaveText(LONG_NOTE)
    expect(await note.locator('.n-text').evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
    expect((await box(note.locator('.n-text'))).height).toBeGreaterThan(40)
    await expect(more).toHaveText('less')
    await expect(more).toBeFocused()
    expect((await box(note.locator('.n-body'))).height).toBeGreaterThan(40)
    expect(await listTop()).toBeGreaterThan(oneLine + 20)
    if (phone(size.width)) await expect(note.locator('.n-meta')).toHaveText('LEAD · 2 h ago')
    await shot(page, info, `goals-gaps-${size.width}-note-full`)

    // "less" folds it back to its one line, and the list returns to where it was.
    await above.check(async () => {
      await more.click()
      await expect(note).toHaveAttribute('data-full', '0')
    })
    await expect(more).toHaveText('more')
    expect(Math.round((await box(note.locator('.n-body'))).height)).toBe(20)
    expect(Math.abs(await listTop() - oneLine)).toBeLessThanOrEqual(0.5)

    // A new note arriving while expanded starts on one line: "more" belonged to the note it was clicked for.
    await more.click()
    await expect(note).toHaveAttribute('data-full', '1')
    w.note = { text: `${LONG_NOTE} Update: the cut moved to Thursday.`, by: 'p-lead', by_name: 'LEAD', set_at: iso(NOW - 60_000) }
    await emitNote(page, 12)
    await expect(note.locator('.n-text')).toHaveText(/Update: the cut moved to Thursday\.$/)
    await expect(note).toHaveAttribute('data-full', '0')
    await expect(more).toHaveText('more')
    expect(Math.abs(await listTop() - oneLine)).toBeLessThanOrEqual(0.5)

    // No note: nothing renders, and Waiting on you follows the toolbar directly.
    w.note = null
    await emitNote(page, 13)
    await expect(page.locator('.ws-note')).toHaveCount(0)
    const waiting = page.getByRole('region', { name: 'Waiting on you' })
    await expect(waiting).toBeVisible()
    await shot(page, info, `goals-gaps-${size.width}-no-note`)
  })
}

// ---------- Addendum: item rows nest under the goal and use the wide span (Tick's decisions A–D) ----------
const LONG_GOAL = 'Release 136 · steady and complete, with the gatekeeper live and every review run authenticated'
// Longer than the whole span to the ETA at 1440, so the wide row still cuts it.
const LONG_ITEM = 'People see in one place what every session is doing, what it waits for and when it lands, without asking anyone for a status update first or opening a single ticket'
const STEPS = [{ id: 'cut', text: 'Open: 135 tagged and nightly green', state: 'done' }, { id: 'chat', text: 'Chat fixes after their designs', state: 'now' }, { id: 'ci', text: 'CI targets met (S3–S5)', state: 'later' }]
const NOTED = 'Agent reviews become authenticated'
const NOTE = 'Hand-check of 5 review runs, then the gate goes live for every merge in the queue'
const NOTE_LINE = `${NOTE} · AEON-LEAD 18:52`
function nestedWorld() {
  const w = world()
  const release = w.goals[0]!
  const items = release.items as ReturnType<typeof item>[]
  // The noted item gets an agent and a note longer than its span; a second moving item has a long label and two tickets.
  Object.assign(items[0]!, { session_ids: ['s-camy'], note: NOTE })
  items.splice(1, 0, item(LONG_ITEM, 'moving', { keys: ['AEON-1402', 'AEON-1409'], eta: { source: 'agent', text: '~22:00', at: '2026-10-18T20:00:00Z', partial: false } }))
  Object.assign(release, { title: LONG_GOAL, steps: STEPS, next_step: STEPS[1] })
  w.data.live.push(liveAgent({ project_id: 'p-aeon', session_id: 's-camy', principal_id: '44444444-4444-4444-8444-444444444444', name: 'camy', harness: 'codex', ticket: { id: 'n-a1', key: 'AEON-1441', title: 'Agent reviews', project_id: 'p-aeon' }, since: iso(NOW - 30 * 60_000), heartbeat_at: iso(NOW - 30_000) }))
  return w
}
// The grid's column tracks as [left, right] in viewport x.
const columns = (page: Page) => page.locator('.goal-grid').evaluate(el => {
  const style = getComputedStyle(el), gap = parseFloat(style.columnGap)
  let x = el.getBoundingClientRect().left + parseFloat(style.paddingLeft)
  return style.gridTemplateColumns.split(' ').map(parseFloat).map(width => { const track = [x, x + width] as const; x += width + gap; return track })
})
const lines = (locator: Locator) => locator.evaluate(el => Math.round(el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight)))
const emitItems = (page: Page, id: number) => page.evaluate(id => (window as unknown as { goalStreams: { emit(type: string, id: number): void } }).goalStreams.emit('goal.items_changed', id), id)

for (const size of [{ width: 1440, height: 900 }, { width: 1024, height: 800 }, { width: 900, height: 800 }, { width: 390, height: 844 }] as const) {
  const wide = size.width >= 1200
  const rows = phone(size.width) ? 'phones keep their stacked item rows with every ticket link'
    : `items nest under their goal at the step icons' x, ${wide ? 'one line each with the live line in the Moving → Progress span' : 'labels run from the key to the ETA in at most two lines with the badge in the meta line'}`
  test(`${size.width}: ${rows}; goal names wrap to two lines; opening and a live update move nothing`, async ({ page }, info) => {
    await page.setViewportSize(size)
    const errors = watchErrors(page)
    const w = nestedWorld()
    await openGoals(page, w)
    await expect(page.locator('.goal-grid')).toHaveAttribute('data-items', wide ? 'wide' : 'stacked')
    const release = page.locator('.goal[data-goal-id="g-r136"]'), other = page.locator('.goal[data-goal-id="g-r137"]')

    // D: the long goal name wraps to two lines (the rest is its tip); "Next" stays one line.
    const name = release.locator('.goal-name')
    expect(await lines(name)).toBe(2)
    expect(await name.evaluate(el => getComputedStyle(el).whiteSpace)).toBe('normal')
    await expect(name).toHaveAttribute('data-tip', LONG_GOAL)
    if (!phone(size.width)) {
      expect(await release.locator('.goal-next .txt').evaluate(el => getComputedStyle(el).whiteSpace)).toBe('nowrap')
      expect(await lines(release.locator('.goal-next'))).toBe(1)
    }

    // 0 px: opening the goal moves none of its controls, and no other goal's name sideways.
    const controls: Record<string, Locator> = { toggle: release.locator('.goal-toggle'), name, filter: page.locator('.project-search') }
    if (!phone(size.width)) Object.assign(controls, { eta: release.locator('.goal-eta'), lands: page.getByRole('button', { name: /^Sort by lands/ }) })
    const guard = await controlStability(page, controls)
    const across = async () => { const b = await box(other.locator('.goal-name')); return [b.x, b.width] }
    const otherBefore = await across()
    await guard.check(async () => { await release.locator('.goal-toggle').click(); await expect(release).toHaveAttribute('data-open', '1') })
    const otherAfter = await across()
    expect(Math.abs(otherAfter[0]! - otherBefore[0]!)).toBeLessThanOrEqual(.5)
    expect(Math.abs(otherAfter[1]! - otherBefore[1]!)).toBeLessThanOrEqual(.5)

    const items = release.locator('.item'), noted = release.locator('.item', { hasText: NOTED }), long = release.locator('.item', { hasText: LONG_ITEM.slice(0, 40) })
    await expect(items).toHaveCount(3)
    if (!phone(size.width)) {
      const cols = await columns(page)
      // A: every item's state icon at the step icons' x, inside the goal's column; the key follows it.
      const step = await box(release.locator('.step .s-ic svg').first())
      for (let i = 0; i < 3; i++) {
        const icon = await box(items.nth(i).locator('.i-state > :first-child')), key = await box(items.nth(i).locator('.i-keys'))
        expect(Math.abs(icon.x - step.x), `item ${i} icon x`).toBeLessThanOrEqual(1)
        expect(icon.x, `item ${i} in the goal column`).toBeGreaterThanOrEqual(cols[2]![0] - .5)
        expect(key.x, `item ${i} key after its icon`).toBeGreaterThan(icon.x + icon.width)
      }
      // One-ticket items start their labels at one x.
      expect(Math.abs((await box(noted.locator('.i-label'))).x - (await box(items.nth(2).locator('.i-label'))).x)).toBeLessThanOrEqual(.5)
      // The fold lines' chevrons centre on the same x, and their words start at the key's x.
      const chevron = await box(release.locator('.fold-btn .f-ic svg').first()), count = await box(release.locator('.fold-btn b').first())
      expect(Math.abs(chevron.x + chevron.width / 2 - (step.x + step.width / 2))).toBeLessThanOrEqual(1)
      expect(Math.abs(count.x - (await box(noted.locator('.i-keys'))).x)).toBeLessThanOrEqual(1)
      // The second ticket is in the "+1" tip.
      await expect(long.locator('.i-keys .more')).toHaveText('+1')
      await expect(long.locator('.i-keys .more')).toHaveAttribute('data-tip', 'AEON-1409')

      if (wide) {
        // B (Tick 10-18): one line per item. With nothing beside it, the label runs on to the ETA column
        // and is cut only when it does not fit there.
        for (let i = 0; i < 3; i++) expect(await lines(items.nth(i).locator('.i-label')), `item ${i} one line`).toBe(1)
        const chat = release.locator('.item', { hasText: 'Chat thread and composer' })
        for (const [name, bare] of [['chat', chat], ['long', long]] as const) {
          const b = await box(bare.locator('.i-label'))
          expect(Math.abs(b.x + b.width - cols[8]![1]), `${name} label reaches the ETA column`).toBeLessThanOrEqual(.5)
        }
        expect(await cut(chat.locator('.i-label')), 'a label that fits before the ETA column is whole').toBe(false)
        expect(await chat.locator('.i-label').getAttribute('data-tip')).toBeNull()
        expect(await cut(long.locator('.i-label'))).toBe(true)
        await expect(long.locator('.i-label')).toHaveAttribute('data-tip', LONG_ITEM)
        // With a live line, the label keeps the goal and agent columns, at least 320 px, and stops before
        // the badge, which sits at the agent column's end (the goal's own badge at its start).
        const live = noted.locator(':scope > .i-live'), labelBox = await box(noted.locator('.i-label')), liveBox = await box(live)
        const goalBadge = await box(release.locator('.goal-row .agents .live-chip')), itemBadge = await box(noted.locator('.i-head > .i-badge .live-chip'))
        expect(labelBox.width, 'a label beside a live line').toBeGreaterThanOrEqual(320)
        expect(labelBox.x + labelBox.width).toBeLessThanOrEqual(itemBadge.x)
        expect(Math.abs(goalBadge.x - cols[3]![0])).toBeLessThanOrEqual(.5)
        expect(itemBadge.x).toBeGreaterThanOrEqual(cols[3]![0] - .5)
        expect(Math.abs(itemBadge.x + itemBadge.width - cols[3]![1]), 'item badge at the agent column end').toBeLessThanOrEqual(.5)
        // The live line takes Moving up to the ETA column: its icon, its author and time readable,
        // the note cut with "…" and shown whole on hover and on keyboard focus.
        await expect(live).toHaveText(NOTE_LINE)
        await expect(live.locator('svg')).toBeVisible()
        expect(Math.abs(liveBox.x - cols[4]![0])).toBeLessThanOrEqual(.5)
        expect(Math.abs(liveBox.x + liveBox.width - cols[8]![1])).toBeLessThanOrEqual(.5)
        expect(liveBox.height).toBeLessThanOrEqual(20.5)
        expect(Math.abs(liveBox.y + liveBox.height / 2 - (labelBox.y + labelBox.height / 2))).toBeLessThanOrEqual(1.5)
        const by = await box(live.locator('.by'))
        expect(by.width).toBeGreaterThan(0)
        expect(by.x + by.width).toBeLessThanOrEqual(cols[8]![1] + .5)
        const tx = live.locator('.tx')
        expect(await cut(tx)).toBe(true)
        await expect(tx).toHaveAttribute('data-tip', NOTE_LINE)
        await tx.hover()
        await expect(page.locator('.tooltip')).toHaveText(NOTE_LINE)
        await page.keyboard.press('Tab')
        await tx.focus()
        await expect(tx).toBeFocused()
        await expect(page.locator('.tooltip')).toHaveText(NOTE_LINE)
        await tx.blur()
        await page.mouse.move(1, 1)
        await expect(page.locator('.tooltip')).toHaveCount(0)
        for (let i = 0; i < 3; i++) expect((await box(items.nth(i))).height, `item ${i} row`).toBeLessThanOrEqual(32.5)
      } else {
        // C: the label runs from the key to the ETA, at least 280 px wide and at most two lines.
        for (let i = 0; i < 3; i++) {
          const label = items.nth(i).locator('.i-label'), b = await box(label)
          expect(b.width, `item ${i} label width`).toBeGreaterThanOrEqual(280)
          expect(b.x + b.width, `item ${i} label reaches the ETA`).toBeGreaterThan(cols[3]![1])
          expect(await lines(label), `item ${i} lines`).toBeLessThanOrEqual(2)
        }
        expect(await lines(long.locator('.i-label'))).toBe(2)
        // Here "+1" sits under its key, so every label starts at one x.
        expect(Math.abs((await box(long.locator('.i-label'))).x - (await box(noted.locator('.i-label'))).x)).toBeLessThanOrEqual(.5)
        expect((await box(long.locator('.i-keys .more'))).y).toBeGreaterThan((await box(long.locator('.i-keys a'))).y)
        // The meta line under the label: the live line, then the badge's faces (no name).
        const meta = noted.locator('.i-meta'), label = await box(noted.locator('.i-label'))
        const live = await box(meta.locator('.i-live')), face = await box(meta.locator('.i-badge .live-chip'))
        await expect(meta.locator('.i-live')).toHaveText(NOTE_LINE)
        expect(live.y).toBeGreaterThanOrEqual(label.y + label.height - .5)
        expect(Math.abs(live.x - label.x)).toBeLessThanOrEqual(.5)
        expect(face.x).toBeGreaterThan(live.x + live.width)
        await expect(meta.locator('.row-name')).toBeHidden()
        await expect(noted.locator(':scope > .i-live, :scope > .i-badge')).toHaveCount(0)
      }
    } else {
      // Phones keep their stacked rows and every ticket link; nothing overflows.
      await expect(long.locator('.phone-tickets a')).toHaveCount(2)
      await expect(noted.locator('.i-live')).toHaveText(NOTE_LINE)
      const overflow = await page.evaluate(() => { const main = document.getElementById('main')!; return main.scrollWidth - main.clientWidth })
      expect(overflow).toBeLessThanOrEqual(0)
    }

    // 0 px: a live update of the item's note moves none of the item controls or the next item.
    const itemControls: Record<string, Locator> = { toggle: release.locator('.goal-toggle'), icon: noted.locator('.i-state'), label: noted.locator('.i-label'), next: long.locator('.i-label'), fold: release.locator('.fold-btn').first() }
    if (!phone(size.width)) Object.assign(itemControls, { key: noted.locator('.i-keys a'), eta: noted.locator('.i-eta'), last: items.nth(2).locator('.i-state') })
    const update = await controlStability(page, itemControls)
    await update.check(async () => {
      const before = w.reads
      const outcome = (w.goals[0]!.items as ReturnType<typeof item>[])[0]! as Record<string, unknown>
      outcome.note = 'The gate is live; watching the first twenty merges before the next step'
      outcome.note_at = '2026-10-18T16:58:00Z'
      await emitItems(page, 20)
      await expect(noted.locator('.i-live .tx')).toHaveText('The gate is live; watching the first twenty merges before the next step')
      expect(w.reads).toBeGreaterThan(before)
    })
    await shot(page, info, `goals-nest-${size.width}-light`)
    await page.emulateMedia({ colorScheme: 'dark' })
    await shot(page, info, `goals-nest-${size.width}-dark`)
    expect(errors).toEqual([])
  })
}
