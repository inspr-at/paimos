/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { RouterView, createMemoryHistory, createRouter, type Router } from 'vue-router'

import i18n from '@/i18n'
import { COMPACT_CONVERSATION_QUERY } from '@/components/agent-mode/agentModePresentation'
import { TOMBSTONE_TTL_MS } from '@/composables/agent-mode/agentModeOrdering'
import { useConfirm } from '@/composables/useConfirm'
import { AgentModeLoadError, type AgentModeSnapshot, type AgentModeSnapshotLoader } from '@/services/agentMode'
import { AGGREGATE_FLAG_KEYS, AGGREGATE_LANDING_KEYS, AGGREGATE_STAGE_KEYS } from '@/services/agentModeAggregateSchema'
import { makeFixtureAggregateSnapshot, makeFixtureDelivery, makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { laneKeyFor, normalizeWireSnapshot, type WireSnapshot } from '@/services/agentModeTransport'
import { useChangesStore } from '@/stores/changes'
import { useAuthStore } from '@/stores/auth'
import { lsAgentModeSelectedKey } from '@/constants/storage'
import AgentModeView from './AgentModeView.vue'

function snapshot(wire: WireSnapshot): AgentModeSnapshot {
  return normalizeWireSnapshot(wire, Date.now())
}

/** Fixture snapshot without the given delivery ids. */
function snapshotWithout(ids: string[], revision = 'fx-10-2'): AgentModeSnapshot {
  const wire = makeFixtureSnapshot(10)
  wire.rows = wire.rows!.filter((d) => !ids.includes(String(d.delivery_id)))
  wire.cursor = revision
  return snapshot(wire)
}

interface MutableWireCountSet {
  active_total: number
  current_stage: Record<string, number>
  flags: Record<string, number>
  landing: Record<string, number>
}

interface MutableWireAggregates {
  structural_revision: string
  root: MutableWireCountSet
  projects: Array<{
    project_id: number
    project_key: string
    project_name: string
    counts: MutableWireCountSet
    lanes: Array<{
      lane_key: string
      epic_id: number | null
      epic_title: string | null
      counts: MutableWireCountSet
    }>
  }>
  attention: { total: number; items: Array<{ delivery_id: string }> }
}

function mutableAggregates(wire: WireSnapshot): MutableWireAggregates {
  return wire.aggregates as MutableWireAggregates
}

function subtractCounts(target: MutableWireCountSet, removed: MutableWireCountSet) {
  target.active_total -= removed.active_total
  for (const key of AGGREGATE_STAGE_KEYS) target.current_stage[key] -= removed.current_stage[key]
  for (const key of AGGREGATE_FLAG_KEYS) target.flags[key] -= removed.flags[key]
  for (const key of AGGREGATE_LANDING_KEYS) target.landing[key] -= removed.landing[key]
}

function retainAttentionReferences(wire: WireSnapshot) {
  const aggregate = mutableAggregates(wire)
  const ids = new Set((wire.rows ?? []).map((row) => String(row.delivery_id)))
  aggregate.attention.total = aggregate.root.flags.attention
  aggregate.attention.items = aggregate.attention.items.filter((item) => ids.has(item.delivery_id))
}

function omitAggregateProject(source: WireSnapshot, projectId: number): WireSnapshot {
  const wire = structuredClone(source)
  const aggregate = mutableAggregates(wire)
  const removed = aggregate.projects.find((project) => project.project_id === projectId)!
  subtractCounts(aggregate.root, removed.counts)
  aggregate.projects = aggregate.projects.filter((project) => project.project_id !== projectId)
  wire.rows = (wire.rows ?? []).filter((row) => row.project_id !== projectId)
  aggregate.structural_revision = `${aggregate.structural_revision}:without-project-${projectId}`
  retainAttentionReferences(wire)
  return wire
}

function omitAggregateLane(source: WireSnapshot, projectId: number, laneKey: string): WireSnapshot {
  const wire = structuredClone(source)
  const aggregate = mutableAggregates(wire)
  const project = aggregate.projects.find((candidate) => candidate.project_id === projectId)!
  const removed = project.lanes.find((lane) => lane.lane_key === laneKey)!
  subtractCounts(project.counts, removed.counts)
  subtractCounts(aggregate.root, removed.counts)
  project.lanes = project.lanes.filter((lane) => lane.lane_key !== laneKey)
  wire.rows = (wire.rows ?? []).filter((row) => laneKeyFor(row.project_id!, row.epic_id ?? null) !== laneKey)
  if (project.lanes.length === 0) aggregate.projects = aggregate.projects.filter((candidate) => candidate !== project)
  aggregate.structural_revision = `${aggregate.structural_revision}:without-lane-${laneKey}`
  retainAttentionReferences(wire)
  return wire
}

function makeEligibleRangeSnapshot(aggregated: boolean): WireSnapshot {
  const wire = aggregated ? makeFixtureAggregateSnapshot(10) : makeFixtureSnapshot(10)
  const row = wire.rows![0]
  row.eta = { ...row.eta!, confidence: 'low' }
  wire.selected_delivery = row.delivery_id
  if (aggregated) {
    const aggregate = mutableAggregates(wire)
    const project = aggregate.projects.find((candidate) => candidate.project_id === row.project_id)!
    const laneKey = laneKeyFor(row.project_id!, row.epic_id ?? null)
    const lane = project.lanes.find((candidate) => candidate.lane_key === laneKey)!
    for (const counts of [aggregate.root, project.counts, lane.counts]) {
      counts.landing.within_4h -= 1
      counts.landing.range_only += 1
    }
  }
  return wire
}

async function flush(times = 64) {
  for (let i = 0; i < times; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

interface Harness {
  router: Router
  root: HTMLElement
  unmount: () => void
  loader: ReturnType<typeof vi.fn>
}

async function mountView(loaderImpl: AgentModeSnapshotLoader, path = '/agent-mode'): Promise<Harness> {
  document.body.innerHTML = '<div id="app-header-left"></div><div id="app-header-right"></div><div id="root"></div>'
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().hydrateAccess({ all_projects: true, levels: {} })
  const loader = vi.fn(loaderImpl)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { render: () => h('div') } },
      { path: '/agent-mode', component: { render: () => h(AgentModeView, { loader: loader as unknown as AgentModeSnapshotLoader }) } },
    ],
  })
  await router.push(path)
  await router.isReady()
  const app = createApp({ render: () => h(RouterView) })
  app.use(pinia)
  app.use(router)
  app.use(i18n)
  const root = document.getElementById('root')!
  app.mount(root)
  await flush()
  return { router, root, loader, unmount: () => app.unmount() }
}

/** Publishes a change-stream hint so the view refetches (debounced 750 ms). */
async function refetchViaHint() {
  useChangesStore().publish({ id: 1, mutation_type: 'update', subject_type: 'agent_run', subject_id: 1, project_id: 6, user_id: null, created_at: 'now' })
  await vi.advanceTimersByTimeAsync(1_000)
  await flush()
}

function stubMatchMedia(matching: (query: string) => boolean) {
  const stub = vi.fn((query: string) => ({
    matches: matching(query),
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
  vi.stubGlobal('matchMedia', stub)
  return stub
}

function stubIssueFetch() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
    const raw = input instanceof Request ? input.url : String(input)
    const path = new URL(raw, 'http://paimos.test').pathname
    const match = path.match(/^\/api\/issues\/(\d+)$/)
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
    if (match) {
      const id = Number(match[1])
      return json({
        id,
        project_id: 6,
        issue_key: `PAI-${id}`,
        type: 'ticket',
        title: `Ticket ${id}`,
        description: '',
        acceptance_criteria: '',
        notes: '',
        report_summary: '',
        status: 'in-progress',
        priority: 'medium',
        assignee_id: null,
        assignee: null,
        tags: [],
        sprint_ids: [],
        children: [],
        created_at: '2026-08-20 10:00:00',
        updated_at: '2026-08-20 12:00:00',
      })
    }
    if (/\/api\/issues\/\d+\/activity$/.test(path)) {
      return json({ undo_rows: [], redo_rows: [], history_rows: [], stack_depth: 0 })
    }
    if (/\/api\/issues\/\d+\/ai-activity$/.test(path)) return json({ rows: [], count: 0, last_week_count: 0 })
    if (/\/api\/issues\/\d+\/(attachments|comments|time-entries)$/.test(path)) return json([])
    if (path === '/api/users') return json([{ id: 7, username: 'ada', role: 'member', status: 'active' }])
    if (path === '/api/projects/6/cost-units') return json(['OPS'])
    if (path === '/api/projects/6/releases') return json(['R1'])
    if (path === '/api/tags') return json([])
    if (path === '/api/sprints') return json([])
    if (path === '/api/time-entries/today-summary') return json({ total_hours: 0, count: 0 })
    return json([])
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function selectedCards(root: HTMLElement) {
  return [...root.querySelectorAll<HTMLElement>('[data-selected="true"]')]
}

function selectedId(root: HTMLElement): string | null {
  const cards = selectedCards(root)
  return cards.length === 1 ? cards[0].dataset.deliveryId ?? null : null
}

function cardOrder(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>('.am-lanes :is(.am-card, .am-selected-above)')]
    .map((el) => el.dataset.deliveryId ?? el.dataset.layoutId!)
}

function key(root: HTMLElement, k: string, target?: Element | null) {
  ;(target ?? root.querySelector('.am-canvas')!).dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }))
}

