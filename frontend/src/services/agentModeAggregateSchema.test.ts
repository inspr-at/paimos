/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { describe, expect, it } from 'vitest'

import oneGolden from './__fixtures__/agent-mode-aggregates-v1-1.json'
import { makeFixtureAggregateSnapshot } from './agentModeFixtures'
import { parseAgentModeAggregates } from './agentModeAggregateSchema'
import { normalizeWireSnapshot } from './agentModeTransport'

interface MutableCountSet {
  active_total: number
  current_stage: Record<string, number>
  flags: Record<string, number>
  landing: Record<string, number>
}

interface MutableAggregate {
  schema_version: number
  structural_revision: string
  classification_revision: string
  calculated_at: string
  next_refresh_at: string | null
  root: MutableCountSet
  projects: Array<{
    project_id: number
    project_key: string
    project_name: string
    counts: MutableCountSet
    lanes: Array<{
      lane_key: string
      epic_id: number | null
      epic_key: string | null
      epic_title: string | null
      counts: MutableCountSet
    }>
  }>
  attention: {
    total: number
    items: Array<{
      delivery_id: string
      level: number
      primary_reason: string
      flags: string[]
      since: string | null
    }>
  }
}

function fixture(count: 1 | 10 | 100): { raw: MutableAggregate; ids: Set<string> } {
  const wire = makeFixtureAggregateSnapshot(count)
  return {
    raw: structuredClone(wire.aggregates) as MutableAggregate,
    ids: new Set((wire.rows ?? []).map((row) => String(row.delivery_id))),
  }
}

describe('PAI-804 aggregate schema-v1 golden contract (PAI-807)', () => {
  it('matches the checked-in one-delivery wire golden exactly', () => {
    expect(makeFixtureAggregateSnapshot(1).aggregates).toEqual(oneGolden)
    const parsed = parseAgentModeAggregates(oneGolden, new Set(['dlv-812']))
    expect(parsed.ok).toBe(true)
    if (parsed.ok) {
      expect(parsed.value.root.activeTotal).toBe(1)
      expect(parsed.value.root.currentStage.specification).toBe(1)
      expect(parsed.value.root.landing.within_4h).toBe(1)
    }
  })

  it('accepts deterministic 1 / 10 / 100 goldens with exact independent partitions', () => {
    for (const count of [1, 10, 100] as const) {
      const normalized = normalizeWireSnapshot(makeFixtureAggregateSnapshot(count), 123)
      expect(normalized.aggregateUnavailableReason).toBeNull()
      expect(normalized.aggregates?.root.activeTotal).toBe(count)
      expect(Object.values(normalized.aggregates!.root.currentStage).reduce((sum, value) => sum + value, 0)).toBe(count)
      expect(Object.values(normalized.aggregates!.root.landing).reduce((sum, value) => sum + value, 0)).toBe(count)
      expect(normalized.aggregates!.attention.items.length).toBeLessThanOrEqual(12)
      expect(normalized.aggregates!.attention.total).toBe(normalized.aggregates!.root.flags.attention)
    }
  })

  it('classifies missing and unsupported aggregates without fabricating zero', () => {
    expect(parseAgentModeAggregates(undefined, new Set())).toEqual({ ok: false, reason: 'missing' })
    expect(parseAgentModeAggregates({ schema_version: 2 }, new Set())).toEqual({ ok: false, reason: 'unsupported-schema' })
  })

  it('fails closed for unsafe counts, absent fields, partition drift, hierarchy drift and row-count drift', () => {
    const mutations: Array<(raw: MutableAggregate) => void> = [
      (raw) => { raw.root.active_total = -1 },
      (raw) => { raw.root.active_total = 1.5 },
      (raw) => { raw.root.active_total = Number.MAX_SAFE_INTEGER + 1 },
      (raw) => { delete raw.root.current_stage.qa },
      (raw) => { raw.root.current_stage.qa += 1 },
      (raw) => { raw.root.landing.later += 1 },
      (raw) => { raw.root.flags.deployed_unverified = raw.root.flags.unverified + 1 },
      (raw) => { raw.calculated_at = '2026-08-20' },
      (raw) => { raw.projects[0].counts.active_total += 1 },
      (raw) => { raw.projects.reverse() },
      (raw) => { raw.projects[0].lanes.reverse() },
      (raw) => {
        const ungrouped = raw.projects.flatMap((project) => project.lanes).find((lane) => lane.epic_id === null)!
        ungrouped.epic_id = 0
      },
    ]
    for (const mutate of mutations) {
      const { raw, ids } = fixture(10)
      mutate(raw)
      expect(parseAgentModeAggregates(raw, ids)).toEqual({ ok: false, reason: 'malformed' })
    }

    const { raw, ids } = fixture(10)
    ids.delete(ids.values().next().value!)
    expect(parseAgentModeAggregates(raw, ids)).toEqual({ ok: false, reason: 'malformed' })
  })

  it('rejects malformed attention references, precedence, cap, totals and duplicate identities', () => {
    const mutations: Array<(raw: MutableAggregate) => void> = [
      (raw) => { raw.attention.items[0].delivery_id = 'hidden-or-revoked' },
      (raw) => { raw.attention.items[0].flags.reverse() },
      (raw) => { raw.attention.items[0].primary_reason = 'other' },
      (raw) => { raw.attention.items[0].level = 1 },
      (raw) => { raw.attention.total += 1 },
      (raw) => { raw.attention.items.push(structuredClone(raw.attention.items[0])) },
      (raw) => { raw.attention.items.reverse() },
      (raw) => {
        raw.root.flags.blocked = 0
        for (const project of raw.projects) {
          project.counts.flags.blocked = 0
          for (const lane of project.lanes) lane.counts.flags.blocked = 0
        }
      },
    ]
    for (const mutate of mutations) {
      const { raw, ids } = fixture(100)
      mutate(raw)
      expect(parseAgentModeAggregates(raw, ids)).toEqual({ ok: false, reason: 'malformed' })
    }
  })

  it('keeps aggregates and revisions selector-independent', () => {
    const left = makeFixtureAggregateSnapshot(100)
    const right = structuredClone(left)
    right.selected_delivery = right.rows?.[55]?.delivery_id ?? null
    expect(normalizeWireSnapshot(left, 1).aggregates).toEqual(normalizeWireSnapshot(right, 2).aggregates)
  })
})
