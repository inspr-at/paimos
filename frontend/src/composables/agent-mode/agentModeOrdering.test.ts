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
import { buildProjectGroups, compareDeliveries, flattenOrder, pruneDeadLanes, pruneIds, reconcileFrozenGroups } from './agentModeOrdering'

function load(n: 1 | 10 | 100): Delivery[] {
  return normalizeWireSnapshot(makeFixtureSnapshot(n), 0).deliveries
}

function withPatch(d: Delivery, patch: Partial<Delivery>): Delivery {
  return { ...d, ...patch }
}

describe('agentModeOrdering (PAI-805)', () => {
  it('derives project → epic lanes with an explicit Ungrouped lane last', () => {
    const groups = buildProjectGroups(load(10))
    // Projects sort by name: Agent runtime, PAIMOS Core platform, Release operations.
    expect(groups.map((g) => g.projectKey)).toEqual(['RUN', 'PAI', 'REL'])
    const pai = groups.find((g) => g.projectKey === 'PAI')!
    expect(pai.lanes.map((l) => l.epicKey ?? 'ungrouped')).toEqual(['PAI-760', 'PAI-801', 'ungrouped'])
    expect(pai.lanes[pai.lanes.length - 1].ungrouped).toBe(true)
    const rel = groups.find((g) => g.projectKey === 'REL')!
    expect(rel.lanes).toHaveLength(1)
    expect(rel.lanes[0].ungrouped).toBe(true)
    expect(groups.reduce((n, g) => n + g.count, 0)).toBe(10)
  })

  it('keeps every delivery exactly once in the flattened visual order for 1, 10 and 100', () => {
    for (const n of [1, 10, 100] as const) {
      const list = load(n)
      const order = flattenOrder(buildProjectGroups(list))
      expect(order).toHaveLength(n)
      expect(new Set(order).size).toBe(n)
    }
  })

  it('orders within a lane by attention, then soonest trusted landing, then issue key', () => {
    const [a, b, c, d] = load(10).slice(0, 4)
    const base = { ...a, lane: a.lane }
    const quiet = withPatch(base, { id: 'q', issueKey: 'PAI-900', attention: { level: 0, reason: null, since: null }, eta: { landingAt: '2026-08-20T16:00:00Z', optimisticAt: null, pessimisticAt: null, trusted: true, confidence: 'high', basis: null, calculatedAt: null } })
    const sooner = withPatch(base, { id: 's', issueKey: 'PAI-901', attention: { level: 0, reason: null, since: null }, eta: { landingAt: '2026-08-20T15:00:00Z', optimisticAt: null, pessimisticAt: null, trusted: true, confidence: 'high', basis: null, calculatedAt: null } })
    const untrusted = withPatch(base, { id: 'u', issueKey: 'PAI-100', attention: { level: 0, reason: null, since: null }, eta: { landingAt: '2026-08-20T14:00:00Z', optimisticAt: null, pessimisticAt: null, trusted: false, confidence: 'high', basis: null, calculatedAt: null } })
    const urgent = withPatch(base, { id: 'x', issueKey: 'PAI-999', attention: { level: 3, reason: 'blocked', since: null }, eta: null })
    const sorted = [quiet, sooner, untrusted, urgent].sort(compareDeliveries).map((x) => x.id)
    expect(sorted).toEqual(['x', 's', 'q', 'u'])
    void b
    void c
    void d
  })

  it('is deterministic: the same data always yields the same order', () => {
    const list = load(100)
    const first = flattenOrder(buildProjectGroups(list))
    const shuffled = [...list].reverse()
    const second = flattenOrder(buildProjectGroups(shuffled))
    expect(second).toEqual(first)
  })

  it('reconciles a frozen layout without moving any existing card', () => {
    const list = load(10)
    const frozen = buildProjectGroups(list)
    const before = flattenOrder(frozen)
    // New data: one delivery gains top attention (would jump to the front),
    // one disappears, one new appears.
    const mutated = list
      .filter((d) => d.id !== before[2])
      .map((d) => (d.id === before[before.length - 1] ? withPatch(d, { attention: { level: 3, reason: 'now urgent', since: null } }) : d))
    const fresh = buildProjectGroups([
      ...mutated,
      withPatch(list[0], { id: 'dlv-new', issueKey: 'PAI-999' }),
    ])
    const reconciled = reconcileFrozenGroups(frozen, fresh)
    const after = flattenOrder(reconciled)
    // Every live previously laid-out id keeps its relative position. The one
    // that left becomes an opaque tombstone rather than retaining identity …
    const beforeSet = new Set(before)
    expect(after.filter((id) => beforeSet.has(id))).toEqual(before.filter((id) => id !== before[2]))
    expect(after).not.toContain(before[2])
    const tombstone = after.find((id) => id.startsWith('__am_tombstone__'))
    expect(tombstone).toBeDefined()
    expect(tombstone).not.toContain(before[2])
    expect(tombstone).not.toContain(list.find((d) => d.id === before[2])!.lane.key)
    // … and the newcomer is appended at the end of its own lane.
    expect(after).toContain('dlv-new')
    const newLane = reconciled.flatMap((g) => g.lanes).find((l) => l.deliveryIds.includes('dlv-new'))!
    expect(newLane.key).toBe(list[0].lane.key)
    expect(newLane.deliveryIds[newLane.deliveryIds.length - 1]).toBe('dlv-new')
    // Canonical order, applied once the hold releases, differs.
    expect(flattenOrder(fresh)).not.toEqual(after)
  })
})

