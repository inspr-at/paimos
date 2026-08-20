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
import { EMPTY_FILTERS, applyFilters, exclusionReason, filtersActive, nextRadioIndex } from './agentModeFilters'

const ten = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries

describe('agentModeFilters (PAI-805)', () => {
  it('passes everything through when no filter is active', () => {
    expect(filtersActive(EMPTY_FILTERS)).toBe(false)
    expect(applyFilters(ten, EMPTY_FILTERS)).toHaveLength(10)
    expect(exclusionReason(ten[0], EMPTY_FILTERS)).toBeNull()
  })

  it('reports the first reason a delivery is excluded (project → health → query)', () => {
    const other = ten.find((d) => d.lane.projectId !== ten[0].lane.projectId)!
    const f = { projectId: ten[0].lane.projectId, health: 'blocked' as const, query: 'zzz' }
    expect(exclusionReason(other, f)).toBe('project')
    const sameProjectHealthy = ten.find((d) => d.lane.projectId === ten[0].lane.projectId && d.health === 'healthy')!
    expect(exclusionReason(sameProjectHealthy, f)).toBe('health')
    const blockedInProject = ten.find((d) => d.health === 'blocked')!
    expect(exclusionReason(blockedInProject, { projectId: blockedInProject.lane.projectId, health: 'blocked', query: 'nope-nope' })).toBe('query')
  })

  it('matches queries against key, title, agent, lane and tags case-insensitively', () => {
    const d = ten[0]
    expect(applyFilters(ten, { ...EMPTY_FILTERS, query: d.issueKey.toLowerCase() }).map((x) => x.id)).toContain(d.id)
    expect(applyFilters(ten, { ...EMPTY_FILTERS, query: 'codex' }).every((x) => x.actor?.label === 'Codex')).toBe(true)
    expect(applyFilters(ten, { ...EMPTY_FILTERS, query: 'security' }).every((x) => x.tags.includes('security'))).toBe(true)
  })

  it('narrows by health vocabulary (attention / blocked / stale)', () => {
    expect(applyFilters(ten, { ...EMPTY_FILTERS, health: 'blocked' }).every((x) => x.health === 'blocked' || x.blockers.length > 0)).toBe(true)
    expect(applyFilters(ten, { ...EMPTY_FILTERS, health: 'stale' }).every((x) => x.freshness.state === 'stale' || x.freshness.state === 'unknown')).toBe(true)
    expect(applyFilters(ten, { ...EMPTY_FILTERS, health: 'attention' }).every((x) => x.attention.level > 0 || x.health === 'attention' || x.health === 'at_risk')).toBe(true)
  })
})

describe('nextRadioIndex (health radiogroup keyboard contract)', () => {
  it('wraps with arrows and jumps with Home / End; ignores other keys', () => {
    expect(nextRadioIndex(0, 'ArrowRight', 4)).toBe(1)
    expect(nextRadioIndex(3, 'ArrowRight', 4)).toBe(0)
    expect(nextRadioIndex(0, 'ArrowDown', 4)).toBe(1)
    expect(nextRadioIndex(0, 'ArrowLeft', 4)).toBe(3)
    expect(nextRadioIndex(2, 'ArrowUp', 4)).toBe(1)
    expect(nextRadioIndex(2, 'Home', 4)).toBe(0)
    expect(nextRadioIndex(1, 'End', 4)).toBe(3)
    expect(nextRadioIndex(1, 'Enter', 4)).toBeNull()
    expect(nextRadioIndex(1, 'Escape', 4)).toBeNull()
    expect(nextRadioIndex(0, 'ArrowRight', 0)).toBeNull()
  })
})
