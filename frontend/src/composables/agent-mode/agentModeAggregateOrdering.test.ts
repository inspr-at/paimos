/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { describe, expect, it } from 'vitest'

import { makeFixtureAggregateSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import {
  aggregateFocusOrder,
  nearestSurvivingAggregateFocusKey,
  reconcileAggregateOrder,
} from './agentModeAggregateOrdering'

function aggregates() {
  return normalizeWireSnapshot(makeFixtureAggregateSnapshot(100), 0).aggregates!
}

describe('Detail-100 aggregate interaction hold ordering', () => {
  it('keeps surviving identity positions while using fresh labels and counts', () => {
    const previous = aggregates()
    const fresh = structuredClone(previous)
    fresh.projects.reverse()
    fresh.projects[0].projectName = 'Fresh authorized label'
    fresh.projects[0].counts.activeTotal += 7
    fresh.projects[0].lanes.reverse()

    const held = reconcileAggregateOrder(previous, fresh)
    expect(held.projects.map((project) => project.projectId)).toEqual(previous.projects.map((project) => project.projectId))
    const changed = held.projects.find((project) => project.projectName === 'Fresh authorized label')!
    expect(changed.counts.activeTotal).toBe(fresh.projects[0].counts.activeTotal)
    expect(changed.lanes.map((lane) => lane.laneKey)).toEqual(
      previous.projects.find((project) => project.projectId === changed.projectId)!.lanes.map((lane) => lane.laneKey),
    )
  })

  it('drops omitted identities and appends genuinely new targets', () => {
    const previous = aggregates()
    const fresh = structuredClone(previous)
    const removed = fresh.projects.shift()!
    const newcomer = { ...structuredClone(removed), projectId: 99, projectKey: 'NEW', projectName: 'New project' }
    newcomer.lanes = []
    fresh.projects.push(newcomer)

    const held = reconcileAggregateOrder(previous, fresh)
    expect(held.projects.some((project) => project.projectId === removed.projectId)).toBe(false)
    expect(held.projects[held.projects.length - 1]?.projectId).toBe(99)
  })

  it('recovers omitted focus by previous identity distance, preferring the successor on ties', () => {
    const previous = aggregates()
    const order = aggregateFocusOrder(previous)
    const focused = order[2]
    const fresh = structuredClone(previous)
    for (const project of fresh.projects) {
      project.lanes = project.lanes.filter((lane) => !focused.endsWith(`:${lane.laneKey}`))
    }
    const survivors = new Set(aggregateFocusOrder(fresh))
    const focusedIndex = order.indexOf(focused)
    const expected = order.slice(focusedIndex + 1).find((key) => survivors.has(key))
      ?? [...order.slice(0, focusedIndex)].reverse().find((key) => survivors.has(key))
      ?? null

    expect(nearestSurvivingAggregateFocusKey(previous, fresh, focused)).toBe(expected)
    const surviving = aggregateFocusOrder(fresh)[1]
    expect(nearestSurvivingAggregateFocusKey(previous, fresh, surviving)).toBe(surviving)
    expect(nearestSurvivingAggregateFocusKey(previous, null, focused)).toBeNull()
  })
})