describe('agentModeOrdering — tombstone pruning and tag neutrality (PAI-805 corrections)', () => {
  it('pruneIds drops ids and collapses lanes / groups that become empty', () => {
    const list = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries
    const groups = buildProjectGroups(list)
    const rel = groups.find((g) => g.projectKey === 'REL')!
    const pruned = pruneIds(groups, new Set([...rel.lanes[0].deliveryIds, 'dlv-813']))
    expect(pruned.find((g) => g.projectKey === 'REL')).toBeUndefined()
    expect(flattenOrder(pruned)).not.toContain('dlv-813')
    expect(flattenOrder(pruned)).toHaveLength(10 - rel.count - 1)
    expect(pruned.reduce((n, g) => n + g.count, 0)).toBe(10 - rel.count - 1)
    // Relative order of survivors is untouched.
    const before = flattenOrder(groups)
    const after = flattenOrder(pruned)
    expect(before.filter((id) => after.includes(id))).toEqual(after)
    // No-op when nothing matches.
    expect(flattenOrder(pruneIds(groups, new Set(['nope'])))).toEqual(before)
  })

  it('pruneDeadLanes removes lanes / projects with no live delivery but keeps lanes that still have one', () => {
    const list = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries
    const frozen = buildProjectGroups(list)
    const rel = frozen.find((g) => g.projectKey === 'REL')!
    const live = new Set(list.map((d) => d.id))
    for (const id of rel.lanes[0].deliveryIds) live.delete(id) // whole project revoked
    live.delete('dlv-813') // one of two in RUN / RUN-40
    const pruned = pruneDeadLanes(frozen, (id) => live.has(id))
    expect(pruned.find((g) => g.projectKey === 'REL')).toBeUndefined()
    const runLane = pruned.flatMap((g) => g.lanes).find((l) => l.deliveryIds.includes('dlv-816'))!
    expect(runLane.deliveryIds).toContain('dlv-813') // tombstone slot survives next to a live card
    expect(pruned.flatMap((g) => g.lanes).every((l) => l.deliveryIds.some((id) => live.has(id)))).toBe(true)
  })

  it('tags never define lanes or order', () => {
    const list = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries
    const base = flattenOrder(buildProjectGroups(list))
    const baseLanes = buildProjectGroups(list).flatMap((g) => g.lanes.map((l) => l.key))
    const retagged = list.map((d, i) => ({ ...d, tags: i % 2 ? ['zzz', 'security'] : [] }))
    expect(flattenOrder(buildProjectGroups(retagged))).toEqual(base)
    expect(buildProjectGroups(retagged).flatMap((g) => g.lanes.map((l) => l.key))).toEqual(baseLanes)
  })
})
