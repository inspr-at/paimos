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
import { estimateView } from '@/components/agent-mode/agentModePresentation'
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
    expect(p.optimisticAt).toBe(trustedEta.optimisticAt)
    expect(p.pessimisticAt).toBe(trustedEta.pessimisticAt)
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

  it('withholds untrusted fields but presents eligible low-confidence ETA only as explicit bounds', () => {
    const healthy = { health: 'healthy' as const, activity: { kind: 'working' as const, text: 'x', since: null }, freshness: { state: 'fresh' as const, lastReportAt: null } }
    const untrusted = estimatePresentation(d({ ...healthy, progress: { ...trustedProgress, trusted: false }, eta: { ...trustedEta, trusted: false } }))
    expect(untrusted.showPercent).toBe(false)
    expect(untrusted.percentReason).toBe('untrusted')
    expect(untrusted.showEta).toBe(false)
    expect(untrusted.etaReason).toBe('untrusted')

    const low = estimatePresentation(d({ ...healthy, progress: { ...trustedProgress, confidence: 'low' }, eta: { ...trustedEta, confidence: 'low' } }))
    expect(low.showPercent).toBe(false)
    expect(low.percentReason).toBe('low_confidence')
    expect(low.showEta).toBe(true)
    expect(low.rangeOnly).toBe(true)
    expect(low.etaReason).toBe('ok')
    expect(low.landingAt).toBeNull()
    expect(low.optimisticAt).toBe(trustedEta.optimisticAt)
    expect(low.pessimisticAt).toBe(trustedEta.pessimisticAt)

    const mixed = estimatePresentation(d({ ...healthy, progress: trustedProgress, eta: { ...trustedEta, confidence: 'low' } }))
    expect(mixed.showPercent).toBe(true)
    expect(mixed.showEta).toBe(true)
    expect(mixed.rangeOnly).toBe(true)
  })

  it('withholds suppressed, missing and invalid ETA ranges without deriving a point or midpoint', () => {
    const healthy = { health: 'healthy' as const, activity: { kind: 'working' as const, text: 'x', since: null }, freshness: { state: 'fresh' as const, lastReportAt: null } }
    const suppressed = estimatePresentation(d({ ...healthy, suppressionCodes: ['source_disagreement'], eta: { ...trustedEta, confidence: 'low' } }))
    expect(suppressed.showEta).toBe(false)
    expect(suppressed.etaReason).toBe('suppressed')

    const missingBounds = estimatePresentation(d({ ...healthy, eta: { ...trustedEta, confidence: 'low', optimisticAt: null, pessimisticAt: null } }))
    expect(missingBounds.showEta).toBe(false)
    expect(missingBounds.etaReason).toBe('low_confidence')

    for (const eta of [
      { ...trustedEta, confidence: 'low' as const, optimisticAt: 'invalid' },
      { ...trustedEta, confidence: 'low' as const, optimisticAt: trustedEta.pessimisticAt, pessimisticAt: trustedEta.optimisticAt },
      { ...trustedEta, confidence: 'high' as const, landingAt: '2026-02-31T12:00:00Z' },
    ]) {
      const invalid = estimatePresentation(d({ ...healthy, eta }))
      expect(invalid.showEta).toBe(false)
      expect(invalid.rangeOnly).toBe(false)
      expect(invalid.landingAt).toBeNull()
      expect(invalid.etaReason).toBe('invalid')
    }
  })

  it('shares range-only presentation without a point landing or remaining duration', () => {
    const ranged = d({
      health: 'healthy',
      activity: { kind: 'working', text: 'x', since: null },
      freshness: { state: 'fresh', lastReportAt: null },
      eta: { ...trustedEta, confidence: 'low' },
    })
    const view = estimateView(ranged, 'en-US', Date.parse('2026-08-20T16:00:00Z'))
    expect(view.presentation.rangeOnly).toBe(true)
    expect(view.rangeLabel).toContain('–')
    expect(view.landingLabel).toBeNull()
    expect(view.remainingLabel).toBeNull()
  })

  it('preserves authoritative bounds alongside a confident point estimate', () => {
    const point = d({
      health: 'healthy',
      activity: { kind: 'working', text: 'x', since: null },
      freshness: { state: 'fresh', lastReportAt: null },
      eta: trustedEta,
    })
    const view = estimateView(point, 'en-US', Date.parse('2026-08-20T16:00:00Z'))
    expect(view.presentation.rangeOnly).toBe(false)
    expect(view.landingLabel).not.toBeNull()
    expect(view.remainingLabel).not.toBeNull()
    expect(view.rangeLabel).toContain('–')
  })

  it('withholds every precision field and basis through the shared retained-snapshot policy', () => {
    const point = d({
      health: 'healthy',
      activity: { kind: 'working', text: 'x', since: null },
      freshness: { state: 'fresh', lastReportAt: null },
      progress: { ...trustedProgress, basis: 'private point basis' },
      eta: { ...trustedEta, basis: 'private range basis' },
    })
    const retained = estimateView(point, 'en-US', Date.parse('2026-08-20T16:00:00Z'), true)
    expect(retained.presentation).toMatchObject({
      showPercent: false,
      percent: null,
      showEta: false,
      rangeOnly: false,
      landingAt: null,
      optimisticAt: null,
      pessimisticAt: null,
      percentReason: 'offline',
      etaReason: 'offline',
    })
    expect(retained.landingLabel).toBeNull()
    expect(retained.remainingLabel).toBeNull()
    expect(retained.rangeLabel).toBeNull()
    expect(retained.basis).toBeNull()
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