function hit(root: HTMLElement, id: string): HTMLButtonElement {
  return root.querySelector<HTMLButtonElement>(`[data-card-hit="${id}"]`)!
}

const FUTURE_TICKET_COPY = /PAI-80[678]/

describe('AgentModeView (PAI-805 detail 10)', () => {
  let harness: Harness | null = null

  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    harness?.unmount()
    harness = null
    document.body.innerHTML = ''
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('renders truthful lanes with exactly one selected card and the header chrome', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root } = harness
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(9)
    expect(root.querySelectorAll('.am-selected-above')).toHaveLength(1)
    expect(selectedCards(root)).toHaveLength(1)
    // Project headings and the explicit Ungrouped lane.
    const headings = [...root.querySelectorAll('.am-project-head')].map((h) => h.textContent)
    expect(headings.some((t) => t?.includes('PAIMOS Core platform'))).toBe(true)
    expect([...root.querySelectorAll('.am-lane-label h3')].some((h) => h.textContent === 'Ungrouped')).toBe(true)
    // Header teleports (the Agent Mode shell provides these targets).
    expect(document.getElementById('app-header-left')!.textContent).toContain('Agent Mode')
    expect(document.getElementById('app-header-left')!.textContent).toContain('10 deliveries in motion')
    expect(document.getElementById('app-header-right')!.querySelector('.am-lever')).not.toBeNull()
    // Narration is templated from structured state; full column by default.
    const conv = root.querySelector<HTMLElement>('.am-conv')!
    expect(conv.textContent).toContain('10 deliveries are in motion')
    expect(conv.dataset.compact).toBe('false')
    expect(conv.querySelectorAll('.am-conv-line').length).toBeGreaterThan(1)
    // Default pick is the highest-attention delivery (the blocked one) —
    // selected AND needing attention: both meanings stay visible.
    const sel = root.querySelector<HTMLElement>('.am-selection-anchor')!
    expect(sel.textContent).toContain('Blocked')
    expect(sel.textContent).toContain('Selected delivery')
    expect(sel.querySelector('.am-card.is-attention')).not.toBeNull()
    expect(sel.querySelector('.am-card-flag--attention')?.textContent).toContain('Needs you')
    expect(sel.querySelector('.am-card-facts')?.textContent).toContain('Pharos · system')
    expect(sel.querySelector('.am-card-blocker')?.textContent).toContain('permissions fixture fails on case 84')
    // No copy promises future tickets.
    expect(document.body.textContent).not.toMatch(FUTURE_TICKET_COPY)
  })

  it('restores the remembered selection and the deep-linked one', async () => {
    localStorage.setItem(lsAgentModeSelectedKey(undefined), 'dlv-817')
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    expect(selectedId(harness.root)).toBe('dlv-817')
    expect(harness.root.querySelector('.am-conv')!.textContent).toContain('Restored your last selection')
    harness.unmount()

    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)), '/agent-mode?delivery=dlv-820')
    expect(selectedId(harness.root)).toBe('dlv-820')
    expect(harness.router.currentRoute.value.query.delivery).toBe('dlv-820')
  })

  it('recovers stale deep-link and remembered selections with one unselected retry', async () => {
    for (const source of ['deep-link', 'remembered'] as const) {
      const stale = `stale-${source}`
      if (source === 'remembered') localStorage.setItem(lsAgentModeSelectedKey(undefined), stale)
      harness = await mountView(async (query) => {
        if (query.selectedDelivery === stale) {
          throw new AgentModeLoadError('not-found', 'selection revoked', 404)
        }
        return snapshot(makeFixtureSnapshot(10))
      }, source === 'deep-link' ? `/agent-mode?delivery=${stale}` : '/agent-mode')

      expect(harness.loader).toHaveBeenCalledTimes(2)
      expect(harness.loader.mock.calls.map(([query]) => query.selectedDelivery)).toEqual([stale, null])
      expect(harness.root.querySelectorAll('.am-card')).toHaveLength(10)
      expect(selectedCards(harness.root)).toHaveLength(1)
      expect(harness.router.currentRoute.value.query.delivery).not.toBe(stale)
      expect(localStorage.getItem(lsAgentModeSelectedKey(undefined))).not.toBe(stale)

      harness.unmount()
      harness = null
      document.body.innerHTML = ''
      localStorage.clear()
    }
  })

  it('lifts a final-lane selection into exactly one data-backed target before Attention', async () => {
    const wire = makeFixtureSnapshot(10)
    const richSelection = wire.rows!.find((delivery) => delivery.delivery_id === 'dlv-820')!
    richSelection.status_text = 'Verification remains inside the release window'
    harness = await mountView(async () => snapshot(wire), '/agent-mode?delivery=dlv-820')
    const { root } = harness
    const anchor = root.querySelector<HTMLElement>('.am-selection-anchor')!
    const attention = root.querySelector<HTMLElement>('.am-attention')!
    expect(anchor.compareDocumentPosition(attention) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(root.querySelectorAll('[data-selected="true"]')).toHaveLength(1)
    expect(root.querySelectorAll('[aria-current="true"]')).toHaveLength(1)
    expect(anchor.querySelector('[aria-current="true"]')).not.toBeNull()
    expect(anchor.textContent).toContain('REL · Release operations / Ungrouped')
    expect(anchor.textContent).toContain('REL-820')
    expect(anchor.textContent).toContain('Release 5.11.0 smoke suite')
    // The lifted target is the canonical full card, not a lossy summary.
    expect(anchor.querySelector('.am-card-facts')?.textContent).toContain('Codex · agent')
    expect(anchor.querySelector('.am-card-tag')?.textContent).toContain('#security')
    expect(anchor.querySelector('.am-card-percent')?.textContent).toContain('96 % complete')
    expect(anchor.querySelector('.am-card-flag--attention')?.textContent).toContain('Needs you')
    expect(anchor.querySelector('.am-card-reason')?.textContent).toContain('Latency threshold exceeded during verification')
    expect(anchor.querySelector('.am-card-status')?.textContent).toContain('Verification remains inside the release window')
    expect(root.querySelector('.am-lanes [data-delivery-id="dlv-820"]')).toBeNull()
    expect(root.querySelector('.am-lanes [data-layout-id="dlv-820"]')?.textContent).toContain('Selected above')
  })

  it('click selects; activating the selected card opens the data-backed Focused delivery; Escape opens the Portfolio overview', async () => {
    harness = await mountView(async () => snapshot(makeFixtureAggregateSnapshot(10)))
    const { root, router } = harness
    const order = cardOrder(root)
    const target = order.find((id) => id !== selectedId(root))!
    hit(root, target).click()
    await flush()
    expect(selectedId(root)).toBe(target)
    expect(router.currentRoute.value.query.detail).toBeUndefined()

    hit(root, target).click()
    await flush()
    expect(router.currentRoute.value.query.detail).toBe('1')
    const focus = root.querySelector<HTMLElement>('.am-focus')!
    expect(focus).not.toBeNull()
    expect(focus.getAttribute('aria-label')).toBe('Focused delivery')
    expect(focus.textContent).toContain('Focused delivery')
    expect(focus.querySelector('.am-card.is-selected')?.getAttribute('data-delivery-id')).toBe(target)
    // Real delivery detail. The ticket surface remains closed until the
    // separate Open ticket action is explicitly activated.
    expect(focus.querySelector('.am-card-title')!.textContent).not.toBe('')
    expect(focus.querySelector('.am-card-drill')).toBeNull()
    expect(focus.querySelectorAll('input, textarea').length).toBe(0)
    expect(root.querySelector('.side-panel')).toBeNull()
    expect(focus.querySelector('.am-focus-open-ticket')?.getAttribute('aria-expanded')).toBe('false')
    expect(focus.querySelectorAll('.am-stage-chain .am-stage')).toHaveLength(5)
    expect(focus.dataset.deliveryId).toBe(target)
    expect(focus.dataset.attemptId).toBe(`attempt-${target.replace('dlv-', '')}-1`)
    expect(document.body.textContent).not.toMatch(FUTURE_TICKET_COPY)

    // Escape from the focused card returns to the lanes.
    key(root, 'Escape', hit(root, target))
    await flush()
    expect(router.currentRoute.value.query.detail).toBeUndefined()
    expect(root.querySelector('.am-lanes')).not.toBeNull()
    expect(selectedId(root)).toBe(target)

    // Escape from detail 10 opens the schema-v1 Portfolio overview with
    // the selection pinned and server-owned lane counts.
    key(root, 'Escape', hit(root, target))
    await flush()
    expect(router.currentRoute.value.query.detail).toBe('100')
    const streams = root.querySelector<HTMLElement>('.am-streams')!
    expect(streams.getAttribute('aria-label')).toBe('Portfolio overview')
    expect(streams.querySelector('.am-card.is-selected')?.getAttribute('data-delivery-id')).toBe(target)
    expect(streams.querySelector('.am-aggregate-root-total strong')?.textContent).toBe('10')
    expect(streams.querySelectorAll('.am-aggregate-project-control').length).toBeGreaterThan(0)
    expect(streams.querySelectorAll('.am-aggregate-lane-control').length).toBeGreaterThan(0)
    expect(document.body.textContent).not.toMatch(FUTURE_TICKET_COPY)
  })

  it('fails closed at Detail 100 when aggregates are missing or malformed and never derives lane totals from 100 cards', async () => {
    for (const malformed of [false, true]) {
      const wire = malformed ? makeFixtureAggregateSnapshot(100) : makeFixtureSnapshot(100)
      if (malformed) {
        const aggregate = wire.aggregates as { root: { active_total: number } }
        aggregate.root.active_total += 1
      }
      harness = await mountView(async () => snapshot(wire), '/agent-mode?detail=100')
      const { root } = harness
      expect(root.querySelector('.am-streams-unavailable')).not.toBeNull()
      expect(root.querySelector('.am-aggregate-root')).toBeNull()
      expect(root.querySelectorAll('.am-aggregate-project-control')).toHaveLength(0)
      expect(root.querySelectorAll('.am-card')).toHaveLength(1)
      expect(root.querySelector('.am-lanes')).toBeNull()
      expect(root.querySelector('.am-subline')).toBeNull()
      expect(root.textContent).not.toContain('Nothing in motion')
      expect(document.getElementById('app-header-left')?.textContent).not.toContain('nothing in motion')
      expect(root.textContent).toContain('never used to fabricate portfolio totals')
      harness.unmount()
      harness = null
      document.body.innerHTML = ''
    }
  })

  it('keeps Detail 100 bounded for 100 rows: one full card, at most 12 attention rows, and aggregate-only drill targets', async () => {
    harness = await mountView(async () => snapshot(makeFixtureAggregateSnapshot(100)), '/agent-mode?detail=100')
    const { root } = harness
    expect(harness.loader).toHaveBeenCalledTimes(1)
    expect(root.querySelectorAll('.am-card')).toHaveLength(1)
    expect(root.querySelectorAll('.am-attention-item').length).toBeLessThanOrEqual(12)
    expect(root.querySelectorAll('.am-lanes, .am-lane .am-card')).toHaveLength(0)
    expect(root.querySelector('.am-aggregate-root-total strong')?.textContent).toBe('100')
    expect(root.querySelectorAll('.am-aggregate-project-control')).toHaveLength(3)
    expect(root.querySelectorAll('.am-aggregate-lane-control').length).toBeGreaterThan(3)
    expect(selectedCards(root)).toHaveLength(1)
    expect(root.textContent).toContain('Range only')
    expect(root.textContent).toContain('Suppressed or unknown')
  })

  it('presents every project/lane CountSet axis visibly and through stable accessible descriptions on native drill buttons', async () => {
    const wire = makeFixtureAggregateSnapshot(100)
    harness = await mountView(async () => snapshot(wire), '/agent-mode?detail=100')
    const { root, router } = harness
    const project = root.querySelector<HTMLButtonElement>('.am-aggregate-project-control')!
    const projectId = Number(project.closest<HTMLElement>('.am-aggregate-project')!.dataset.projectId)
    const projectWire = mutableAggregates(wire).projects.find((candidate) => candidate.project_id === projectId)!
    const projectDescription = document.getElementById(project.getAttribute('aria-describedby')!)!

    expect(project.tagName).toBe('BUTTON')
    expect(project.type).toBe('button')
    expect(project.getAttribute('role')).toBeNull()
    expect(project.tabIndex).toBe(0)
    expect(project.querySelectorAll('.am-aggregate-project-stages > span')).toHaveLength(6)
    expect(project.querySelectorAll('.am-aggregate-project-flags > span')).toHaveLength(8)
    expect(project.querySelectorAll('.am-aggregate-project-landing > span')).toHaveLength(6)
    for (const key of AGGREGATE_STAGE_KEYS) {
      expect(projectDescription.textContent).toContain(`${i18n.global.t(`agentMode.stage.${key}`)} ${projectWire.counts.current_stage[key]}`)
    }
    for (const key of AGGREGATE_FLAG_KEYS) {
      expect(projectDescription.textContent).toContain(`${i18n.global.t(`agentMode.aggregate.flag.${key}`)} ${projectWire.counts.flags[key]}`)
    }
    for (const key of AGGREGATE_LANDING_KEYS) {
      expect(projectDescription.textContent).toContain(`${i18n.global.t(`agentMode.aggregate.landing.${key}`)} ${projectWire.counts.landing[key]}`)
    }

    const lane = root.querySelector<HTMLButtonElement>('.am-aggregate-lane-control')!
    const laneWire = projectWire.lanes.find((candidate) => candidate.lane_key === lane.dataset.laneKey)!
    const laneDescription = document.getElementById(lane.getAttribute('aria-describedby')!)!
    expect(lane.tagName).toBe('BUTTON')
    expect(lane.type).toBe('button')
    expect(laneDescription.textContent).toContain(`${laneWire.counts.active_total} active`)
    for (const key of AGGREGATE_STAGE_KEYS) {
      expect(laneDescription.textContent).toContain(`${i18n.global.t(`agentMode.stage.${key}`)} ${laneWire.counts.current_stage[key]}`)
    }
    for (const key of AGGREGATE_FLAG_KEYS) {
      expect(laneDescription.textContent).toContain(`${i18n.global.t(`agentMode.aggregate.flag.${key}`)} ${laneWire.counts.flags[key]}`)
    }
    for (const key of AGGREGATE_LANDING_KEYS) {
      expect(laneDescription.textContent).toContain(`${i18n.global.t(`agentMode.aggregate.landing.${key}`)} ${laneWire.counts.landing[key]}`)
    }

    // Native button activation is the only drill path; keyboard agents get
    // the platform Enter/Space contract without a custom role or key trap.
    project.focus()
    expect(document.activeElement).toBe(project)
    project.click()
    await flush()
    expect(router.currentRoute.value.query.detail).toBeUndefined()
    expect(router.currentRoute.value.query.project).toBe(String(projectId))
  })

  it('schedules next_refresh_at from the coherent server clock offset, not the browser wall clock', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-20T09:00:00Z'))
    const wire = makeFixtureAggregateSnapshot(100)
    const aggregate = wire.aggregates as { next_refresh_at: string }
    aggregate.next_refresh_at = '2026-08-20T13:48:05Z'
    harness = await mountView(async () => snapshot(wire), '/agent-mode?detail=100')

    expect(harness.loader).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(4_999)
    await flush()
    expect(harness.loader).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(harness.loader).toHaveBeenCalledTimes(2)
  })

  it('shows the same eligible low-confidence range-only selected card at Detail 1, 10 and 100', async () => {
    for (const detail of [1, 10, 100] as const) {
      const wire = makeEligibleRangeSnapshot(detail === 100)
      const selected = String(wire.selected_delivery)
      const detailQuery = detail === 10 ? '' : `&detail=${detail}`
      harness = await mountView(async () => snapshot(wire), `/agent-mode?delivery=${selected}${detailQuery}`)
      const card = harness.root.querySelector<HTMLElement>(`[data-delivery-id="${selected}"]`)!
      expect(card).not.toBeNull()
      expect(card.querySelector('.am-card-eta--range')?.textContent).toContain('Landing range')
      expect(card.textContent).not.toContain('Lands ~')
      expect(card.querySelector('.am-card-eta small')).toBeNull()
      expect(card.textContent).not.toContain('confidence too low')
      expect(harness.root.querySelector('.am-conv')?.textContent).toContain('Landing range')
      expect(harness.root.querySelector('.am-conv')?.textContent).not.toContain('No estimate')
      if (detail === 1) expect(harness.root.querySelector('.am-detail-list')?.textContent).toContain('Landing range')
      expect(selectedCards(harness.root)).toHaveLength(1)
      harness.unmount()
      harness = null
      document.body.innerHTML = ''
    }
  })

  it('recovers focus from revoked project/lane identities immediately under hold and leaves surviving focus untouched', async () => {
    vi.useFakeTimers()
    const source = makeFixtureAggregateSnapshot(100)
    const aggregate = mutableAggregates(source)
    const removableProject = aggregate.projects.find((project) => project.project_id !== source.rows![0].project_id)!
    const multiLaneProject = aggregate.projects.find((project) => project.lanes.length > 1 && project.project_id !== source.rows![0].project_id)!
    const removableLane = multiLaneProject.lanes[0]
    const cases = [
      {
        selector: `[data-aggregate-focus-key="project:${removableProject.project_id}"]`,
        staleLabel: removableProject.project_name,
        fresh: omitAggregateProject(source, removableProject.project_id),
      },
      {
        selector: `[data-lane-key="${removableLane.lane_key}"]`,
        staleLabel: removableLane.epic_title!,
        fresh: omitAggregateLane(source, multiLaneProject.project_id, removableLane.lane_key),
      },
    ]

    for (const testCase of cases) {
      let fresh = false
      harness = await mountView(async () => snapshot(fresh ? testCase.fresh : source), '/agent-mode?detail=100')
      const target = harness.root.querySelector<HTMLButtonElement>(testCase.selector)!
      const selected = selectedId(harness.root)!
      target.focus()
      await flush()
      expect(document.activeElement).toBe(target)
      expect(harness.root.querySelector('.am-canvas')?.getAttribute('data-held')).toBe('true')

      fresh = true
      await refetchViaHint()
      expect(harness.root.querySelector(testCase.selector)).toBeNull()
      expect(harness.root.textContent).not.toContain(testCase.staleLabel)
      expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(selected)
      harness.unmount()
      harness = null
      document.body.innerHTML = ''
    }

    const surviving = structuredClone(source)
    const survivingAggregate = mutableAggregates(surviving)
    const survivor = survivingAggregate.projects[0]
    const originalName = survivor.project_name
    survivor.project_name = 'Fresh authorized project label'
    for (const row of surviving.rows ?? []) {
      if (row.project_id === survivor.project_id) row.project_name = survivor.project_name
    }
    let refreshed = false
    harness = await mountView(async () => snapshot(refreshed ? surviving : source), '/agent-mode?detail=100')
    const survivorSelector = `[data-aggregate-focus-key="project:${survivor.project_id}"]`
    const survivorButton = harness.root.querySelector<HTMLButtonElement>(survivorSelector)!
    survivorButton.focus()
    refreshed = true
    await refetchViaHint()
    expect(harness.root.textContent).not.toContain(originalName)
    expect(harness.root.querySelector(survivorSelector)?.textContent).toContain('Fresh authorized project label')
    expect((document.activeElement as HTMLElement | null)?.dataset.aggregateFocusKey).toBe(`project:${survivor.project_id}`)
  })

  it('uses selector-independent attention order, removes the pinned duplicate, adjusts hidden count, and selects only on activation', async () => {
    const wire = makeFixtureAggregateSnapshot(100)
    const aggregate = wire.aggregates as {
      attention: { total: number; items: Array<{ delivery_id: string }> }
    }
    const pinned = aggregate.attention.items[0].delivery_id
    wire.selected_delivery = pinned
    harness = await mountView(async () => snapshot(wire), '/agent-mode?detail=100')
    const { root, router } = harness
    expect(selectedId(root)).toBe(pinned)
    expect(root.querySelector(`[data-attention-id="${pinned}"]`)).toBeNull()
    expect(root.querySelectorAll('.am-attention-item')).toHaveLength(11)
    const expectedHidden = aggregate.attention.total - 12
    expect(root.querySelector('.am-attention-more')?.textContent).toContain(String(expectedHidden))

    const offer = root.querySelector<HTMLButtonElement>('.am-attention-select')!
    const offeredId = offer.closest<HTMLElement>('.am-attention-item')!.dataset.attentionId!
    expect(selectedId(root)).toBe(pinned)
    offer.click()
    await flush()
    expect(selectedId(root)).toBe(offeredId)
    expect(router.currentRoute.value.query.detail).toBe('100')
    expect(selectedCards(root)).toHaveLength(1)
  })

  it('drills aggregate controls through immutable server filters, retains delivery, and restores focus to the pinned selection', async () => {
    harness = await mountView(async () => snapshot(makeFixtureAggregateSnapshot(100)), '/agent-mode?detail=100')
    const { root, router } = harness
    const selected = selectedId(root)!
    const lane = root.querySelector<HTMLButtonElement>('.am-aggregate-lane-control')!
    const laneKey = lane.dataset.laneKey!
    const projectId = Number(lane.closest<HTMLElement>('.am-aggregate-project')!.dataset.projectId)
    lane.focus()
    lane.click()
    await flush()

    expect(router.currentRoute.value.query.detail).toBeUndefined()
    expect(router.currentRoute.value.query.project).toBe(String(projectId))
    expect(router.currentRoute.value.query.lane).toBe(laneKey)
    expect(router.currentRoute.value.query.delivery).toBe(selected)
    expect(harness.loader).toHaveBeenCalledTimes(2)
    expect(harness.loader.mock.calls[harness.loader.mock.calls.length - 1]?.[0]).toMatchObject({
      projectId,
      laneKey,
      selectedDelivery: selected,
    })
    expect(selectedId(root)).toBe(selected)
    expect(root.querySelectorAll('[data-selected="true"]')).toHaveLength(1)
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(selected)
  })

  it('opens the reused ticket panel only from Open ticket, then closes with selection intact and focus restored', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'fixture has no issue API' }), {
      status: 404,
      headers: { 'Content-Type': 'application/json' },
    })))
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)), '/agent-mode?detail=1&delivery=dlv-812')
    const { root, router } = harness
    const open = root.querySelector<HTMLButtonElement>('.am-focus-open-ticket')!
    expect(root.querySelector('.side-panel')).toBeNull()
    open.click()
    await flush()

    const panel = root.querySelector<HTMLElement>('.side-panel--embedded')!
    expect(panel).not.toBeNull()
    expect(panel.id).toBe('agent-mode-ticket-panel')
    expect(open.getAttribute('aria-expanded')).toBe('true')
    expect(router.currentRoute.value.query.detail).toBe('1')
    expect(selectedId(root)).toBe('dlv-812')
    expect(root.querySelectorAll('[data-selected="true"]')).toHaveLength(1)

    panel.querySelector<HTMLButtonElement>('[aria-label="Close ticket"]')!.click()
    await flush()
    expect(root.querySelector('.side-panel')).toBeNull()
    expect(selectedId(root)).toBe('dlv-812')
    expect(document.activeElement).toBe(open)
    expect(open.getAttribute('aria-expanded')).toBe('false')
  })

  it('fails closed when the snapshot does not authorize ticket viewing', async () => {
    const wire = makeFixtureSnapshot(10)
    wire.rows![0].capabilities = { ...wire.rows![0].capabilities, view_issue: false }
    harness = await mountView(async () => snapshot(wire), '/agent-mode?detail=1&delivery=dlv-812')
    const open = harness.root.querySelector<HTMLButtonElement>('.am-focus-open-ticket')!
    expect(open.disabled).toBe(true)
    open.click()
    await flush()
    expect(harness.root.querySelector('.side-panel')).toBeNull()
  })

  it('keeps dirty ticket state bound to its delivery when selection is rejected', async () => {
    stubIssueFetch()
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)), '/agent-mode?detail=1&delivery=dlv-812')
    const { root } = harness
    root.querySelector<HTMLButtonElement>('.am-focus-open-ticket')!.click()
    await flush()
    root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    const title = root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Unsaved selection-bound edit'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()

    const selectedBefore = selectedId(root)
    root.querySelectorAll<HTMLButtonElement>('.am-focus-nav button')[1].click()
    await flush()
    expect(useConfirm().visible.value).toBe(true)
    useConfirm().resolve(false)
    await flush()
    expect(selectedId(root)).toBe(selectedBefore)
    expect(root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')?.value).toBe('Unsaved selection-bound edit')
  })

  it('loads scoped editor metadata on panel open so assignee quick-edit works in the real view', async () => {
    const fetchMock = stubIssueFetch()
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)), '/agent-mode?detail=1&delivery=dlv-812')
    const { root } = harness
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/api/users'))).toBe(false)
    root.querySelector<HTMLButtonElement>('.am-focus-open-ticket')!.click()
    await flush()

    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/api/users'))).toBe(true)
    const triggers = root.querySelectorAll<HTMLButtonElement>('.sp-meta .meta-select-trigger')
    expect(triggers).toHaveLength(2)
    triggers[1].click()
    await flush()
    const ada = [...document.querySelectorAll<HTMLButtonElement>('.ms-option')]
      .find((candidate) => candidate.textContent?.includes('ada'))
    expect(ada).not.toBeUndefined()
    ada!.click()
    await flush()
    const assigneePut = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT')
    expect(String(assigneePut?.[0])).toContain('/api/issues/5000')
    expect(assigneePut?.[1]?.headers).toMatchObject({ 'If-Match': '"issue-5000-2026-08-20T12:00:00"' })
    expect(JSON.parse(String(assigneePut?.[1]?.body))).toEqual({ assignee_id: 7 })
  })

  it('passes a deep-linked persistent identity as selected_delivery without inventing a second transport', async () => {
    const wire = makeFixtureSnapshot(10)
    const delivery = wire.rows!.find((row) => row.delivery_id === 'dlv-820')!
    delivery.estimate_suppression_codes = ['source_disagreement']
    delivery.estimate_disagreement_codes = ['runner_vs_plan']
    harness = await mountView(async () => snapshot(wire), '/agent-mode?detail=1&delivery=dlv-820')
    expect(harness.loader.mock.calls[0]?.[0]).toEqual({
      projectId: null,
      laneKey: null,
      health: 'all',
      q: '',
      selectedDelivery: 'dlv-820',
    })
    const focus = harness.root.querySelector<HTMLElement>('.am-focus')!
    expect(focus.dataset.deliveryId).toBe('dlv-820')
    expect(focus.dataset.laneKey).toBe('project:12/ungrouped')
    expect(focus.dataset.stageKey).toBe('deployment')
    expect(focus.dataset.planRevision).toBe('plan:820:1')
    expect(focus.dataset.trustRevision).toBe('trust:820:1')
    expect(focus.querySelectorAll('.am-stage-chain .am-stage')).toHaveLength(5)
    expect(focus.textContent).toContain('Smoke-testing the production release')
    expect(focus.textContent).toContain('Accepted deployment ownership')
    expect(focus.textContent).toContain('specification evidence')
    expect(focus.textContent).toContain('source_disagreement')
    expect(focus.textContent).toContain('runner_vs_plan')
  })

  it('keeps an authorized persistent selection outside active results without adding it to delivery counts', async () => {
    const wire = makeFixtureSnapshot(10)
    const outside = makeFixtureDelivery(20)
    wire.selected_delivery = outside.delivery_id
    wire.selected_outside = { reason: 'terminal', row: outside }
    harness = await mountView(async () => snapshot(wire), `/agent-mode?detail=1&delivery=${outside.delivery_id}`)
    expect(harness.root.querySelector<HTMLElement>('.am-focus')?.dataset.deliveryId).toBe(outside.delivery_id)
    expect(harness.root.querySelectorAll('[data-selected="true"]')).toHaveLength(1)
    expect(harness.root.querySelector('[data-selected-outside-reason="terminal"]')?.textContent).toContain('terminal delivery')
    expect(document.getElementById('app-header-left')!.textContent).toContain('10 deliveries in motion')
  })

  it('arrow keys move the selection along the visual order and carry DOM focus; Enter opens the focused delivery', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root, router } = harness
    const order = cardOrder(root)
    const start = selectedId(root)!
    const startIndex = order.indexOf(start)
    key(root, 'ArrowRight')
    await flush()
    expect(selectedId(root)).toBe(order[Math.min(order.length - 1, startIndex + 1)])
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(selectedId(root))
    key(root, 'ArrowLeft')
    await flush()
    expect(selectedId(root)).toBe(start)
    key(root, 'End')
    await flush()
    expect(selectedId(root)).toBe(order[order.length - 1])
    // Focus follows the selection (roving tabindex).
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(order[order.length - 1])
    key(root, 'Home')
    await flush()
    expect(selectedId(root)).toBe(order[0])
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(order[0])
    // Enter on the focused selected card opens the focused delivery.
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await flush()
    expect(router.currentRoute.value.query.detail).toBe('1')
  })

  it('filters pin an excluded selection above the results and never clear it', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root, router } = harness
    const selected = selectedId(root)!
    const selectedProject = root.querySelector<HTMLElement>('.am-selected-above')!.closest('.am-project')!
    const projectSelect = root.querySelector<HTMLSelectElement>('.am-filter-project select')!
    const otherOption = [...projectSelect.options].find((o) => o.value !== '' && !selectedProject.querySelector(`#am-project-${o.value}`))!
    projectSelect.value = otherOption.value
    projectSelect.dispatchEvent(new Event('change', { bubbles: true }))
    await flush()
    expect(router.currentRoute.value.query.project).toBe(otherOption.value)
    // Lanes only show the filtered project …
    expect(root.querySelectorAll('.am-lanes .am-project')).toHaveLength(1)
    // … while the selection survives, pinned with its reason.
    expect(selectedId(root)).toBe(selected)
    const pinned = root.querySelector<HTMLElement>('.am-selection-anchor')!
    expect(pinned.querySelector<HTMLElement>('.am-card')?.dataset.deliveryId).toBe(selected)
    expect(pinned.textContent).toContain('hidden by the project filter')
    // Clearing restores the full layout and keeps the selection.
    root.querySelector<HTMLButtonElement>('.am-filter-clear')!.click()
    await flush()
    expect(root.querySelector('.am-selection-anchor .am-card-pinned-note')).toBeNull()
    expect(selectedId(root)).toBe(selected)
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(9)
  })

  it('keyboard travel from a pinned (filtered-out) selection into the results and back moves DOM focus with the selection', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root } = harness
    const pinnedId = selectedId(root)!
    const selectedProject = root.querySelector<HTMLElement>('.am-selected-above')!.closest('.am-project')!
    const projectSelect = root.querySelector<HTMLSelectElement>('.am-filter-project select')!
    const otherOption = [...projectSelect.options].find((o) => o.value !== '' && !selectedProject.querySelector(`#am-project-${o.value}`))!
    projectSelect.value = otherOption.value
    projectSelect.dispatchEvent(new Event('change', { bubbles: true }))
    await flush()
    expect(root.querySelector('.am-selection-anchor .am-card-pinned-note')).not.toBeNull()
    hit(root, pinnedId).focus()

    // Into the results: the first lane card is selected AND focused.
    key(root, 'ArrowRight', hit(root, pinnedId))
    await flush()
    const firstResult = cardOrder(root)[0]
    expect(selectedId(root)).toBe(firstResult)
    expect(root.querySelector('.am-selection-anchor .am-card-pinned-note')).toBeNull()
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(firstResult)
    expect(document.activeElement!.closest('.am-selection-anchor')).not.toBeNull()

    // Back: the pinned card is selected again AND focused.
    key(root, 'ArrowLeft', hit(root, firstResult))
    await flush()
    expect(selectedId(root)).toBe(pinnedId)
    expect(root.querySelector('.am-selection-anchor .am-card-pinned-note')).not.toBeNull()
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(pinnedId)
    expect(document.activeElement!.closest('.am-selection-anchor')).not.toBeNull()
  })

  it('attention offers but never steals selection; selecting requires an explicit click', async () => {
    localStorage.setItem(lsAgentModeSelectedKey(undefined), 'dlv-812')
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root } = harness
    expect(selectedId(root)).toBe('dlv-812')
    const strip = root.querySelector('.am-attention')!
    expect(strip).not.toBeNull()
    const items = [...strip.querySelectorAll('.am-attention-item')]
    expect(items.length).toBeGreaterThan(0)
    expect(items.length).toBeLessThanOrEqual(3)
    // Strip never contains the selected delivery and mounting alone did not move selection.
    expect(strip.textContent).not.toContain('PAI-812 ')
    expect(selectedId(root)).toBe('dlv-812')
    strip.querySelector<HTMLButtonElement>('.am-attention-select')!.click()
    await flush()
    expect(selectedId(root)).not.toBe('dlv-812')
    expect(selectedCards(root)).toHaveLength(1)
  })

  it('never reorders cards while the pointer is on the canvas; applies the new order after release', async () => {
    vi.useFakeTimers()
    let phase = 0
    harness = await mountView(async () => {
      const wire = makeFixtureSnapshot(10)
      if (phase === 1) {
        // Give the last card in the first lane top attention: canonically it
        // would jump to the front of its lane.
        const sorted = [...wire.rows!]
        sorted[0] = { ...sorted[0], attention: { level: 3, reason: 'now urgent', since: null } }
        sorted[9] = { ...sorted[9], attention: { level: 0, reason: null, since: null } }
        wire.rows = sorted
        wire.cursor = 'fx-10-2'
      }
      return snapshot(wire)
    })
    const { root } = harness
    const before = cardOrder(root)
    const canvas = root.querySelector<HTMLElement>('.am-canvas')!
    canvas.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false }))
    await flush()
    expect(canvas.dataset.held).toBe('true')

    phase = 1
    await refetchViaHint()
    expect(harness.loader).toHaveBeenCalledTimes(2)
    // Data refreshed, but the layout is frozen under the pointer.
    expect(cardOrder(root)).toEqual(before)
    expect(root.querySelector('[data-delivery-id="dlv-812"]')!.textContent).toContain('now urgent')

    canvas.dispatchEvent(new PointerEvent('pointerleave', { bubbles: false }))
    await vi.advanceTimersByTimeAsync(600)
    await flush()
    expect(canvas.dataset.held).toBe('false')
    const after = cardOrder(root)
    expect(after).not.toEqual(before)
    // dlv-812 and dlv-818 share the PAI-801 lane: before, the sooner trusted
    // landing (818) led; now the urgent one (812) leads.
    expect(before.indexOf('dlv-818')).toBeLessThan(before.indexOf('dlv-812'))
    expect(after.indexOf('dlv-812')).toBeLessThan(after.indexOf('dlv-818'))
  })

  it('moves a live delivery to its current project during hold without a stale project or duplicate card', async () => {
    vi.useFakeTimers()
    let moved = false
    harness = await mountView(async () => {
      const wire = makeFixtureSnapshot(10)
      if (moved) {
        wire.rows = wire.rows!
          .filter((d) => !['dlv-817', 'dlv-820'].includes(String(d.delivery_id)))
          .map((d) => d.delivery_id === 'dlv-814'
            ? {
                ...d,
                project_id: 6,
                project_key: 'PAI',
                project_name: 'PAIMOS Core platform',
                epic_id: 4655,
                epic_key: 'PAI-801',
                epic_title: 'Agent Mode',
              }
            : d)
        wire.cursor = 'fx-project-move'
      }
      return snapshot(wire)
    })
    const { root } = harness
    const canvas = root.querySelector<HTMLElement>('.am-canvas')!
    canvas.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false }))
    moved = true
    await refetchViaHint()

    expect(root.textContent).not.toContain('Release operations')
    const live = root.querySelectorAll<HTMLElement>('[data-delivery-id="dlv-814"]')
    expect(live).toHaveLength(1)
    expect(live[0].closest('.am-project')?.textContent).toContain('PAIMOS Core platform')
    expect(root.querySelectorAll('.am-tombstone')).toHaveLength(0)

    canvas.dispatchEvent(new PointerEvent('pointerleave', { bubbles: false }))
    await vi.advanceTimersByTimeAsync(600)
    await flush()
    expect(root.querySelectorAll('[data-delivery-id="dlv-814"]')).toHaveLength(1)
    expect(root.textContent).not.toContain('Release operations')
  })

  it('moves a live delivery to its current epic, leaves only an opaque old-slot tombstone, then converges on TTL/release', async () => {
    vi.useFakeTimers()
    let moved = false
    harness = await mountView(async () => {
      const wire = makeFixtureSnapshot(10)
      if (moved) {
        wire.rows = wire.rows!.map((d) => d.delivery_id === 'dlv-812'
          ? { ...d, epic_id: 9999, epic_key: 'PAI-999', epic_title: 'Current authorization lane' }
          : d)
        wire.cursor = 'fx-epic-move'
      }
      return snapshot(wire)
    })
    const { root } = harness
    const canvas = root.querySelector<HTMLElement>('.am-canvas')!
    canvas.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false }))
    moved = true
    await refetchViaHint()

    const live = root.querySelectorAll<HTMLElement>('[data-delivery-id="dlv-812"]')
    expect(live).toHaveLength(1)
    expect(live[0].closest('.am-lane')?.textContent).toContain('PAI-999 · Current authorization lane')
    const tombstone = root.querySelector<HTMLElement>('.am-tombstone')!
    expect(tombstone).not.toBeNull()
    expect(tombstone.textContent).toBe('No longer in your active set')
    expect(tombstone.textContent).not.toMatch(/PAI-812|Workspace-level access controls|PAI-801/)

    await vi.advanceTimersByTimeAsync(TOMBSTONE_TTL_MS + 50)
    await flush()
    expect(root.querySelector('.am-tombstone')).toBeNull()
    expect(root.querySelectorAll('[data-delivery-id="dlv-812"]')).toHaveLength(1)

    canvas.dispatchEvent(new PointerEvent('pointerleave', { bubbles: false }))
    await vi.advanceTimersByTimeAsync(600)
    await flush()
    expect(root.querySelectorAll('[data-delivery-id="dlv-812"]')).toHaveLength(1)
    expect(root.querySelector('.am-tombstone')).toBeNull()
  })

  it('clears the snapshot immediately when a refresh returns 403 or 404 after a success', async () => {
    for (const [kind, status, title] of [
      ['forbidden', 403, 'No access'],
      ['not-found', 404, 'not found'],
    ] as const) {
      vi.useFakeTimers()
      let revoke = false
      harness = await mountView(async () => {
        if (revoke) throw new AgentModeLoadError(kind, 'revoked', status)
        return snapshot(makeFixtureSnapshot(10))
      })
      const { root } = harness
      const canvas = root.querySelector<HTMLElement>('.am-canvas')!
      // Even under interaction hold nothing may survive a revocation.
      canvas.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false }))
      await flush()
      expect(root.querySelectorAll('.am-card')).toHaveLength(10)
      expect(root.querySelectorAll('.am-selection-anchor')).toHaveLength(1)
      const seenTitle = root.querySelector('.am-card-title')!.textContent!

      revoke = true
      await refetchViaHint()
      expect(harness.loader).toHaveBeenCalledTimes(status === 404 ? 3 : 2)
      expect(root.querySelectorAll('.am-card')).toHaveLength(0)
      expect(root.querySelectorAll('.am-tombstone')).toHaveLength(0)
      expect(root.querySelector(`.am-state--${kind}`)!.textContent).toContain(title)
      expect(root.textContent).not.toContain(seenTitle)
      expect(root.textContent).not.toContain('PAIMOS Core platform')
      expect(document.getElementById('app-header-left')!.textContent).not.toContain('deliveries in motion')
      expect(root.querySelector('.am-conv')!.textContent).not.toContain('is selected')
      expect(root.querySelector('.am-conv')!.textContent).not.toContain('Nothing is in motion')
      expect(root.querySelector('.am-conv')!.textContent).toContain(title)
      harness.unmount()
      harness = null
      document.body.innerHTML = ''
      vi.useRealTimers()
    }
  })

  it('recovers a selected delivery revoked during refresh without parking or retrying in a loop', async () => {
    vi.useFakeTimers()
    let revoked = false
    harness = await mountView(async (query) => {
      if (revoked && query.selectedDelivery === 'dlv-812') {
        throw new AgentModeLoadError('not-found', 'selection revoked', 404)
      }
      return revoked
        ? snapshotWithout(['dlv-812'], 'fx-selection-revoked')
        : snapshot(makeFixtureSnapshot(10))
    }, '/agent-mode?delivery=dlv-812')
    expect(selectedId(harness.root)).toBe('dlv-812')

    revoked = true
    await refetchViaHint()

    expect(harness.loader).toHaveBeenCalledTimes(3)
    expect(harness.loader.mock.calls.map(([query]) => query.selectedDelivery)).toEqual([
      'dlv-812',
      'dlv-812',
      null,
    ])
    expect(harness.root.querySelector('.am-state--not-found')).toBeNull()
    expect(harness.root.querySelectorAll('.am-card')).toHaveLength(9)
    expect(selectedCards(harness.root)).toHaveLength(1)
    expect(selectedId(harness.root)).not.toBe('dlv-812')
    expect(harness.router.currentRoute.value.query.delivery).not.toBe('dlv-812')
    expect(localStorage.getItem(lsAgentModeSelectedKey(undefined))).not.toBe('dlv-812')

    await vi.advanceTimersByTimeAsync(5_000)
    await flush()
    expect(harness.loader).toHaveBeenCalledTimes(3)
  })

  it('renders a delivery that left a successful snapshot as a neutral tombstone only, drops dead lanes at once, and expires the tombstone', async () => {
    vi.useFakeTimers()
    let phase = 0
    harness = await mountView(async () =>
      phase === 0 ? snapshot(makeFixtureSnapshot(10)) : snapshotWithout(['dlv-813', 'dlv-814', 'dlv-817', 'dlv-820']),
    )
    const { root } = harness
    const canvas = root.querySelector<HTMLElement>('.am-canvas')!
    const before = cardOrder(root)
    expect(before).toContain('dlv-813')
    const goneTitle = root.querySelector('[data-delivery-id="dlv-813"] .am-card-title')!.textContent!
    const goneLane = root.querySelector('[data-delivery-id="dlv-813"]')!.closest<HTMLElement>('.am-lane')!.dataset.laneKey!
    expect(root.textContent).toContain('Release operations')

    canvas.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false }))
    await flush()
    phase = 1
    await refetchViaHint()
    expect(canvas.dataset.held).toBe('true')

    // The gone delivery is neither rendered nor retained: no id, no title.
    expect(root.querySelector('[data-delivery-id="dlv-813"]')).toBeNull()
    expect(root.textContent).not.toContain(goneTitle)
    expect(root.textContent).not.toContain('dlv-813')
    // A neutral tombstone keeps the slot in its lane (a live card remains there) …
    const tombstones = root.querySelectorAll<HTMLElement>('.am-tombstone')
    expect(tombstones).toHaveLength(1)
    expect(tombstones[0].closest<HTMLElement>('.am-lane')!.dataset.laneKey).toBe(goneLane)
    expect(tombstones[0].textContent).toContain('No longer in your active set')
    expect(tombstones[0].textContent).not.toMatch(/PAI-|RUN-|REL-/)
    // … while the project whose deliveries ALL left vanishes immediately,
    // header included, despite the hold.
    expect(root.textContent).not.toContain('Release operations')
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(5)
    // Live cards did not move.
    const live = cardOrder(root)
    expect(live).toEqual(before.filter((id) => live.includes(id)))
    expect(selectedCards(root)).toHaveLength(1)

    // Tombstones are short-lived even under hold.
    await vi.advanceTimersByTimeAsync(TOMBSTONE_TTL_MS + 50)
    await flush()
    expect(canvas.dataset.held).toBe('true')
    expect(root.querySelectorAll('.am-tombstone')).toHaveLength(0)

    canvas.dispatchEvent(new PointerEvent('pointerleave', { bubbles: false }))
    await vi.advanceTimersByTimeAsync(600)
    await flush()
    expect(root.querySelectorAll('.am-tombstone')).toHaveLength(0)
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(5)
  })

  it('keeps exactly one live delivery selected: arrow travel skips ids removed during pointer and keyboard holds', async () => {
    vi.useFakeTimers()
    let removed: string[] = []
    harness = await mountView(async () => (removed.length ? snapshotWithout(removed, `fx-${removed.join('-')}`) : snapshot(makeFixtureSnapshot(10))))
    const { root } = harness
    const canvas = root.querySelector<HTMLElement>('.am-canvas')!
    const order = cardOrder(root)
    // Select the first card so there is always a next one to remove.
    hit(root, order[0]).click()
    await flush()
    expect(selectedId(root)).toBe(order[0])

    // Pointer hold: remove the next card, then ArrowRight.
    canvas.dispatchEvent(new PointerEvent('pointerenter', { bubbles: false }))
    await flush()
    removed = [order[1]]
    await refetchViaHint()
    expect(root.querySelector(`[data-delivery-id="${order[1]}"]`)).toBeNull()
    key(root, 'ArrowRight', hit(root, order[0]))
    await flush()
    expect(selectedCards(root)).toHaveLength(1)
    expect(selectedId(root)).toBe(order[2])
    expect(root.querySelector('.am-conv')!.textContent).toContain('is selected')
    canvas.dispatchEvent(new PointerEvent('pointerleave', { bubbles: false }))
    await vi.advanceTimersByTimeAsync(600)
    await flush()

    // Keyboard hold: focus inside the canvas, remove the next card, ArrowRight.
    const current = selectedId(root)!
    const liveOrder = cardOrder(root)
    const idx = liveOrder.indexOf(current)
    hit(root, current).focus()
    canvas.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    await flush()
    expect(canvas.dataset.held).toBe('true')
    removed = [order[1], liveOrder[idx + 1]]
    await refetchViaHint()
    expect(root.querySelector(`[data-delivery-id="${liveOrder[idx + 1]}"]`)).toBeNull()
    key(root, 'ArrowRight', hit(root, current))
    await flush()
    expect(selectedCards(root)).toHaveLength(1)
    expect(selectedId(root)).toBe(liveOrder[idx + 2])
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(liveOrder[idx + 2])
    // End / Home also land on live ids only.
    key(root, 'End', hit(root, liveOrder[idx + 2]))
    await flush()
    const lastLive = cardOrder(root)[cardOrder(root).length - 1]
    expect(selectedId(root)).toBe(lastLive)
  })

  it('canonicalizes invalid URL project/lane filters to null without mutating an identity into a request', async () => {
    const invalidPaths = [
      '/agent-mode?project=6.5',
      `/agent-mode?project=${Number.MAX_SAFE_INTEGER + 1}`,
      '/agent-mode?lane=arbitrary-lane',
      `/agent-mode?lane=${encodeURIComponent(`project:6/epic:${'1'.repeat(220)}`)}`,
    ]
    for (const path of invalidPaths) {
      harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)), path)
      expect(harness.loader.mock.calls[0]?.[0]).toMatchObject({ projectId: null, laneKey: null })
      expect(harness.root.querySelectorAll('.am-card')).toHaveLength(10)
      harness.unmount()
      harness = null
      document.body.innerHTML = ''
    }
  })

  it('scopes delivery keys to the canvas and cards: the health radiogroup and other controls keep their own behaviour', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root, router } = harness
    const selected = selectedId(root)!
    const group = root.querySelector<HTMLElement>('[role="radiogroup"]')!
    expect(group.getAttribute('aria-label')).toBe('Health')
    const radios = [...group.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
    expect(radios.map((r) => r.dataset.health)).toEqual(['all', 'attention', 'blocked', 'stale'])
    expect(radios.map((r) => r.getAttribute('aria-checked'))).toEqual(['true', 'false', 'false', 'false'])
    expect(radios.map((r) => r.tabIndex)).toEqual([0, -1, -1, -1])

    // Arrow on the radiogroup moves the checked option + focus, not the delivery.
    radios[0].focus()
    key(root, 'ArrowRight', radios[0])
    await flush()
    expect(router.currentRoute.value.query.health).toBe('attention')
    expect(group.querySelector('[aria-checked="true"]')?.getAttribute('data-health')).toBe('attention')
    expect((document.activeElement as HTMLElement | null)?.dataset.health).toBe('attention')
    expect(selectedId(root)).toBe(selected)
    key(root, 'End', document.activeElement)
    await flush()
    expect(router.currentRoute.value.query.health).toBe('stale')
    key(root, 'ArrowRight', document.activeElement) // wraps
    await flush()
    expect(router.currentRoute.value.query.health).toBeUndefined()
    key(root, 'ArrowLeft', document.activeElement) // wraps back to the end
    await flush()
    expect(router.currentRoute.value.query.health).toBe('stale')
    key(root, 'Home', document.activeElement)
    await flush()
    expect(router.currentRoute.value.query.health).toBeUndefined()
    expect(selectedId(root)).toBe(selected)
    expect(router.currentRoute.value.query.detail).toBeUndefined()

    // Inputs, selects, buttons and links keep their native keys.
    const search = root.querySelector<HTMLInputElement>('.am-filter-query input')!
    for (const k of ['ArrowRight', 'ArrowLeft', 'Home', 'End', 'Escape']) key(root, k, search)
    await flush()
    const select = root.querySelector<HTMLSelectElement>('.am-filter-project select')!
    for (const k of ['ArrowDown', 'ArrowUp', 'Escape']) key(root, k, select)
    await flush()
    const offer = root.querySelector<HTMLButtonElement>('.am-attention-select')!
    for (const k of ['ArrowRight', 'End', 'Escape']) key(root, k, offer)
    await flush()
    expect(selectedId(root)).toBe(selected)
    expect(router.currentRoute.value.query.detail).toBeUndefined()

    // Keys on a card DO navigate deliveries (and leave the health filter alone).
    key(root, 'ArrowRight', hit(root, selected))
    await flush()
    expect(selectedId(root)).not.toBe(selected)
    expect(router.currentRoute.value.query.health).toBeUndefined()
  })

  it('qualifies retained data as last-known while offline and withholds exact estimates', async () => {
    vi.useFakeTimers()
    let offline = false
    harness = await mountView(async () => {
      if (offline) throw new AgentModeLoadError('offline', 'down', 0)
      return snapshot(makeFixtureSnapshot(10))
    })
    const { root } = harness
    expect(root.querySelectorAll('.am-card-percent').length).toBeGreaterThan(0)
    expect([...root.querySelectorAll('.am-card-eta')].some((el) => el.textContent?.includes('Lands ~'))).toBe(true)

    offline = true
    await refetchViaHint()
    // Data is retained — visibly qualified.
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(9)
    expect(root.querySelectorAll('.am-card.is-degraded')).toHaveLength(10)
    expect(root.querySelector('.am-selection-anchor .am-card.is-degraded')).not.toBeNull()
    expect(root.querySelectorAll('.am-card-retained').length).toBe(10)
    expect(root.querySelector('.am-banner')!.textContent).toContain('Last known state')
    // … and no false precision: no percent, no landing time, reason named.
    expect(root.querySelectorAll('.am-card-percent')).toHaveLength(0)
    expect(root.querySelectorAll('.am-card-progress')).toHaveLength(0)
    expect([...root.querySelectorAll('.am-card-eta')].some((el) => el.textContent?.includes('Lands ~'))).toBe(false)
    expect(root.querySelectorAll('.am-card-eta--withheld').length).toBe(10)
    expect(root.textContent).toContain('No estimate — feed offline')
    expect(root.querySelector('.am-conv')!.textContent).toContain('feed offline')
    expect(root.querySelector('.am-conv')!.textContent).not.toContain('Lands about')
    expect(document.getElementById('app-header-right')!.textContent).toContain('Offline')
  })

  it('collapses the conversation into a compact, non-occluding lower-left dock at constrained widths', async () => {
    stubMatchMedia((q) => q === COMPACT_CONVERSATION_QUERY)
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root } = harness
    expect(root.querySelector('.am-root')!.classList.contains('am-root--compact')).toBe(true)
    const conv = root.querySelector<HTMLElement>('.am-conv')!
    expect(conv.dataset.compact).toBe('true')
    expect(conv.classList.contains('am-conv--compact')).toBe(true)
    // At most three recent lines + listening state survive (no column head).
    expect(conv.querySelectorAll('.am-conv-line')).toHaveLength(3)
    expect(conv.querySelector('.am-conv-dock')).not.toBeNull()
    expect(conv.querySelector('.am-conv-compact-live')?.textContent).toContain('Listening')
    expect(conv.querySelector('.am-conv-head')).toBeNull()
    // The lane canvas is not starved: all cards still render.
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(9)
    expect(root.querySelectorAll('.am-selected-above')).toHaveLength(1)
    expect(selectedCards(root)).toHaveLength(1)
    // The conversation is a surface, not navigation: no links or app chrome.
    expect(conv.querySelectorAll('a, nav').length).toBe(0)
  })

  it('renders honest loading, offline, forbidden, not-found and empty states without fabricating data', async () => {
    let gate: (s: AgentModeSnapshot) => void = () => {}
    harness = await mountView(() => new Promise<AgentModeSnapshot>((resolve) => { gate = resolve }))
    expect(harness.root.querySelector('.am-state--loading')).not.toBeNull()
    expect(harness.root.querySelectorAll('.am-card')).toHaveLength(0)
    gate(snapshot({ schema_version: 1, server_time: '2026-08-20T13:48:00Z', rows: [] }))
    await flush()
    expect(harness.root.querySelector('.am-state--empty')!.textContent).toContain('Nothing in motion')
    harness.unmount()

    harness = await mountView(async () => { throw new AgentModeLoadError('offline', 'down', 0) })
    expect(harness.root.querySelector('.am-state--offline')!.textContent).toContain('Offline — retrying')
    expect(harness.root.querySelectorAll('.am-card')).toHaveLength(0)
    expect(document.getElementById('app-header-right')!.textContent).toContain('Offline')
    harness.unmount()

    harness = await mountView(async () => { throw new AgentModeLoadError('forbidden', 'nope', 403) })
    expect(harness.root.querySelector('.am-state--forbidden')!.textContent).toContain('No access')
    harness.unmount()

    harness = await mountView(async () => { throw new AgentModeLoadError('not-found', 'missing', 404) })
    expect(harness.root.querySelector('.am-state--not-found')!.textContent).toContain('not found')
    harness.unmount()

    harness = await mountView(async () => { throw new AgentModeLoadError('error', 'boom', 500) })
    expect(harness.root.querySelector('.am-state--error')!.textContent).toContain('Could not load')
    expect(harness.root.querySelector('.am-state--error')!.textContent).toContain('boom')
  })

  it('scales to 100 deliveries with one selection and consistent lane totals', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(100)))
    const { root } = harness
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(99)
    expect(selectedCards(root)).toHaveLength(1)
    const laneCounts = [...root.querySelectorAll('.am-lane')].map((lane) => lane.querySelectorAll('.am-card, .am-selected-above').length)
    expect(laneCounts.reduce((a, b) => a + b, 0)).toBe(100)
    expect(document.getElementById('app-header-left')!.textContent).toContain('100 deliveries in motion')
  })
})
