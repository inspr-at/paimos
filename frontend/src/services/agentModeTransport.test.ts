/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { createHash } from 'node:crypto'
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import { estimatePresentation } from '@/composables/agent-mode/agentModeTrust'
import { buildAgentModeEventsURL } from './agentModeEvents'
import {
  AGENT_MODE_LANE_KEY_MAX_LENGTH,
  AGENT_MODE_SEARCH_MAX_BYTES,
  AgentModeQueryContractError,
  buildSnapshotPath,
  normalizeWireDelivery,
  normalizeWireSnapshot,
  parseAgentModeDeliveryKey,
  parseAgentModeLaneFilter,
  parseAgentModeProjectFilter,
  parseAgentModeSearchFilter,
  trimAgentModeSpace,
} from './agentModeTransport'

interface MutableSnapshot extends Record<string, unknown> {
  rows: Array<Record<string, unknown>>
}

interface FixtureManifest {
  schema_version: number
  contract: string
  media_type: string
  encoding: string
  generator_version: number
  fixtures: Array<{ file: string; rows: number; bytes: number; sha256: string }>
}

const FIXTURE_ROOT = resolve(process.cwd(), '../backend/contracts/fixtures/agent-mode')

function fixtureBytes(file: string): Buffer {
  return readFileSync(resolve(FIXTURE_ROOT, file))
}

function fixture(count: 1 | 10 | 100): MutableSnapshot {
  return JSON.parse(fixtureBytes(`snapshot-v1-${count}.json`).toString('utf8')) as MutableSnapshot
}

function expectSnapshotFailure(value: unknown) {
  expect(() => normalizeWireSnapshot(value, 123)).toThrowError(/Agent Mode snapshot schema/)
}

