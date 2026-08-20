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
import { estimatePresentation, remainingMs } from './agentModeTrust'

const base = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries[0]

function d(p: Partial<Delivery>): Delivery {
  return { ...base, ...p }
}

const trustedProgress = { percent: 64, trusted: true, confidence: 'high' as const, source: null, basis: null, revision: 1 }
const trustedEta = { landingAt: '2026-08-20T16:40:00Z', optimisticAt: '2026-08-20T16:30:00Z', pessimisticAt: '2026-08-20T17:00:00Z', trusted: true, confidence: 'high' as const, basis: null, calculatedAt: null }

describe('agentModeTrust (client side of PAI-803)', () => {
  it('shows percent and landing time only for trusted, confident, fresh, unblocked work', () => {
    const p = estimatePresentation(d({ health: 'healthy', activity: { kind: 'working', text: 'x', since: null }, freshness: { state: 'fresh', lastReportAt: null }, progress: trustedProgress, eta: trustedEta }))
    expect(p.showPercent).toBe(true)
    expect(p.percent).toBe(64)
    expect(p.showEta).toBe(true)
    expect(p.landingAt).toBe(trustedEta.landingAt)
    expect(p.percentReason).toBe('ok')
    expect(p.etaReason).toBe('ok')
  })

  it.each([
    ['blocked health', { health: 'blocked' as const }, 'blocked'],
    ['blocked activity', { activity: { kind: 'blocked' as const, text: null, since: null } }, 'blocked'],
    ['waiting', { activity: { kind: 'waiting' as const, text: null, since: null } }, 'waiting'],
    ['stale', { freshness: { state: 'stale' as const, lastReportAt: null } }, 'stale'],
    ['unknown freshness', { freshness: { state: 'unknown' as const, lastReportAt: null } }, 'stale'],
  ])('suppresses both percent and ETA when %s', (_label, patch, reason) => {
    const p = estimatePresentation(d({ health: 'healthy', activity: { kind: 'working', text: 'x', since: null }, freshness: { state: 'fresh', lastReportAt: null }, progress: trustedProgress, eta: trustedEta, ...patch }))
    expect(p.showPercent).toBe(false)
    expect(p.showEta).toBe(false)
    expect(p.percent).toBeNull()
    expect(p.landingAt).toBeNull()
    expect(p.percentReason).toBe(reason)
    expect(p.etaReason).toBe(reason)
  })

  it('withholds untrusted or low-confidence estimates per field', () => {
    const healthy = { health: 'healthy' as const, activity: { kind: 'working' as const, text: 'x', since: null }, freshness: { state: 'fresh' as const, lastReportAt: null } }
    const untrusted = estimatePresentation(d({ ...healthy, progress: { ...trustedProgress, trusted: false }, eta: { ...trustedEta, trusted: false } }))
    expect(untrusted.showPercent).toBe(false)
    expect(untrusted.percentReason).toBe('untrusted')
    expect(untrusted.showEta).toBe(false)
    expect(untrusted.etaReason).toBe('untrusted')

    const low = estimatePresentation(d({ ...healthy, progress: { ...trustedProgress, confidence: 'low' }, eta: { ...trustedEta, confidence: 'low' } }))
    expect(low.showPercent).toBe(false)
    expect(low.percentReason).toBe('low_confidence')
    expect(low.showEta).toBe(false)
    expect(low.etaReason).toBe('low_confidence')

    const mixed = estimatePresentation(d({ ...healthy, progress: trustedProgress, eta: { ...trustedEta, confidence: 'low' } }))
    expect(mixed.showPercent).toBe(true)
    expect(mixed.showEta).toBe(false)
  })

  it('reports "none" when the API carries no estimate at all', () => {
    const p = estimatePresentation(d({ progress: null, eta: null }))
    expect(p.percentReason).toBe('none')
    expect(p.etaReason).toBe('none')
    expect(p.showPercent).toBe(false)
    expect(p.showEta).toBe(false)
  })

  it('computes remaining time against the server clock', () => {
    const serverNow = Date.parse('2026-08-20T16:00:00Z')
    expect(remainingMs('2026-08-20T16:40:00Z', serverNow)).toBe(40 * 60_000)
    expect(remainingMs(null, serverNow)).toBeNull()
    expect(remainingMs('not a date', serverNow)).toBeNull()
  })
})
