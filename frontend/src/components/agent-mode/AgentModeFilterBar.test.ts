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

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'

import { mountComponent } from '@/components/ai/testMount'
import { EMPTY_FILTERS, type AgentModeFilters } from '@/composables/agent-mode/agentModeFilters'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import AgentModeFilterBar from './AgentModeFilterBar.vue'

const fixture = normalizeWireSnapshot(makeFixtureSnapshot(10), 0)

async function bar(filters: AgentModeFilters, onUpdate = vi.fn(), extra: Record<string, unknown> = {}) {
  setActivePinia(createPinia())
  const m = await mountComponent(AgentModeFilterBar, { filters, aggregates: fixture.aggregates, 'onUpdate:filters': onUpdate, ...extra })
  return { ...m, onUpdate }
}

function press(el: Element, key: string) {
  const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
  el.dispatchEvent(event)
  return event
}

describe('AgentModeFilterBar (PAI-805) — health radiogroup', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('exposes a radiogroup with one tab stop and aria-checked state', async () => {
    const m = await bar({ ...EMPTY_FILTERS, health: 'blocked' })
    const group = m.el.querySelector<HTMLElement>('[role="radiogroup"]')!
    expect(group.getAttribute('aria-label')).toBe('Health')
    const radios = [...group.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
    expect(radios).toHaveLength(4)
    expect(radios.map((r) => r.getAttribute('aria-checked'))).toEqual(['false', 'false', 'true', 'false'])
    expect(radios.map((r) => r.tabIndex)).toEqual([-1, -1, 0, -1])
    await m.unmount()
  })

  it('moves the checked option with arrows (wrapping) and Home / End, moves focus, and consumes the key', async () => {
    const m = await bar(EMPTY_FILTERS)
    const outer = vi.fn()
    document.addEventListener('keydown', outer)
    const radios = [...m.el.querySelectorAll<HTMLButtonElement>('[role="radio"]')]

    let e = press(radios[0], 'ArrowRight')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'attention' })
    expect(e.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(radios[1])

    // Wraps from the first backwards to the last …
    e = press(radios[0], 'ArrowLeft')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'stale' })
    expect(document.activeElement).toBe(radios[3])
    // … and Down / Up behave like Right / Left.
    press(radios[0], 'ArrowDown')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'attention' })
    press(radios[0], 'ArrowUp')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'stale' })
    press(radios[0], 'End')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'stale' })
    press(radios[0], 'Home')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'all' })

    // The group owns these keys: nothing above it (e.g. the delivery
    // navigation handler) sees them …
    expect(outer).not.toHaveBeenCalled()
    // … while unrelated keys still bubble normally.
    const other = press(radios[0], 'a')
    expect(other.defaultPrevented).toBe(false)
    expect(outer).toHaveBeenCalledTimes(1)
    document.removeEventListener('keydown', outer)
    await m.unmount()
  })

  it('wraps forward from the last option to the first', async () => {
    const m = await bar({ ...EMPTY_FILTERS, health: 'stale' })
    const radios = [...m.el.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
    press(radios[3], 'ArrowRight')
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'all' })
    expect(document.activeElement).toBe(radios[0])
    await m.unmount()
  })

  it('click still selects an option and filters never touch anything but the filter model', async () => {
    const m = await bar(EMPTY_FILTERS)
    m.el.querySelector<HTMLButtonElement>('[data-health="blocked"]')!.click()
    expect(m.onUpdate).toHaveBeenLastCalledWith({ health: 'blocked' })
    await m.unmount()
  })

  it('shows Clear only when the canonical server query is active', async () => {
    const empty = await bar({ ...EMPTY_FILTERS, query: '\u0085' })
    expect(empty.el.querySelector('.am-filter-clear')).toBeNull()
    await empty.unmount()

    const active = await bar({ ...EMPTY_FILTERS, query: '\uFEFF' })
    expect(active.el.querySelector('.am-filter-clear')).not.toBeNull()
    await active.unmount()
  })

  it('switches from the shared authorized catalog, including a zero-active project absent from aggregates', async () => {
    const m = await bar(EMPTY_FILTERS, vi.fn(), {
      projectCatalog: [
        { projectId: 12, projectKey: 'PAI', projectName: 'Paimos' },
        { projectId: 91, projectKey: 'VAC', projectName: 'Vacation' },
      ],
      projectActiveTotals: new Map([[12, 7], [91, 0]]),
    })
    m.el.querySelector<HTMLButtonElement>('.am-project-picker__trigger')!.click()
    await nextTick()
    const options = [...m.el.querySelectorAll<HTMLButtonElement>('[role="option"]')]
    expect(options.map((option) => option.textContent?.replace(/\s+/g, ' ').trim())).toEqual([
      'All projects',
      'PAI · Paimos7 active',
      'VAC · VacationNo active deliveries',
    ])
    options[2].click()
    expect(m.onUpdate).toHaveBeenLastCalledWith({ projectId: 91, laneKey: null })
    await m.unmount()
  })

  it('offers a persisted-presentation density choice separately from filters', async () => {
    const onDensity = vi.fn()
    const m = await bar(EMPTY_FILTERS, vi.fn(), { density: 'calm', 'onUpdate:density': onDensity })
    const group = m.el.querySelector<HTMLElement>('.am-density')!
    const options = [...group.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
    expect(options.map((option) => option.getAttribute('aria-checked'))).toEqual(['true', 'false'])
    options[1].click()
    expect(onDensity).toHaveBeenCalledWith('full')
    await m.unmount()
  })
})
