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

import { describe, expect, it } from 'vitest'

import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { Delivery } from '@/services/agentMode'
import { pickDefaultSelection, resolveSelection, stepSelection } from './agentModeSelection'

const ten = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries

function patch(d: Delivery, p: Partial<Delivery>): Delivery {
  return { ...d, ...p }
}

describe('agentModeSelection (PAI-805 invariants)', () => {
  it('restores the remembered delivery when it is still authorized', () => {
    expect(resolveSelection(ten, null, ten[5].id)).toEqual({ id: ten[5].id, origin: 'restored' })
  })

  it('keeps the in-memory selection over memory when still present', () => {
    expect(resolveSelection(ten, ten[1].id, ten[5].id)).toEqual({ id: ten[1].id, origin: 'kept' })
  })

  it('falls back to highest attention when nothing can be restored', () => {
    const choice = resolveSelection(ten, 'gone', 'also-gone')
    expect(choice.origin).toBe('default')
    const picked = ten.find((d) => d.id === choice.id)!
    const maxAttention = Math.max(...ten.map((d) => d.attention.level))
    expect(picked.attention.level).toBe(maxAttention)
  })

  it('breaks attention ties by soonest trusted landing, then issue key, then id', () => {
    const base = ten[0]
    const eta = (at: string, trusted = true) => ({ landingAt: at, optimisticAt: null, pessimisticAt: null, trusted, confidence: 'high' as const, basis: null, calculatedAt: null })
    const none = { level: 0 as const, reason: null, since: null }
    const list = [
      patch(base, { id: 'late', issueKey: 'PAI-1', attention: none, eta: eta('2026-08-20T18:00:00Z') }),
      patch(base, { id: 'soon', issueKey: 'PAI-9', attention: none, eta: eta('2026-08-20T14:00:00Z') }),
      patch(base, { id: 'untrusted-sooner', issueKey: 'PAI-0', attention: none, eta: eta('2026-08-20T13:00:00Z', false) }),
    ]
    expect(pickDefaultSelection(list)).toBe('soon')
    const tie = [
      patch(base, { id: 'b', issueKey: 'PAI-20', attention: none, eta: null }),
      patch(base, { id: 'a', issueKey: 'PAI-3', attention: none, eta: null }),
      patch(base, { id: 'a2', issueKey: 'PAI-3', attention: none, eta: null }),
    ]
    expect(pickDefaultSelection(tie)).toBe('a')
    // Order of input never matters.
    expect(pickDefaultSelection([...tie].reverse())).toBe('a')
  })

  it('yields no selection only when there is nothing to select', () => {
    expect(resolveSelection([], 'x', 'y')).toEqual({ id: null, origin: 'none' })
    expect(pickDefaultSelection([])).toBeNull()
  })

  it('steps along the visual order and clamps at the ends', () => {
    const order = ['a', 'b', 'c']
    expect(stepSelection(order, 'a', 1)).toBe('b')
    expect(stepSelection(order, 'c', 1)).toBe('c')
    expect(stepSelection(order, 'a', -1)).toBe('a')
    expect(stepSelection(order, null, 1)).toBe('a')
    expect(stepSelection(order, null, -1)).toBe('c')
    expect(stepSelection(order, 'zzz', 1)).toBe('a')
    expect(stepSelection([], 'a', 1)).toBeNull()
  })
})
