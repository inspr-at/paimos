// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1494: what Goals needs before In flight retires, to Tick's approved design
// (build-notes decisions 1, 3, 4, 5 and 6; the note per Tick's 10-18 amendment).
// Risks: a status note that moves the list when it arrives or changes, reserves
// more than its one line, or pushes anything but the list (and only on "more");
// hours left that push the row; a dropped stream the footer never admits, or whose
// label moves the footer; an item note without its author; "today" missing from
// the Done fold; a goal showing another goal's percent. Screenshots go to each
// test's output directory.
import { test, expect, type Page, type Locator, type TestInfo } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
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
    if (!phone(size.width)) Object.assign(controls, { meta: note.locator('.n-meta'), eta: release.locator('.goal-eta') })
    const guard = await controlStability(page, controls)
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
