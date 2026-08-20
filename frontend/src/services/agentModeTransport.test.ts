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

import { makeFixtureSnapshot } from './agentModeFixtures'
import { buildSnapshotPath, laneKeyFor, normalizeWireDelivery, normalizeWireSnapshot } from './agentModeTransport'

describe('agentModeTransport (PAI-805 / PAI-804 seam)', () => {
  it('builds the snapshot path with optional server hints', () => {
    expect(buildSnapshotPath()).toBe('/agent-mode/deliveries')
    expect(buildSnapshotPath({ projectId: 6 })).toBe('/agent-mode/deliveries?project_id=6')
    expect(buildSnapshotPath({ projectId: 6, epicId: 4655 })).toBe('/agent-mode/deliveries?project_id=6&epic_id=4655')
  })

  it('normalizes the 1 / 10 / 100 fixtures without dropping or inventing anything', () => {
    for (const n of [1, 10, 100] as const) {
      const snap = normalizeWireSnapshot(makeFixtureSnapshot(n), 1_000)
      expect(snap.deliveries).toHaveLength(n)
      expect(snap.serverTime).toBe('2026-08-20T13:48:00Z')
      expect(snap.revision).toBe(`fx-${n}-1`)
      expect(snap.receivedAt).toBe(1_000)
      expect(new Set(snap.deliveries.map((d) => d.id)).size).toBe(n)
    }
  })

  it('maps unknown or missing fields to explicit unknown / untrusted, never to fabricated values', () => {
    const d = normalizeWireDelivery({
      delivery_id: 42,
      issue_id: 7,
      project_id: 3,
      health: 'splendid',
      activity: { kind: 'dancing' },
      stage: { key: 'mystery' },
      freshness: { state: 'shiny' },
      progress: { percent: 250, confidence: 'absolute' },
      eta: { landing_at: '2026-08-20T15:00:00Z', confidence: 'high' },
    })!
    expect(d.id).toBe('42')
    expect(d.health).toBe('unknown')
    expect(d.activity.kind).toBe('unknown')
    expect(d.stage.key).toBe('unknown')
    expect(d.freshness.state).toBe('unknown')
    expect(d.progress).toEqual({ percent: 100, trusted: false, confidence: 'none', source: null, basis: null, revision: null })
    expect(d.eta?.trusted).toBe(false)
    expect(d.actor).toBeNull()
    expect(d.lane.key).toBe(laneKeyFor(3, null))
    expect(d.lane.epicId).toBeNull()
    expect(d.issueKey).toBe('#7')
  })

  it('drops deliveries without identity or project instead of inventing a lane, and dedupes ids', () => {
    const snap = normalizeWireSnapshot(
      {
        deliveries: [
          { issue_id: 1, project_id: 2, delivery_id: 'a' },
          { issue_id: 2, project_id: 2, delivery_id: 'a' },
          { issue_id: 3 },
          { project_id: 2 },
          null as never,
        ],
      },
      0,
    )
    expect(snap.deliveries.map((d) => d.id)).toEqual(['a'])
    expect(snap.serverTime).toBeNull()
    expect(snap.revision).toBeNull()
  })

  it('falls back to issue-rooted identity and the explicit ungrouped lane', () => {
    const d = normalizeWireDelivery({ issue_id: 99, project_id: 5, project_key: 'PAI' })!
    expect(d.id).toBe('issue:99')
    expect(d.lane.key).toBe('project:5/ungrouped')
    expect(d.lane.projectName).toBe('PAI')
    const e = normalizeWireDelivery({ issue_id: 1, project_id: 5, epic_id: 77, epic_key: 'PAI-1' })!
    expect(e.lane.key).toBe('project:5/epic:77')
  })
})
