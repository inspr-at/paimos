/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-807 interaction-hold ordering for aggregate drill targets. Identity
// controls position; every object (including every mutable label/count) is
// always taken from the fresh authorized snapshot. Removed identities vanish
// immediately and newcomers append in server-canonical order.

import type {
  AgentModeAggregates,
  AgentModeLaneAggregate,
  AgentModeProjectAggregate,
} from '@/services/agentModeAggregateSchema'

function reconcileLanes(
  previous: readonly AgentModeLaneAggregate[],
  fresh: readonly AgentModeLaneAggregate[],
): AgentModeLaneAggregate[] {
  const freshByKey = new Map(fresh.map((lane) => [lane.laneKey, lane]))
  const result: AgentModeLaneAggregate[] = []
  const used = new Set<string>()
  for (const old of previous) {
    const current = freshByKey.get(old.laneKey)
    if (!current) continue
    result.push(current)
    used.add(current.laneKey)
  }
  for (const current of fresh) if (!used.has(current.laneKey)) result.push(current)
  return result
}

export function reconcileAggregateOrder(
  previous: AgentModeAggregates | null,
  fresh: AgentModeAggregates,
): AgentModeAggregates {
  if (!previous) return fresh
  const freshById = new Map(fresh.projects.map((project) => [project.projectId, project]))
  const projects: AgentModeProjectAggregate[] = []
  const used = new Set<number>()
  for (const old of previous.projects) {
    const current = freshById.get(old.projectId)
    if (!current) continue
    projects.push({ ...current, lanes: reconcileLanes(old.lanes, current.lanes) })
    used.add(current.projectId)
  }
  for (const current of fresh.projects) {
    if (!used.has(current.projectId)) projects.push(current)
  }
  return { ...fresh, projects }
}
