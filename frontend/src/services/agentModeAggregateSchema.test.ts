/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import type { Delivery } from './agentMode'
import { compareIsoInstants, parseAgentModeAggregates, parseIsoInstant } from './agentModeAggregateSchema'
import { makeFixtureSnapshot } from './agentModeFixtures'
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

interface MutableSnapshot extends Record<string, unknown> {
  rows: Array<Record<string, unknown>>
  aggregates: MutableAggregate
}

const FIXTURE_ROOT = resolve(process.cwd(), '../backend/contracts/fixtures/agent-mode')

function wireFixture(count: 1 | 10 | 100): MutableSnapshot {
  return JSON.parse(readFileSync(resolve(FIXTURE_ROOT, `snapshot-v1-${count}.json`), 'utf8')) as MutableSnapshot
}

function fixture(count: 1 | 10 | 100): { raw: MutableAggregate; rows: Delivery[]; wire: MutableSnapshot } {
  const wire = wireFixture(count)
  const normalized = normalizeWireSnapshot(wire, 123)
  return {
    raw: structuredClone(wire.aggregates),
    rows: normalized.deliveries,
    wire,
  }
}

function zeroCountSet(): MutableCountSet {
  return {
    active_total: 0,
    current_stage: { specification: 0, implementation: 0, qa: 0, deployment: 0, verification: 0, unknown: 0 },
    flags: { attention: 0, waiting_needs_input: 0, blocked: 0, stale_no_signal: 0, failed_needs_retry: 0, deployed_unverified: 0, unverified: 0, unknown_reporter: 0 },
    landing: { within_4h: 0, within_24h: 0, within_3d: 0, later: 0, range_only: 0, suppressed_or_unknown: 0 },
  }
}

