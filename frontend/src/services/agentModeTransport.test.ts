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

import { makeFixtureAggregateSnapshot, makeFixtureDelivery, makeFixtureSnapshot } from './agentModeFixtures'
import { buildSnapshotPath, laneKeyFor, normalizeWireDelivery, normalizeWireSnapshot } from './agentModeTransport'

describe('agentModeTransport (PAI-805 / PAI-804 seam)', () => {
  it('builds the snapshot path with optional server hints', () => {
    expect(buildSnapshotPath()).toBe('/agent-mode/deliveries')
    expect(buildSnapshotPath({ projectId: 6 })).toBe('/agent-mode/deliveries?project_id=6')
    expect(buildSnapshotPath({
      projectId: 6,
      laneKey: 'project:6/epic:4655',
      states: ['active', 'waiting'],
      attention: 'required',
      health: 'blocked',
      q: ' release ',
    })).toBe('/agent-mode/deliveries?project_id=6&lane_key=project%3A6%2Fepic%3A4655&state=active&state=waiting&attention=required&health=blocked&q=release')
  })

  it('normalizes the 1 / 10 / 100 fixtures without dropping or inventing anything', () => {
    for (const n of [1, 10, 100] as const) {
      const snap = normalizeWireSnapshot(makeFixtureSnapshot(n), 1_000)
      expect(snap.deliveries).toHaveLength(n)
      expect(snap.serverTime).toBe('2026-08-20T13:48:00Z')
      expect(snap.revision).toBeNull()
      expect(snap.cursor).toBe(`fixture-cursor-${n}`)
      expect(snap.selectedOutsideResults).toBeNull()
      expect(snap.aggregates).toBeNull()
      expect(snap.aggregateUnavailableReason).toBe('missing')
      expect(snap.receivedAt).toBe(1_000)
      expect(new Set(snap.deliveries.map((d) => d.id)).size).toBe(n)
    }
  })

  it('sends the persistent selected identity and keeps an outside result out of active rows', () => {
    expect(buildSnapshotPath({ projectId: 6, selectedDelivery: ' dlv-terminal ' }))
      .toBe('/agent-mode/deliveries?project_id=6&selected_delivery=dlv-terminal')
    const active = makeFixtureDelivery(0)
    const outside = makeFixtureDelivery(1)
    const snap = normalizeWireSnapshot({
      schema_version: 1,
      cursor: 'cursor:42',
      selected_delivery: outside.delivery_id,
      rows: [active],
      selected_outside: { reason: 'terminal', row: outside },
    }, 123)
    expect(snap.deliveries.map((d) => d.id)).toEqual([active.delivery_id])
    expect(snap.selectedOutsideResults?.id).toBe(outside.delivery_id)
    expect(snap.selectedDeliveryId).toBe(outside.delivery_id)
    expect(snap.selectedOutsideReason).toBe('terminal')
    expect(snap.cursor).toBe('cursor:42')
  })

  it('rejects an outside-result object whose identity does not match the selected delivery', () => {
    const outside = makeFixtureDelivery(1)
    const snap = normalizeWireSnapshot({
      schema_version: 1,
      selected_delivery: 'dlv-requested',
      selected_outside: { reason: 'terminal', row: outside },
      rows: [],
    }, 123)
    expect(snap.selectedDeliveryId).toBe('dlv-requested')
    expect(snap.selectedOutsideResults).toBeNull()
  })

  it('preserves opaque delivery, attempt, trust, suppression, stage, evidence, handoff and capability facts', () => {
    const wire = makeFixtureDelivery(3)
    wire.estimate_suppression_codes = ['source_disagreement']
    wire.estimate_disagreement_codes = ['runner_vs_plan']
    const d = normalizeWireSnapshot({ schema_version: 1, rows: [wire] }, 123).deliveries[0]
    expect(d.attempt).toMatchObject({ id: 'attempt-815-1', number: 1, planRevision: 'plan:815:1' })
    expect(d.deliveryRevision).toBe('delivery:815:1')
    expect(d.trustRevision).toBe('trust:815:1')
    expect(d.suppressionCodes).toEqual(['source_disagreement'])
    expect(d.disagreementCodes).toEqual(['runner_vs_plan'])
    expect(d.stages).toHaveLength(5)
    expect(d.evidence.length).toBeGreaterThan(0)
    expect(d.handoffs[0]).toMatchObject({ status: 'accepted' })
    expect(d.capabilities).toMatchObject({ viewIssue: true, editIssue: true, attach: true, liveNote: false })
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
        schema_version: 1,
        rows: [
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

  it('fails closed on the removed selected_outside_results alias and both-shape ambiguity', () => {
    const outside = makeFixtureDelivery(1)
    const legacy = normalizeWireSnapshot({
      schema_version: 1,
      selected_delivery: outside.delivery_id,
      rows: [],
      selected_outside_results: outside,
    } as unknown as import('./agentModeTransport').WireSnapshot, 0)
    expect(legacy.selectedOutsideResults).toBeNull()

    const ambiguous = normalizeWireSnapshot({
      schema_version: 1,
      selected_delivery: outside.delivery_id,
      rows: [],
      selected_outside: { reason: 'terminal', row: outside },
      selected_outside_results: outside,
    } as unknown as import('./agentModeTransport').WireSnapshot, 0)
    expect(ambiguous.selectedOutsideResults).toBeNull()
  })

  it('fails aggregate parsing closed on an unsupported top-level schema or malformed/duplicate rows', () => {
    const unsupported = makeFixtureAggregateSnapshot(1)
    unsupported.schema_version = 2
    expect(normalizeWireSnapshot(unsupported, 0).aggregateUnavailableReason).toBe('unsupported-schema')

    const duplicate = makeFixtureAggregateSnapshot(1)
    duplicate.rows!.push(structuredClone(duplicate.rows![0]))
    const normalized = normalizeWireSnapshot(duplicate, 0)
    expect(normalized.deliveries).toHaveLength(1)
    expect(normalized.aggregates).toBeNull()
    expect(normalized.aggregateUnavailableReason).toBe('malformed')
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
