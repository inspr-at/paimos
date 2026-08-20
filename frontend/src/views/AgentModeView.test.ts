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
import { AgentModeLoadError, type AgentModeSnapshot, type AgentModeSnapshotLoader } from '@/services/agentMode'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot, type WireSnapshot } from '@/services/agentModeTransport'
import { useChangesStore } from '@/stores/changes'
import { lsAgentModeSelectedKey } from '@/constants/storage'
import AgentModeView from './AgentModeView.vue'

function snapshot(wire: WireSnapshot): AgentModeSnapshot {
  return normalizeWireSnapshot(wire, Date.now())
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

function selectedCards(root: HTMLElement) {
  return [...root.querySelectorAll<HTMLElement>('.am-card.is-selected')]
}

function selectedId(root: HTMLElement): string | null {
  const cards = selectedCards(root)
  return cards.length === 1 ? cards[0].dataset.deliveryId ?? null : null
}

function cardOrder(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>('.am-lanes .am-card')].map((el) => el.dataset.deliveryId!)
}

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
  })

  it('renders truthful lanes with exactly one selected card and the header chrome', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root } = harness
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(10)
    expect(selectedCards(root)).toHaveLength(1)
    // Project headings and the explicit Ungrouped lane.
    const headings = [...root.querySelectorAll('.am-project-head')].map((h) => h.textContent)
    expect(headings.some((t) => t?.includes('PAIMOS Core platform'))).toBe(true)
    expect([...root.querySelectorAll('.am-lane-label h3')].some((h) => h.textContent === 'Ungrouped')).toBe(true)
    // Header teleports.
    expect(document.getElementById('app-header-left')!.textContent).toContain('Agent Mode')
    expect(document.getElementById('app-header-left')!.textContent).toContain('10 deliveries in motion')
    expect(document.getElementById('app-header-right')!.querySelector('.am-lever')).not.toBeNull()
    // Narration is templated from structured state.
    expect(root.querySelector('.am-conv')!.textContent).toContain('10 deliveries are in motion')
    // Default pick is the highest-attention delivery (the blocked one).
    const sel = root.querySelector<HTMLElement>('.am-card.is-selected')!
    expect(sel.textContent).toContain('Blocked')
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

  it('click selects, activating the selected card drills to detail 1, Escape zooms out', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root, router } = harness
    const order = cardOrder(root)
    const target = order.find((id) => id !== selectedId(root))!
    root.querySelector<HTMLButtonElement>(`[data-card-hit="${target}"]`)!.click()
    await flush()
    expect(selectedId(root)).toBe(target)
    expect(router.currentRoute.value.query.detail).toBeUndefined()

    root.querySelector<HTMLButtonElement>(`[data-card-hit="${target}"]`)!.click()
    await flush()
    expect(router.currentRoute.value.query.detail).toBe('1')
    expect(root.querySelector('.am-focus')).not.toBeNull()
    expect(root.querySelector('.am-focus .am-card.is-selected')?.getAttribute('data-delivery-id')).toBe(target)

    root.querySelector<HTMLElement>('.am-root')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flush()
    expect(router.currentRoute.value.query.detail).toBeUndefined()
    expect(root.querySelector('.am-lanes')).not.toBeNull()
    expect(selectedId(root)).toBe(target)

    // Escape from 10 zooms out to 100 (seam keeps the selection pinned).
    root.querySelector<HTMLElement>('.am-root')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flush()
    expect(router.currentRoute.value.query.detail).toBe('100')
    expect(root.querySelector('.am-streams .am-card.is-selected')?.getAttribute('data-delivery-id')).toBe(target)
  })

  it('arrow keys move the selection along the visual order; Enter drills', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root, router } = harness
    const order = cardOrder(root)
    const start = selectedId(root)!
    const startIndex = order.indexOf(start)
    const canvas = root.querySelector<HTMLElement>('.am-root')!
    canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    await flush()
    expect(selectedId(root)).toBe(order[Math.min(order.length - 1, startIndex + 1)])
    canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }))
    await flush()
    expect(selectedId(root)).toBe(start)
    canvas.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    await flush()
    expect(selectedId(root)).toBe(order[order.length - 1])
    // Focus follows the selection (roving tabindex).
    expect((document.activeElement as HTMLElement | null)?.dataset.cardHit).toBe(order[order.length - 1])
    // Enter on the focused selected card drills.
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await flush()
    expect(router.currentRoute.value.query.detail).toBe('1')
  })

  it('filters pin an excluded selection above the results and never clear it', async () => {
    harness = await mountView(async () => snapshot(makeFixtureSnapshot(10)))
    const { root, router } = harness
    const selected = selectedId(root)!
    const selectedProject = root.querySelector<HTMLElement>(`.am-card.is-selected`)!.closest('.am-project')!
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
    const pinned = root.querySelector<HTMLElement>('.am-pinned .am-card.is-selected')!
    expect(pinned.dataset.deliveryId).toBe(selected)
    expect(pinned.textContent).toContain('hidden by the project filter')
    // Clearing restores the full layout and keeps the selection.
    root.querySelector<HTMLButtonElement>('.am-filter-clear')!.click()
    await flush()
    expect(root.querySelector('.am-pinned')).toBeNull()
    expect(selectedId(root)).toBe(selected)
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(10)
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
        const sorted = [...wire.deliveries!]
        sorted[0] = { ...sorted[0], attention: { level: 3, reason: 'now urgent', since: null } }
        sorted[9] = { ...sorted[9], attention: { level: 0, reason: null, since: null } }
        wire.deliveries = sorted
        wire.revision = 'fx-10-2'
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
    useChangesStore().publish({ id: 1, mutation_type: 'update', subject_type: 'agent_run', subject_id: 1, project_id: 6, user_id: null, created_at: 'now' })
    await vi.advanceTimersByTimeAsync(1_000)
    await flush()
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

  it('renders honest loading, offline, forbidden, not-found and empty states without fabricating data', async () => {
    let gate: (s: AgentModeSnapshot) => void = () => {}
    harness = await mountView(() => new Promise<AgentModeSnapshot>((resolve) => { gate = resolve }))
    expect(harness.root.querySelector('.am-state--loading')).not.toBeNull()
    expect(harness.root.querySelectorAll('.am-card')).toHaveLength(0)
    gate(snapshot({ server_time: '2026-08-20T13:48:00Z', deliveries: [] }))
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
    expect(root.querySelectorAll('.am-lanes .am-card')).toHaveLength(100)
    expect(selectedCards(root)).toHaveLength(1)
    const laneCounts = [...root.querySelectorAll('.am-lane')].map((lane) => lane.querySelectorAll('.am-card').length)
    expect(laneCounts.reduce((a, b) => a + b, 0)).toBe(100)
    expect(document.getElementById('app-header-left')!.textContent).toContain('100 deliveries in motion')
  })
})