describe('PAI-804 aggregate schema-v1 golden contract (PAI-807)', () => {
  it('parses the backend-owned one-delivery aggregate exactly', () => {
    const { raw, rows } = fixture(1)
    const parsed = parseAgentModeAggregates(raw, rows)
    expect(parsed.ok).toBe(true)
    if (parsed.ok) {
      expect(parsed.value.root.activeTotal).toBe(1)
      expect(Object.values(parsed.value.root.currentStage).reduce((sum, value) => sum + value, 0)).toBe(1)
      expect(Object.values(parsed.value.root.landing).reduce((sum, value) => sum + value, 0)).toBe(1)
    }
  })

  it('accepts deterministic 1 / 10 / 100 goldens with exact independent partitions', () => {
    for (const count of [1, 10, 100] as const) {
      const normalized = normalizeWireSnapshot(wireFixture(count), 123)
      expect(normalized.aggregateUnavailableReason).toBeNull()
      expect(normalized.aggregates?.root.activeTotal).toBe(count)
      expect(Object.values(normalized.aggregates!.root.currentStage).reduce((sum, value) => sum + value, 0)).toBe(count)
      expect(Object.values(normalized.aggregates!.root.landing).reduce((sum, value) => sum + value, 0)).toBe(count)
      expect(normalized.aggregates!.attention.items.length).toBeLessThanOrEqual(12)
      expect(normalized.aggregates!.attention.total).toBe(normalized.aggregates!.root.flags.attention)
    }
  })

  it('accepts the 1000-row API budget and rejects the first oversized snapshot', () => {
    expect(() => normalizeWireSnapshot(makeFixtureSnapshot(1000), 123)).not.toThrow()
    expect(() => normalizeWireSnapshot(makeFixtureSnapshot(1001), 123)).toThrow()
  })

  it('classifies missing and unsupported aggregates without fabricating zero', () => {
    expect(parseAgentModeAggregates(undefined, [])).toEqual({ ok: false, reason: 'missing' })
    expect(parseAgentModeAggregates(null, [])).toEqual({ ok: false, reason: 'malformed' })
    expect(parseAgentModeAggregates({ schema_version: 2 }, [])).toEqual({ ok: false, reason: 'unsupported-schema' })
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
      (raw) => { raw.structural_revision = 'x' },
      (raw) => { raw.classification_revision = 'y' },
      (raw) => { raw.next_refresh_at = '2026-02-31T12:00:00Z' },
      (raw) => { raw.attention.items[0].since = '2025-02-29T12:00:00Z' },
      (raw) => { raw.projects[0].counts.active_total += 1 },
      (raw) => {
        const ungrouped = raw.projects.flatMap((project) => project.lanes).find((lane) => lane.epic_id === null)!
        ungrouped.epic_id = 0
      },
    ]
    for (const mutate of mutations) {
      const { raw, rows } = fixture(10)
      mutate(raw)
      expect(parseAgentModeAggregates(raw, rows)).toEqual({ ok: false, reason: 'malformed' })
    }

    const { raw, rows } = fixture(10)
    expect(parseAgentModeAggregates(raw, rows.slice(1))).toEqual({ ok: false, reason: 'malformed' })
  })

  it('rejects malformed attention references, precedence, cap, totals and duplicate identities', () => {
    const mutations: Array<(raw: MutableAggregate) => void> = [
      (raw) => { raw.attention.items[0].delivery_id = 'hidden-or-revoked' },
      (raw) => { raw.attention.items[0].flags.reverse() },
      (raw) => { raw.attention.items[0].primary_reason = 'other' },
      (raw) => { raw.attention.items[0].flags = ['other'] },
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
      const { raw, rows } = fixture(10)
      mutate(raw)
      expect(parseAgentModeAggregates(raw, rows)).toEqual({ ok: false, reason: 'malformed' })
    }
  })

  it('validates real calendar instants while accepting leap days and equivalent offsets', () => {
    expect(parseIsoInstant('2024-02-29T23:59:59.123+05:30')).toBe('2024-02-29T23:59:59.123+05:30')
    expect(parseIsoInstant('2026-08-20T15:48:00+02:00')).toBe('2026-08-20T15:48:00+02:00')
    expect(compareIsoInstants(
      '2026-08-20T12:00:00.000000001Z',
      '2026-08-20T12:00:00.000000002Z',
    )).toBe(-1)
    for (const invalid of [
      '2026-02-31T12:00:00Z',
      '2025-02-29T12:00:00Z',
      '2026-13-01T12:00:00Z',
      '2026-08-20T24:00:00Z',
      '2026-08-20T12:00:60Z',
      '2026-08-20T12:00:00+24:00',
      '2026-08-20T12:00:00.0000000001Z',
    ]) expect(parseIsoInstant(invalid)).toBeNull()

    const { raw, rows } = fixture(1)
    raw.calculated_at = '2026-02-31T12:00:00Z'
    expect(parseAgentModeAggregates(raw, rows)).toEqual({ ok: false, reason: 'malformed' })
  })

  it('rejects zero-count/amplified hierarchy controls but accepts a true zero-root snapshot', () => {
    const zero = fixture(1).raw
    zero.root = zeroCountSet()
    zero.projects = []
    zero.attention = { total: 0, items: [] }
    expect(parseAgentModeAggregates(zero, [])).toMatchObject({ ok: true })

    const zeroProject = fixture(1)
    zeroProject.raw.projects.push({
      project_id: 99,
      project_key: 'ZERO',
      project_name: 'Zero project',
      counts: zeroCountSet(),
      lanes: [],
    })
    expect(parseAgentModeAggregates(zeroProject.raw, zeroProject.rows)).toEqual({ ok: false, reason: 'malformed' })

    const zeroLane = fixture(10)
    const project = zeroLane.raw.projects[0]
    project.lanes.unshift({
      lane_key: `project:${project.project_id}/epic:1`,
      epic_id: 1,
      epic_key: 'ZERO-1',
      epic_title: 'Zero lane',
      counts: zeroCountSet(),
    })
    expect(parseAgentModeAggregates(zeroLane.raw, zeroLane.rows)).toEqual({ ok: false, reason: 'malformed' })

    const emptyProject = fixture(10)
    emptyProject.raw.projects[0].lanes = []
    expect(parseAgentModeAggregates(emptyProject.raw, emptyProject.rows)).toEqual({ ok: false, reason: 'malformed' })

    const amplified = fixture(1)
    amplified.raw.projects.push(...Array.from({ length: 1_000 }, (_, index) => ({
      project_id: 100 + index,
      project_key: `ZERO-${index}`,
      project_name: `Zero project ${index}`,
      counts: zeroCountSet(),
      lanes: [],
    })))
    expect(parseAgentModeAggregates(amplified.raw, amplified.rows)).toEqual({ ok: false, reason: 'malformed' })
  })

  it('cross-checks every project/lane identity and positive membership against normalized active rows', () => {
    for (const mutate of [
      (rows: Delivery[]) => { rows[0].lane.projectName = 'Unrelated project label' },
      (rows: Delivery[]) => { rows[0].lane.projectId = 999 },
      (rows: Delivery[]) => { rows[0].lane.key = 'project:999/ungrouped' },
      (rows: Delivery[]) => { rows[0].lane.epicTitle = 'Revoked epic label' },
      (rows: Delivery[]) => { rows.find((row) => row.attention.level > 0)!.attention.reason = 'different' },
      (rows: Delivery[]) => { rows.find((row) => row.attention.level > 0)!.attention.since = '2026-08-20T00:00:00Z' },
    ]) {
      const { raw, rows } = fixture(10)
      mutate(rows)
      expect(parseAgentModeAggregates(raw, rows)).toEqual({ ok: false, reason: 'malformed' })
    }
  })

  it('keeps aggregates and revisions selector-independent', () => {
    const left = wireFixture(100)
    const right = structuredClone(left)
    right.selected_delivery = right.rows[55].delivery_id
    delete right.selected_outside
    expect(normalizeWireSnapshot(left, 1).aggregates).toEqual(normalizeWireSnapshot(right, 2).aggregates)
  })
})