describe('agentModeTransport — frozen PAI-804 schema v1', () => {
  it('pins the backend fixture manifest bytes, digests, and declared cardinalities', () => {
    const manifest = JSON.parse(fixtureBytes('manifest-v1.json').toString('utf8')) as FixtureManifest
    expect(manifest).toMatchObject({
      schema_version: 1,
      contract: 'agent-mode.snapshot.v1',
      media_type: 'application/json',
      encoding: 'utf-8-json-lf',
      generator_version: 1,
    })
    expect(manifest.fixtures.map((entry) => entry.rows)).toEqual([1, 10, 100])
    const declared = manifest.fixtures.map((entry) => entry.file)
    expect(new Set(declared).size).toBe(declared.length)
    expect(readdirSync(FIXTURE_ROOT).sort()).toEqual(['manifest-v1.json', ...declared].sort())
    for (const entry of manifest.fixtures) {
      const raw = fixtureBytes(entry.file)
      expect(raw.byteLength, entry.file).toBe(entry.bytes)
      expect(createHash('sha256').update(raw).digest('hex'), entry.file).toBe(entry.sha256)
      const payload = JSON.parse(raw.toString('utf8')) as MutableSnapshot
      expect(payload.rows, entry.file).toHaveLength(entry.rows)
    }
  })

  it('accepts the exact frozen 1 / 10 / 100 snapshots without dropping rows', () => {
    for (const count of [1, 10, 100] as const) {
      const snapshot = normalizeWireSnapshot(fixture(count), 1_000 + count)
      expect(snapshot.deliveries).toHaveLength(count)
      expect(snapshot.cursor).toMatch(/^[A-Za-z0-9_-]{211}$/)
      expect(snapshot.serverTime).toBe('2026-08-20T12:00:00Z')
      expect([
        ...snapshot.deliveries.map((row) => row.id),
        snapshot.selectedOutsideResults?.id,
      ]).toContain(snapshot.selectedDeliveryId)
      expect(snapshot.selectedOutsideResults == null).toBe(count === 1)
      expect(snapshot.aggregates?.root.activeTotal).toBe(count)
      expect(snapshot.aggregateUnavailableReason).toBeNull()
      expect(snapshot.receivedAt).toBe(1_000 + count)
      expect(new Set(snapshot.deliveries.map((row) => row.id)).size).toBe(count)
    }
  })

  it('builds one canonical closed query without silently broadening invalid input', () => {
    expect(buildSnapshotPath()).toBe('/agent-mode/deliveries')
    expect(buildSnapshotPath({
      projectId: 6,
      laneKey: 'project:6/epic:4655',
      states: ['pending', 'active', 'pending'],
      attention: 'required',
      health: 'stale',
      q: ' release ',
      selectedDelivery: 'issue:42',
    })).toBe('/agent-mode/deliveries?project_id=6&lane_key=project%3A6%2Fepic%3A4655&state=active&state=pending&attention=required&health=stale&q=release&selected_delivery=issue%3A42')

    const exactly160Bytes = 'é'.repeat(AGENT_MODE_SEARCH_MAX_BYTES / 2)
    expect(new TextEncoder().encode(exactly160Bytes)).toHaveLength(160)
    expect(parseAgentModeSearchFilter(exactly160Bytes)).toBe(exactly160Bytes)
    expect(parseAgentModeSearchFilter(`${exactly160Bytes}é`)).toBeNull()
    expect(() => buildSnapshotPath({ q: `${exactly160Bytes}é` })).toThrow(AgentModeQueryContractError)
    expect(() => buildSnapshotPath({ q: '\u0000secret' })).toThrow(AgentModeQueryContractError)
    expect(() => buildSnapshotPath({ states: ['active', 'legacy'] as never })).toThrow(AgentModeQueryContractError)
    expect(() => buildSnapshotPath({ attention: 'maybe' as never })).toThrow(AgentModeQueryContractError)
    expect(() => buildSnapshotPath({ project_id: 6 } as never)).toThrow(AgentModeQueryContractError)
    expect(buildSnapshotPath({ selectedDelivery: ' issue:42 ' }))
      .toBe('/agent-mode/deliveries?selected_delivery=issue%3A42')
    expect(trimAgentModeSpace('\u0085\u00a0issue:42\u3000')).toBe('issue:42')
    expect(buildSnapshotPath({ q: '\u0085\u00a0ship\u3000', selectedDelivery: '\u0085issue:42\u0085' }))
      .toBe('/agent-mode/deliveries?q=ship&selected_delivery=issue%3A42')
    expect(buildSnapshotPath({ q: '\uFEFF' })).toBe('/agent-mode/deliveries?q=%EF%BB%BF')
    expect(() => buildSnapshotPath({ selectedDelivery: '\uFEFF' })).toThrow(AgentModeQueryContractError)
  })

  it('accepts only exact immutable project, lane, and delivery identities', () => {
    expect(parseAgentModeProjectFilter('6')).toBe(6)
    expect(parseAgentModeProjectFilter(6)).toBe(6)
    expect(parseAgentModeLaneFilter('project:6/epic:4655')).toBe('project:6/epic:4655')
    expect(parseAgentModeLaneFilter('project:6/ungrouped')).toBe('project:6/ungrouped')
    expect(parseAgentModeDeliveryKey('issue:42')).toBe('issue:42')
    for (const invalid of ['6.5', '06', '0', '-1', String(Number.MAX_SAFE_INTEGER + 1)]) {
      expect(parseAgentModeProjectFilter(invalid)).toBeNull()
    }
    for (const invalid of [
      ' project:6/epic:4655',
      'project:6/epic:4655 ',
      'arbitrary-lane',
      'project:6.5/ungrouped',
      'project:6/epic:0',
      `project:6/epic:${'1'.repeat(AGENT_MODE_LANE_KEY_MAX_LENGTH)}`,
    ]) expect(parseAgentModeLaneFilter(invalid)).toBeNull()
    for (const invalid of ['', '#42', 'x'.repeat(129)]) {
      expect(parseAgentModeDeliveryKey(invalid)).toBeNull()
    }
    expect(parseAgentModeDeliveryKey(' issue:42 ')).toBe('issue:42')
  })

  it('projects only the privacy-reviewed row facts', () => {
    const wire = fixture(1).rows[0]
    const row = normalizeWireDelivery(wire)
    expect(row).not.toBeNull()
    expect(row).toMatchObject({
      id: 'issue:1',
      issueId: 1,
      deliveryRevision: 'delivery:1:6:6',
      actor: { name: 'external', label: 'External reporter', kind: 'system' },
      handoffs: [],
    })
    expect(row?.trust.trustRevision).toBe(row?.trustRevision)
    expect(row?.trust.scope).toMatchObject({ attemptId: 'attempt:1', planId: 'plan:1:1' })
    expect(row?.evidence[0]).toMatchObject({ id: null, reporter: null, kind: 'spec_acceptance' })
  })

  it('accepts backend-valid empty public labels while keeping structural identities exact', () => {
    const snapshot = fixture(1)
    const row = snapshot.rows[0]
    const aggregate = snapshot.aggregates as {
      projects: Array<{
        project_key: string
        project_name: string
        lanes: Array<Record<string, unknown>>
      }>
    }
    row.issue_key = ''
    row.title = ' '
    row.project_key = ''
    row.project_name = ' '
    row.delivery_revision = ''
    row.attempt_id = ''
    row.plan_revision = ''
    row.tags = ['']
    ;(row.actor as Record<string, unknown>).name = ''
    ;(row.actor as Record<string, unknown>).label = ' '
    ;((row.evidence as Array<Record<string, unknown>>)[0]).kind = ''
    const stages = row.stages as Array<Record<string, unknown>>
    for (const stage of stages) stage.label = ''
    ;(row.stage as Record<string, unknown>).label = ''
    aggregate.projects[0].project_key = ''
    aggregate.projects[0].project_name = ' '

    const normalized = normalizeWireSnapshot(snapshot, 123)
    expect(normalized.deliveries[0]).toMatchObject({
      issueKey: '',
      title: ' ',
      deliveryRevision: '',
      lane: { projectKey: '', projectName: ' ' },
      actor: { name: '', label: ' ' },
      tags: [''],
    })
    expect(normalized.aggregates?.projects[0]).toMatchObject({ projectKey: '', projectName: ' ' })

    const epicSnapshot = fixture(1)
    const epicRow = epicSnapshot.rows[0]
    const epicAggregate = epicSnapshot.aggregates as { projects: Array<{ lanes: Array<Record<string, unknown>> }> }
    epicRow.epic_id = 7
    epicRow.epic_key = ''
    epicRow.epic_title = ''
    epicRow.lane_key = 'project:1/epic:7'
    Object.assign(epicAggregate.projects[0].lanes[0], {
      lane_key: 'project:1/epic:7',
      epic_id: 7,
      epic_key: '',
      epic_title: '',
    })
    expect(normalizeWireSnapshot(epicSnapshot, 123).deliveries[0].lane).toMatchObject({
      epicId: 7,
      epicKey: '',
      epicTitle: '',
    })
  })

  it('rejects legacy aliases, mock-only identities, and unsafe nested extras', () => {
    const mutations: Array<(snapshot: MutableSnapshot) => void> = [
      (snapshot) => { snapshot.selected_outside_results = snapshot.rows[0] },
      (snapshot) => { snapshot.rows[0].handoffs = [] },
      (snapshot) => { snapshot.rows[0].estimate_suppression_codes = ['stale'] },
      (snapshot) => { snapshot.rows[0].estimate_disagreement_codes = ['secret'] },
      (snapshot) => {
        const evidence = snapshot.rows[0].evidence as Array<Record<string, unknown>>
        evidence[0].evidence_id = 'secret-evidence-id'
      },
      (snapshot) => {
        const evidence = snapshot.rows[0].evidence as Array<Record<string, unknown>>
        evidence[0].reporter = { name: 'secret-reporter' }
      },
      (snapshot) => {
        const trust = snapshot.rows[0].trust as Record<string, unknown>
        trust.provider = 'secret-provider'
      },
      (snapshot) => {
        const attention = snapshot.rows[0].attention as Record<string, unknown>
        attention.reason = 'not_in_openapi'
      },
      (snapshot) => { snapshot.secret_canary = 'must-not-survive' },
    ]
    for (const mutate of mutations) {
      const snapshot = fixture(1)
      mutate(snapshot)
      expectSnapshotFailure(snapshot)
      try {
        normalizeWireSnapshot(snapshot, 123)
      } catch (error) {
        expect(String(error)).not.toContain('secret-')
      }
    }
  })

  it('rejects control text anywhere in the response without echoing it', () => {
    for (const mutate of [
      (snapshot: MutableSnapshot) => { snapshot.rows[0].title = 'unsafe\u0000title' },
      (snapshot: MutableSnapshot) => {
        ;(snapshot.rows[0].activity as Record<string, unknown>).text = 'unsafe\nactivity'
      },
      (snapshot: MutableSnapshot) => {
        ;(snapshot.rows[0].trust as Record<string, unknown>).basis = 'unsafe\u0085basis'
      },
    ]) {
      const snapshot = fixture(1)
      mutate(snapshot)
      expectSnapshotFailure(snapshot)
      try {
        normalizeWireSnapshot(snapshot, 123)
      } catch (error) {
        expect(String(error)).not.toContain('unsafe')
      }
    }
  })

  it('fails the whole load for malformed shell, cursor, row, or selection identity', () => {
    const invalidCursor = fixture(1)
    invalidCursor.cursor = 'legacy-cursor'
    expectSnapshotFailure(invalidCursor)

    const trailingBitAlias = fixture(1)
    trailingBitAlias.cursor = `${String(trailingBitAlias.cursor).slice(0, -1)}5`
    expectSnapshotFailure(trailingBitAlias)

    const overPreciseClock = fixture(1)
    overPreciseClock.server_time = '2026-08-20T12:00:00.0000000001Z'
    expectSnapshotFailure(overPreciseClock)

    const nanosecondMismatch = fixture(1)
    nanosecondMismatch.server_time = '2026-08-20T12:00:00.000000001Z'
    ;(nanosecondMismatch.aggregates as Record<string, unknown>).calculated_at = nanosecondMismatch.server_time
    ;(nanosecondMismatch.rows[0].eta as Record<string, unknown>).calculated_at = '2026-08-20T12:00:00.000000002Z'
    expectSnapshotFailure(nanosecondMismatch)

    const duplicate = fixture(1)
    duplicate.rows.push(structuredClone(duplicate.rows[0]))
    expectSnapshotFailure(duplicate)

    const mismatch = fixture(10)
    mismatch.selected_delivery = 'issue:999'
    expectSnapshotFailure(mismatch)

    const outside = fixture(10)
    const outsideRow = structuredClone(outside.rows[1])
    outside.rows = [outside.rows[0]]
    outside.selected_delivery = 'issue:999'
    outside.selected_outside = { reason: 'terminal', row: outsideRow }
    expectSnapshotFailure(outside)
  })

  it('rejects every same-byte RawURL terminal alias at snapshot and events boundaries', () => {
    const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
    for (let index = 0; index < alphabet.length; index += 4) {
      const canonical = `${'A'.repeat(210)}${alphabet[index]}`
      const valid = fixture(1)
      valid.cursor = canonical
      expect(normalizeWireSnapshot(valid, 123).cursor).toBe(canonical)
      expect(buildAgentModeEventsURL({}, canonical)).toContain(`cursor=${canonical}`)
      for (let aliasOffset = 1; aliasOffset <= 3; aliasOffset += 1) {
        const alias = `${'A'.repeat(210)}${alphabet[index + aliasOffset]}`
        const invalid = fixture(1)
        invalid.cursor = alias
        expectSnapshotFailure(invalid)
        expect(() => buildAgentModeEventsURL({}, alias)).toThrow(/cursor/)
      }
    }
  })

  it('keeps valid rows but explicitly marks missing or malformed aggregates unavailable', () => {
    const missing = fixture(10)
    delete missing.aggregates
    const missingResult = normalizeWireSnapshot(missing, 123)
    expect(missingResult.deliveries).toHaveLength(10)
    expect(missingResult.aggregates).toBeNull()
    expect(missingResult.aggregateUnavailableReason).toBe('missing')

    const explicitNull = fixture(10)
    explicitNull.aggregates = null
    expect(normalizeWireSnapshot(explicitNull, 123).aggregateUnavailableReason).toBe('malformed')

    const malformed = fixture(10)
    const aggregates = malformed.aggregates as Record<string, unknown>
    aggregates.secret = true
    const malformedResult = normalizeWireSnapshot(malformed, 123)
    expect(malformedResult.deliveries).toHaveLength(10)
    expect(malformedResult.aggregates).toBeNull()
    expect(malformedResult.aggregateUnavailableReason).toBe('malformed')
  })

  it('uses trust.suppression as the authoritative precision gate', () => {
    const snapshot = fixture(1)
    const wire = snapshot.rows[0]
    const trust = wire.trust as Record<string, unknown>
    const eta = wire.eta as Record<string, unknown>
    trust.suppression = 'stale'
    eta.trusted = false

    const row = normalizeWireSnapshot(snapshot, 123).deliveries[0]
    expect(row.progress).toMatchObject({ percent: 46, trusted: true, confidence: 'high' })
    expect(row.suppressionCodes).toEqual(['stale'])
    expect(estimatePresentation(row)).toMatchObject({
      showPercent: false,
      percent: null,
      showEta: false,
      landingAt: null,
      percentReason: 'suppressed',
      etaReason: 'suppressed',
    })
  })

  it('accepts backend-valid stage-evidence progress with owner/history ETA trust', () => {
    for (const sourceKind of ['owner_estimate', 'history'] as const) {
      const snapshot = fixture(1)
      const row = snapshot.rows[0]
      const trust = row.trust as Record<string, unknown>
      const progress = row.progress as Record<string, unknown>
      trust.source_kind = sourceKind
      progress.source = 'stage_evidence'
      expect(normalizeWireSnapshot(snapshot, 123).deliveries[0].trust.sourceKind).toBe(sourceKind)
    }

    const invalid = fixture(1)
    const progress = invalid.rows[0].progress as Record<string, unknown>
    progress.source = 'legacy_source'
    expectSnapshotFailure(invalid)
  })
})
