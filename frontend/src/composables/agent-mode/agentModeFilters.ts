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

// PAI-805 — filter model. Filters narrow what is laid out in lanes; they
// never touch selection. A selected delivery excluded by the active
// filter is pinned above the results with the reason.

import type { Delivery } from '@/services/agentMode'

export type HealthFilter = 'all' | 'attention' | 'blocked' | 'stale'

export interface AgentModeFilters {
  projectId: number | null
  laneKey: string | null
  health: HealthFilter
  query: string
}

export type FilterExclusion = 'project' | 'lane' | 'health' | 'query'

export const EMPTY_FILTERS: AgentModeFilters = { projectId: null, laneKey: null, health: 'all', query: '' }

export function filtersActive(f: AgentModeFilters): boolean {
  return f.projectId != null || f.laneKey != null || f.health !== 'all' || f.query.trim() !== ''
}

function matchesHealth(d: Delivery, health: HealthFilter): boolean {
  switch (health) {
    case 'all':
      return true
    case 'attention':
      return d.attention.level > 0 || d.health === 'attention' || d.health === 'at_risk'
    case 'blocked':
      return d.health === 'blocked' || d.activity.kind === 'blocked' || d.blockers.length > 0
    case 'stale':
      return d.freshness.state === 'stale' || d.freshness.state === 'unknown'
    default:
      return true
  }
}

function matchesQuery(d: Delivery, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (q === '') return true
  const hay = [
    d.issueKey,
    d.title,
    d.actor?.label ?? '',
    d.actor?.name ?? '',
    d.lane.projectKey,
    d.lane.projectName,
    d.lane.epicKey ?? '',
    d.lane.epicTitle ?? '',
    ...d.tags,
  ]
    .join(' ')
    .toLowerCase()
  return hay.includes(q)
}

/** First reason a delivery is excluded, or null when it passes. */
export function exclusionReason(d: Delivery, f: AgentModeFilters): FilterExclusion | null {
  if (f.projectId != null && d.lane.projectId !== f.projectId) return 'project'
  if (f.laneKey != null && d.lane.key !== f.laneKey) return 'lane'
  if (!matchesHealth(d, f.health)) return 'health'
  if (!matchesQuery(d, f.query)) return 'query'
  return null
}

export function applyFilters(deliveries: readonly Delivery[], f: AgentModeFilters): Delivery[] {
  if (!filtersActive(f)) return [...deliveries]
  return deliveries.filter((d) => exclusionReason(d, f) === null)
}

/**
 * WAI-ARIA radiogroup keyboard contract used by the health filter:
 * Arrow Right / Down → next (wrapping), Arrow Left / Up → previous
 * (wrapping), Home → first, End → last. Any other key → null (not ours).
 */
export function nextRadioIndex(current: number, key: string, length: number): number | null {
  if (length <= 0) return null
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return (current + 1) % length
    case 'ArrowLeft':
    case 'ArrowUp':
      return (current - 1 + length) % length
    case 'Home':
      return 0
    case 'End':
      return length - 1
    default:
      return null
  }
}
