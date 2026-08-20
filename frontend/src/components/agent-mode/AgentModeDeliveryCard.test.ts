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

import { mountComponent } from '@/components/ai/testMount'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { Delivery } from '@/services/agentMode'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'

const ten = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries
const SERVER_NOW = Date.parse('2026-08-20T13:48:00Z')

function card(d: Delivery, extra: Record<string, unknown> = {}) {
  setActivePinia(createPinia())
  return mountComponent(AgentModeDeliveryCard, {
    delivery: d,
    selected: false,
    tabbable: false,
    serverNowMs: SERVER_NOW,
    locale: 'en-US',
    ...extra,
  })
}

describe('AgentModeDeliveryCard (PAI-805)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('renders identity, actor, activity, stage, health and freshness without animation dependencies', async () => {
    const d = ten[0]
    const m = await card(d)
    const text = m.el.textContent ?? ''
    expect(text).toContain(d.issueKey)
    expect(text).toContain(d.title)
    expect(text).toContain('Codex')
    expect(text).toContain('Writing membership checks')
    expect(text).toContain('Specification')
    expect(text).toContain('Healthy')
    expect(m.el.querySelector('.am-glyph--working')).not.toBeNull()
    expect(m.el.querySelector('.am-card-hit')?.getAttribute('aria-current')).toBeNull()
    expect(m.el.querySelector('.am-card-flag--selected')).toBeNull()
    await m.unmount()
  })

  it('shows percent and landing time only when trusted, and names the reason otherwise', async () => {
    const trusted = ten[0]
    const m1 = await card(trusted)
    expect(m1.el.querySelector('.am-card-percent')?.textContent).toContain('64 %')
    expect(m1.el.querySelector('.am-card-eta')?.textContent).toContain('Lands ~')
    await m1.unmount()

    const waiting = ten[2]
    const m2 = await card(waiting)
    expect(m2.el.querySelector('.am-card-percent')).toBeNull()
    expect(m2.el.querySelector('.am-card-progress')).toBeNull()
    expect(m2.el.textContent).toContain('No estimate — waiting for input')
    await m2.unmount()

    const blocked = ten[3]
    const m3 = await card(blocked)
    expect(m3.el.querySelector('.am-card-percent')).toBeNull()
    expect(m3.el.textContent).toContain('No estimate — blocked')
    expect(m3.el.textContent).toContain('permissions fixture fails')
    await m3.unmount()

    const untrusted = ten[5]
    const m4 = await card(untrusted)
    expect(m4.el.querySelector('.am-card-percent')).toBeNull()
    expect(m4.el.textContent).toContain('No estimate — not trusted yet')
    await m4.unmount()

    const stale = ten[7]
    const m5 = await card(stale)
    expect(m5.el.querySelector('.am-card-percent')).toBeNull()
    expect(m5.el.textContent).toContain('No estimate — report is stale')
    expect(m5.el.querySelector('.am-card.is-stale')).not.toBeNull()
    await m5.unmount()
  })

  it('marks selection, attention and pinning distinctly', async () => {
    const m = await card(ten[2], { selected: true, tabbable: true, pinnedReason: 'project' })
    const root = m.el.querySelector('.am-card')!
    expect(root.classList.contains('is-selected')).toBe(true)
    expect(root.classList.contains('is-attention')).toBe(true)
    expect(m.el.querySelector('.am-card-flag--selected')?.textContent).toContain('Selected')
    expect(m.el.querySelector('.am-card-hit')?.getAttribute('aria-current')).toBe('true')
    expect(m.el.querySelector('.am-card-hit')?.getAttribute('tabindex')).toBe('0')
    expect(m.el.querySelector('.am-card-pinned-note')?.textContent).toContain('hidden by the project filter')
    expect(m.el.querySelector('.am-card-drill')).not.toBeNull()
    await m.unmount()

    const m2 = await card(ten[2])
    expect(m2.el.querySelector('.am-card-flag--attention')?.textContent).toContain('Needs you')
    expect(m2.el.querySelector('.am-card-drill')).toBeNull()
    await m2.unmount()
  })

  it('click selects when unselected and activates when already selected; Enter mirrors that', async () => {
    const onSelect = vi.fn()
    const onActivate = vi.fn()
    const m = await card(ten[1], { onSelect, onActivate })
    const hit = m.el.querySelector<HTMLButtonElement>('.am-card-hit')!
    hit.click()
    expect(onSelect).toHaveBeenCalledWith(ten[1].id)
    expect(onActivate).not.toHaveBeenCalled()
    hit.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onSelect).toHaveBeenCalledTimes(2)
    await m.unmount()

    const onSelect2 = vi.fn()
    const onActivate2 = vi.fn()
    const m2 = await card(ten[1], { selected: true, onSelect: onSelect2, onActivate: onActivate2 })
    const hit2 = m2.el.querySelector<HTMLButtonElement>('.am-card-hit')!
    hit2.click()
    expect(onActivate2).toHaveBeenCalledWith(ten[1].id)
    hit2.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onActivate2).toHaveBeenCalledTimes(2)
    expect(onSelect2).not.toHaveBeenCalled()
    await m2.unmount()
  })
})
