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
import { MAX_VISIBLE_TAGS } from './agentModePresentation'
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

/** Effective accessible name of the hit area per accname: no aria-label /
 * labelledby is used, so the name is the text of the button's subtree
 * minus aria-hidden branches, whitespace-normalised. */
function textOf(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent ?? ''
  if (node.nodeType !== Node.ELEMENT_NODE) return ''
  const el = node as Element
  if (el.getAttribute('aria-hidden') === 'true') return ''
  return [...el.childNodes].map(textOf).join(' ')
}
function accessibleName(el: HTMLElement): string {
  const hit = el.querySelector<HTMLButtonElement>('.am-card-hit')!
  expect(hit.getAttribute('aria-label')).toBeNull()
  expect(hit.getAttribute('aria-labelledby')).toBeNull()
  return textOf(hit).replace(/\s+/g, ' ').trim()
}

function count(hay: string, needle: string): number {
  return hay.split(needle).length - 1
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
    // The visible percent text carries the value; the bar itself is decoration.
    expect(m1.el.querySelector('.am-card-progress')?.getAttribute('aria-hidden')).toBe('true')
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

  it('withholds exact estimates and qualifies the card when the data is retained offline', async () => {
    const m = await card(ten[0], { degraded: true })
    expect(m.el.querySelector('.am-card.is-degraded')).not.toBeNull()
    expect(m.el.querySelector('.am-card-percent')).toBeNull()
    expect(m.el.querySelector('.am-card-progress')).toBeNull()
    expect(m.el.textContent).not.toContain('Lands ~')
    expect(m.el.textContent).toContain('No estimate — feed offline')
    expect(m.el.querySelector('.am-card-retained')?.textContent).toContain('Last known state')
    // The qualifier is part of the accessible name.
    expect(accessibleName(m.el)).toContain('Last known state · feed offline')
    await m.unmount()
  })

  it('marks selection, attention and pinning distinctly — and shows Selected + Needs you together', async () => {
    const m = await card(ten[2], { selected: true, tabbable: true, pinnedReason: 'project' })
    const root = m.el.querySelector('.am-card')!
    expect(root.classList.contains('is-selected')).toBe(true)
    expect(root.classList.contains('is-attention')).toBe(true)
    expect(m.el.querySelector('.am-card-flag--selected')?.textContent).toContain('Selected')
    expect(m.el.querySelector('.am-card-flag--attention')?.textContent).toContain('Needs you')
    expect(m.el.querySelector('.am-card-hit')?.getAttribute('aria-current')).toBe('true')
    expect(m.el.querySelector('.am-card-hit')?.getAttribute('tabindex')).toBe('0')
    expect(m.el.querySelector('.am-card-pinned-note')?.textContent).toContain('hidden by the project filter')
    expect(m.el.querySelector('.am-card-drill')).not.toBeNull()
    await m.unmount()

    const m2 = await card(ten[2])
    expect(m2.el.querySelector('.am-card-flag--attention')?.textContent).toContain('Needs you')
    expect(m2.el.querySelector('.am-card-flag--selected')).toBeNull()
    expect(m2.el.querySelector('.am-card-drill')).toBeNull()
    await m2.unmount()
  })

  it('exposes one ordered accessible name: state, key/title, health, activity, stage, freshness, blocker, estimate — nothing twice', async () => {
    const blocked = ten[3] // attention 3, blocked, blocker text, no estimate
    const m = await card(blocked, { selected: true, tabbable: true })
    const name = accessibleName(m.el)
    // State flags first, exactly once (the visible flags are aria-hidden).
    expect(name.startsWith('Selected. Needs you.')).toBe(true)
    expect(count(name, 'Selected')).toBe(1)
    expect(count(name, 'Needs you')).toBe(1)
    expect(m.el.querySelector('.am-card-flags')?.getAttribute('aria-hidden')).toBe('true')
    // Then identity, health, activity, stage, freshness, blocker, estimate reason — in order.
    const order = [
      blocked.issueKey,
      blocked.title,
      'Blocked', // health word
      'Investigating 3 failed assertions', // activity
      'Deployment · 4/5', // stage
      'Reported', // freshness
      'Blocked: permissions fixture fails on case 84', // blocker
      'No estimate — blocked', // withholding reason
    ]
    let cursor = -1
    for (const part of order) {
      const at = name.indexOf(part, cursor + 1)
      expect(at, `expected "${part}" after position ${cursor} in "${name}"`).toBeGreaterThan(cursor)
      cursor = at
    }
    // The decorative actor initials and the drill button are not part of the name.
    expect(m.el.querySelector('.am-card-actor')?.getAttribute('aria-hidden')).toBe('true')
    expect(m.el.querySelector('.am-card-drill')?.getAttribute('aria-label')).toBe(`Open details for ${blocked.issueKey}`)
    await m.unmount()

    // Healthy, trusted, unselected: ETA present, no state prefix, no flags.
    const m2 = await card(ten[0])
    const name2 = accessibleName(m2.el)
    expect(name2.startsWith(ten[0].issueKey)).toBe(true)
    expect(count(name2, 'Selected')).toBe(0)
    expect(count(name2, 'Needs you')).toBe(0)
    expect(name2).toContain('Healthy')
    expect(name2).toContain('Working')
    expect(name2).toContain('Specification · 1/5')
    expect(name2).toContain('64 % complete')
    expect(name2).toContain('Lands ~')
    expect(count(name2, '64 %')).toBe(1)
    expect(m2.el.querySelector('.am-card-flags')).toBeNull()
    await m2.unmount()
  })

  it('renders supplemental tags restrained: capped inline, accessible overflow summary, never a lane', async () => {
    const tagged: Delivery = { ...ten[0], tags: ['security', 'infra', 'q3', 'billing', 'eu'] }
    const m = await card(tagged)
    const visible = [...m.el.querySelectorAll<HTMLElement>('.am-card-tag:not(.am-card-tag--more)')].map((el) => el.textContent)
    expect(visible).toEqual(['#security', '#infra', '#q3'])
    expect(visible).toHaveLength(MAX_VISIBLE_TAGS)
    expect(m.el.querySelector('.am-card-tag--more')?.textContent).toBe('+2')
    // The visible chips are decoration; the full list is announced once.
    expect(m.el.querySelector('.am-card-tags-visible')?.getAttribute('aria-hidden')).toBe('true')
    const name = accessibleName(m.el)
    expect(name).toContain('Tags: security, infra, q3, billing, eu')
    expect(count(name, 'security')).toBe(1)
    // Tags do not change the lane the card belongs to.
    expect(tagged.lane).toEqual(ten[0].lane)
    await m.unmount()

    const untagged = await card({ ...ten[0], tags: [] })
    expect(untagged.el.querySelector('.am-card-tags')).toBeNull()
    await untagged.unmount()
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

  it('offers no drill / activation when the card is already the focus (activatable=false)', async () => {
    const onActivate = vi.fn()
    const m = await card(ten[1], { selected: true, activatable: false, onActivate })
    expect(m.el.querySelector('.am-card-drill')).toBeNull()
    m.el.querySelector<HTMLButtonElement>('.am-card-hit')!.click()
    expect(onActivate).not.toHaveBeenCalled()
    await m.unmount()
  })
})
