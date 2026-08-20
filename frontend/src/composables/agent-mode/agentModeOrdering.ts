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

// PAI-805 — lane derivation and deterministic ordering.
//
// Lanes are structural and truthful (PAI-801): project across projects,
// epic within a project, plus an explicit Ungrouped lane for deliveries
// whose issue has no epic parent. Tags never influence lanes.
//
// Ordering is a pure function of snapshot data (never of wall-clock
// time), so a re-render without new data can never reshuffle cards.

import type { Delivery } from '@/services/agentMode'

export interface AgentModeLane {
  key: string
  projectId: number
  epicId: number | null
  epicKey: string | null
  epicTitle: string | null
  ungrouped: boolean
  deliveryIds: string[]
}

export interface AgentModeProjectGroup {
  projectId: number
  projectKey: string
  projectName: string
  lanes: AgentModeLane[]
  /** Total deliveries across the project's lanes. */
  count: number
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

export function compareKeys(a: string | null | undefined, b: string | null | undefined): number {
  if (a == null && b == null) return 0
  if (a == null) return 1
  if (b == null) return -1
  return collator.compare(a, b)
}

function trustedLandingMs(d: Delivery): number | null {
  if (!d.eta || !d.eta.trusted || !d.eta.landingAt) return null
  const ms = Date.parse(d.eta.landingAt)
  return Number.isFinite(ms) ? ms : null
}

/**
 * Canonical in-lane order: highest attention first, then soonest trusted
 * landing (untrusted / unknown last), then issue key (natural), then id.
 * The final id comparison makes the order total, so two snapshots with
 * the same data always produce the same order.
 */
export function compareDeliveries(a: Delivery, b: Delivery): number {
  if (a.attention.level !== b.attention.level) return b.attention.level - a.attention.level
  const la = trustedLandingMs(a)
  const lb = trustedLandingMs(b)
  if (la != null || lb != null) {
    if (la == null) return 1
    if (lb == null) return -1
    if (la !== lb) return la - lb
  }
  const byKey = compareKeys(a.issueKey, b.issueKey)
  if (byKey !== 0) return byKey
  return compareKeys(a.id, b.id)
}

/**
 * Groups deliveries into project groups → epic lanes (+ Ungrouped last).
 * Projects sort by name then key; epics by key; Ungrouped always trails.
 */
export function buildProjectGroups(deliveries: readonly Delivery[]): AgentModeProjectGroup[] {
  const projects = new Map<number, AgentModeProjectGroup & { laneMap: Map<string, AgentModeLane & { items: Delivery[] }> }>()
  for (const d of deliveries) {
    let group = projects.get(d.lane.projectId)
    if (!group) {
      group = {
        projectId: d.lane.projectId,
        projectKey: d.lane.projectKey,
        projectName: d.lane.projectName,
        lanes: [],
        count: 0,
        laneMap: new Map(),
      }
      projects.set(d.lane.projectId, group)
    }
    let lane = group.laneMap.get(d.lane.key)
    if (!lane) {
      lane = {
        key: d.lane.key,
        projectId: d.lane.projectId,
        epicId: d.lane.epicId,
        epicKey: d.lane.epicKey,
        epicTitle: d.lane.epicTitle,
        ungrouped: d.lane.epicId == null,
        deliveryIds: [],
        items: [],
      }
      group.laneMap.set(d.lane.key, lane)
    }
    lane.items.push(d)
    group.count += 1
  }

  const groups = [...projects.values()].sort((a, b) => {
    const byName = compareKeys(a.projectName, b.projectName)
    if (byName !== 0) return byName
    const byKey = compareKeys(a.projectKey, b.projectKey)
    if (byKey !== 0) return byKey
    return a.projectId - b.projectId
  })

  return groups.map((group) => {
    const lanes = [...group.laneMap.values()].sort((a, b) => {
      if (a.ungrouped !== b.ungrouped) return a.ungrouped ? 1 : -1
      const byKey = compareKeys(a.epicKey, b.epicKey)
      if (byKey !== 0) return byKey
      return compareKeys(a.key, b.key)
    })
    return {
      projectId: group.projectId,
      projectKey: group.projectKey,
      projectName: group.projectName,
      count: group.count,
      lanes: lanes.map((lane) => ({
        key: lane.key,
        projectId: lane.projectId,
        epicId: lane.epicId,
        epicKey: lane.epicKey,
        epicTitle: lane.epicTitle,
        ungrouped: lane.ungrouped,
        deliveryIds: [...lane.items].sort(compareDeliveries).map((d) => d.id),
      })),
    }
  })
}

/** Visual reading order (project → lane → card) used for arrow-key travel. */
export function flattenOrder(groups: readonly AgentModeProjectGroup[]): string[] {
  const out: string[] = []
  for (const g of groups) for (const lane of g.lanes) out.push(...lane.deliveryIds)
  return out
}

const TOMBSTONE_PREFIX = '__am_tombstone__'

function membershipKey(projectId: number, laneKey: string): string {
  return `${projectId}:${laneKey}`
}

function tombstoneId(groupIndex: number, laneIndex: number, slotIndex: number): string {
  // The token is scoped only to the already-visible frozen slot. It must not
  // retain the delivery id or stale project/lane identifiers: omission from an
  // authorized refresh can mean access revocation, not ordinary deletion.
  return `${TOMBSTONE_PREFIX}:${groupIndex}:${laneIndex}:${slotIndex}`
}

/**
 * Reconciles a frozen layout with fresh data while interaction is held:
 * existing cards keep their slot, new deliveries are appended at the end
 * of their lane, and lanes/projects that are new appear at the end. No
 * existing target ever moves. A delivery that left the current layout or
 * changed project/lane leaves only an opaque tombstone in its old slot; its
 * live identity is appended exactly once in the current authorized lane.
 */
export function reconcileFrozenGroups(
  frozen: readonly AgentModeProjectGroup[],
  fresh: readonly AgentModeProjectGroup[],
): AgentModeProjectGroup[] {
  const freshMembership = new Map<string, string>()
  for (const group of fresh) {
    for (const lane of group.lanes) {
      for (const id of lane.deliveryIds) freshMembership.set(id, membershipKey(group.projectId, lane.key))
    }
  }

  const result: AgentModeProjectGroup[] = frozen.map((g, groupIndex) => ({
    ...g,
    lanes: g.lanes.map((l, laneIndex) => ({
      ...l,
      deliveryIds: l.deliveryIds.map((id, slotIndex) => {
        if (id.startsWith(TOMBSTONE_PREFIX)) return id
        const currentMembership = freshMembership.get(id)
        return currentMembership === membershipKey(g.projectId, l.key)
          ? id
          : tombstoneId(groupIndex, laneIndex, slotIndex)
      }),
    })),
  }))
  // Only a live id in its CURRENT project+lane owns that identity. A moved
  // delivery's old slot is now an opaque tombstone, so the live identity can
  // be appended once to its current authorized lane.
  const knownIds = new Set(flattenOrder(result).filter((id) => !id.startsWith(TOMBSTONE_PREFIX)))
  for (const freshGroup of fresh) {
    let group = result.find((g) => g.projectId === freshGroup.projectId)
    if (!group) {
      group = { ...freshGroup, lanes: [], count: 0 }
      result.push(group)
    }
    for (const freshLane of freshGroup.lanes) {
      let lane = group.lanes.find((l) => l.key === freshLane.key)
      if (!lane) {
        lane = { ...freshLane, deliveryIds: [] }
        group.lanes.push(lane)
      }
      for (const id of freshLane.deliveryIds) {
        if (!knownIds.has(id)) {
          lane.deliveryIds.push(id)
          knownIds.add(id)
        }
      }
    }
  }
  for (const g of result) g.count = g.lanes.reduce((n, l) => n + l.deliveryIds.length, 0)
  return result
}

/** How long a neutral tombstone may keep its slot under interaction hold
 * before it collapses anyway (security / honesty outrank stability). */
export const TOMBSTONE_TTL_MS = 5_000

/**
 * Drops the given ids from a layout. Lanes and project groups that end up
 * empty are removed as well. Used to expire tombstones.
 */
export function pruneIds(groups: readonly AgentModeProjectGroup[], ids: ReadonlySet<string>): AgentModeProjectGroup[] {
  if (ids.size === 0) return [...groups]
  const out: AgentModeProjectGroup[] = []
  for (const g of groups) {
    const lanes = g.lanes
      .map((l) => ({ ...l, deliveryIds: l.deliveryIds.filter((id) => !ids.has(id)) }))
      .filter((l) => l.deliveryIds.length > 0)
    if (lanes.length === 0) continue
    out.push({ ...g, lanes, count: lanes.reduce((n, l) => n + l.deliveryIds.length, 0) })
  }
  return out
}

/**
 * Security rule for frozen layouts (PAI-805): a lane or project group whose
 * deliveries ALL left the authorized snapshot must disappear immediately —
 * even under interaction hold — because its header (project name, epic
 * key / title) is grouping metadata of deliveries the user may no longer
 * see. Lanes that still contain at least one live delivery keep their
 * slots; the caller renders the gone ids as neutral tombstones.
 */
export function pruneDeadLanes(
  groups: readonly AgentModeProjectGroup[],
  isLive: (id: string) => boolean,
): AgentModeProjectGroup[] {
  const out: AgentModeProjectGroup[] = []
  for (const g of groups) {
    const lanes = g.lanes.filter((l) => l.deliveryIds.some(isLive))
    if (lanes.length === 0) continue
    out.push({ ...g, lanes, count: lanes.reduce((n, l) => n + l.deliveryIds.length, 0) })
  }
  return out
}
