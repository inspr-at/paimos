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

// PAI-805 — deterministic Agent Mode fixtures (wire-shaped).
//
// Used by unit tests and by the DEV-only `/dev/agent-mode` reference
// route. NOT imported by any production code path: the runtime never
// falls back to fabricated deliveries. Everything here is derived from a
// fixed base clock so snapshots and ordering are reproducible.

import { laneKeyFor, type WireDelivery, type WireSnapshot } from './agentModeTransport'

export const FIXTURE_BASE_TIME = '2026-08-20T13:48:00Z'
const BASE_MS = Date.parse(FIXTURE_BASE_TIME)

function iso(offsetMinutes: number): string {
  return new Date(BASE_MS + offsetMinutes * 60_000).toISOString()
}

interface FixtureProject {
  id: number
  key: string
  name: string
  epics: Array<{ id: number; key: string; title: string }>
}

const PROJECTS: FixtureProject[] = [
  {
    id: 6,
    key: 'PAI',
    name: 'PAIMOS Core platform',
    epics: [
      { id: 4655, key: 'PAI-801', title: 'Agent Mode' },
      { id: 4601, key: 'PAI-760', title: 'Access & trust' },
    ],
  },
  {
    id: 9,
    key: 'RUN',
    name: 'Agent runtime',
    epics: [{ id: 9001, key: 'RUN-40', title: 'Runner reliability' }],
  },
  {
    id: 12,
    key: 'REL',
    name: 'Release operations',
    epics: [],
  },
]

const ACTORS = [
  { name: 'codex', label: 'Codex', kind: 'agent' },
  { name: 'claude', label: 'Claude', kind: 'agent' },
  { name: 'janus', label: 'Janus', kind: 'system' },
  { name: 'pharos', label: 'Pharos', kind: 'system' },
] as const

const STAGES = ['specification', 'implementation', 'qa', 'deployment', 'verification'] as const

interface Variant {
  activityKind: string
  activity: string
  health: string
  attention: number
  attentionReason: string | null
  freshness: string
  lastReportMin: number
  progress: { percent: number; trusted: boolean; confidence: string } | null
  eta: { landMin: number; trusted: boolean; confidence: string } | null
  blockers?: string[]
  statusText?: string
}

// Ten canonical variants — enough to cover every card state in detail 10.
const VARIANTS: Variant[] = [
  { activityKind: 'working', activity: 'Writing membership checks', health: 'healthy', attention: 0, attentionReason: null, freshness: 'fresh', lastReportMin: -1, progress: { percent: 64, trusted: true, confidence: 'high' }, eta: { landMin: 52, trusted: true, confidence: 'high' } },
  { activityKind: 'testing', activity: 'Scoring 240 retrieval fixtures', health: 'healthy', attention: 0, attentionReason: null, freshness: 'fresh', lastReportMin: -2, progress: { percent: 82, trusted: true, confidence: 'medium' }, eta: { landMin: 30, trusted: true, confidence: 'medium' } },
  { activityKind: 'waiting', activity: 'Waiting for retention decision', health: 'attention', attention: 2, attentionReason: 'Needs the retention period before the specification can close', freshness: 'fresh', lastReportMin: -12, progress: { percent: 19, trusted: true, confidence: 'high' }, eta: { landMin: 1112, trusted: true, confidence: 'low' }, statusText: 'Decision requested 12 min ago' },
  { activityKind: 'blocked', activity: 'Investigating 3 failed assertions', health: 'blocked', attention: 3, attentionReason: 'Permissions fixture failure blocks this delivery', freshness: 'fresh', lastReportMin: -3, progress: { percent: 69, trusted: true, confidence: 'high' }, eta: { landMin: 67, trusted: true, confidence: 'high' }, blockers: ['permissions fixture fails on case 84'] },
  { activityKind: 'deploying', activity: 'Deploying region 3 of 5', health: 'healthy', attention: 0, attentionReason: null, freshness: 'fresh', lastReportMin: 0, progress: { percent: 91, trusted: true, confidence: 'high' }, eta: { landMin: 14, trusted: true, confidence: 'high' } },
  { activityKind: 'working', activity: 'Profiling onboarding trace', health: 'healthy', attention: 0, attentionReason: null, freshness: 'aging', lastReportMin: -9, progress: { percent: 58, trusted: false, confidence: 'low' }, eta: { landMin: 78, trusted: false, confidence: 'low' } },
  { activityKind: 'testing', activity: 'Running case 84 of 112', health: 'healthy', attention: 0, attentionReason: null, freshness: 'fresh', lastReportMin: -1, progress: { percent: 94, trusted: true, confidence: 'high' }, eta: { landMin: 10, trusted: true, confidence: 'high' } },
  { activityKind: 'unknown', activity: '', health: 'unknown', attention: 1, attentionReason: 'No report for 41 minutes', freshness: 'stale', lastReportMin: -41, progress: { percent: 47, trusted: true, confidence: 'high' }, eta: { landMin: 97, trusted: true, confidence: 'high' } },
  { activityKind: 'verifying', activity: 'Smoke-testing the production release', health: 'at_risk', attention: 1, attentionReason: 'Latency threshold exceeded during verification', freshness: 'fresh', lastReportMin: -1, progress: { percent: 96, trusted: true, confidence: 'medium' }, eta: { landMin: 37, trusted: true, confidence: 'medium' } },
  { activityKind: 'working', activity: 'Reproducing reconnect drop', health: 'healthy', attention: 0, attentionReason: null, freshness: 'fresh', lastReportMin: -4, progress: null, eta: null },
]

const TITLES = [
  'Workspace-level access controls',
  'Knowledge retrieval evaluation',
  'Run lifecycle audit log',
  'Permissions regression pack',
  'Production release 5.11.0',
  'Project onboarding latency',
  'Agent artifact smoke suite',
  'Anchor drift diagnostics',
  'Release 5.11.0 smoke suite',
  'Runner reconnect reliability',
]

export function makeFixtureDelivery(index: number): WireDelivery {
  const project = PROJECTS[index % PROJECTS.length]
  // Every fourth delivery lands in the explicit Ungrouped lane (even in
  // projects that have epics) so the lane contract is exercised by every
  // fixture size.
  const epic = project.epics.length > 0 && index % 4 !== 3
    ? project.epics[Math.floor(index / PROJECTS.length) % project.epics.length]
    : null
  const actor = ACTORS[index % ACTORS.length]
  const variant = VARIANTS[index % VARIANTS.length]
  const stageIndex = index % STAGES.length
  const issueNumber = 812 + index
  const stageRows = STAGES.map((key, i) => ({
    key,
    status: i < stageIndex ? 'succeeded' : i === stageIndex ? variant.activityKind === 'blocked' ? 'blocked' : 'active' : 'pending',
    required: true,
    owner: { ...ACTORS[(index + i) % ACTORS.length] },
    activity: i === stageIndex ? variant.activity : null,
    evidence: i < stageIndex
      ? [{ evidence_id: `ev-${issueNumber}-${i + 1}`, kind: 'stage_result', label: `${key} evidence`, status: 'accepted', reported_at: iso(-30 + i) }]
      : [],
  }))
  return {
    delivery_id: `dlv-${issueNumber}`,
    issue_id: 5000 + index,
    issue_key: `${project.key}-${issueNumber}`,
    title: index < TITLES.length ? TITLES[index] : `${TITLES[index % TITLES.length]} ${Math.floor(index / TITLES.length) + 1}`,
    project_id: project.id,
    project_key: project.key,
    project_name: project.name,
    epic_id: epic?.id ?? null,
    epic_key: epic?.key ?? null,
    epic_title: epic?.title ?? null,
    attempt_id: `attempt-${issueNumber}-1`,
    attempt_number: 1,
    attempt_status: variant.activityKind === 'blocked' ? 'blocked' : 'active',
    plan_revision: `plan:${issueNumber}:1`,
    delivery_revision: `delivery:${issueNumber}:1`,
    trust_revision: `trust:${issueNumber}:1`,
    tags: index % 4 === 0 ? ['security'] : [],
    actor: { ...actor },
    activity: {
      kind: variant.activityKind,
      text: variant.activity || null,
      since: iso(-15 - (index % 7) * 4),
    },
    stage: { key: STAGES[stageIndex], index: stageIndex + 1, total: STAGES.length },
    stages: stageRows,
    evidence: stageRows.flatMap((stage) => stage.evidence),
    handoffs: stageIndex > 0
      ? [{
          handoff_id: `handoff-${issueNumber}-${stageIndex}`,
          from: { ...ACTORS[(index + stageIndex - 1) % ACTORS.length] },
          to: { ...ACTORS[(index + stageIndex) % ACTORS.length] },
          status: 'accepted',
          summary: `Accepted ${STAGES[stageIndex]} ownership`,
          reported_at: iso(-20),
        }]
      : [],
    capabilities: {
      view_issue: true,
      edit_issue: true,
      comment: true,
      attach: true,
      live_note: false,
      one_shot_run_active: index % 3 === 0,
    },
    health: variant.health,
    attention: { level: variant.attention, reason: variant.attentionReason, since: variant.attention ? iso(-12) : null },
    freshness: { state: variant.freshness, last_report_at: iso(variant.lastReportMin) },
    blockers: (variant.blockers ?? []).map((text) => ({ kind: 'dependency', text })),
    progress: variant.progress
      ? { percent: variant.progress.percent, trusted: variant.progress.trusted, confidence: variant.progress.confidence, source: 'stage_weights', basis: 'stage evidence', revision: 1 }
      : null,
    eta: variant.eta
      ? {
          landing_at: iso(variant.eta.landMin),
          optimistic_at: iso(Math.max(1, variant.eta.landMin - 8)),
          pessimistic_at: iso(variant.eta.landMin + 15),
          trusted: variant.eta.trusted,
          confidence: variant.eta.confidence,
          basis: 'history n=14',
          calculated_at: iso(0),
        }
      : null,
    status_text: variant.statusText ?? null,
    updated_at: iso(variant.lastReportMin),
  }
}

export function makeFixtureSnapshot(count: 1 | 10 | 100 | number, serverTime = FIXTURE_BASE_TIME): WireSnapshot {
  return {
    schema_version: 1,
    server_time: serverTime,
    cursor: `fixture-cursor-${count}`,
    rows: Array.from({ length: count }, (_, i) => makeFixtureDelivery(i)),
  }
}

type WireCountSet = {
  active_total: number
  current_stage: Record<string, number>
  flags: Record<string, number>
  landing: Record<string, number>
}

const AGGREGATE_STAGES = ['specification', 'implementation', 'qa', 'deployment', 'verification', 'unknown'] as const
const AGGREGATE_FLAGS = [
  'attention', 'waiting_needs_input', 'blocked', 'stale_no_signal', 'failed_needs_retry',
  'deployed_unverified', 'unverified', 'unknown_reporter',
] as const
const AGGREGATE_LANDING = ['within_4h', 'within_24h', 'within_3d', 'later', 'range_only', 'suppressed_or_unknown'] as const
const ATTENTION_REASONS = [
  'blocked', 'waiting_needs_input', 'failed_needs_retry', 'stale_no_signal',
  'unknown_reporter', 'deployed_unverified', 'unverified', 'other',
] as const

function emptyCountSet(): WireCountSet {
  return {
    active_total: 0,
    current_stage: Object.fromEntries(AGGREGATE_STAGES.map((key) => [key, 0])),
    flags: Object.fromEntries(AGGREGATE_FLAGS.map((key) => [key, 0])),
    landing: Object.fromEntries(AGGREGATE_LANDING.map((key) => [key, 0])),
  }
}

function aggregateReasonFlags(row: WireDelivery): Array<(typeof ATTENTION_REASONS)[number]> {
  const flags: Array<(typeof ATTENTION_REASONS)[number]> = []
  if (row.health === 'blocked' || row.activity?.kind === 'blocked' || (row.blockers?.length ?? 0) > 0) flags.push('blocked')
  if (row.activity?.kind === 'waiting') flags.push('waiting_needs_input')
  if (row.attempt_status === 'failed') flags.push('failed_needs_retry')
  if (row.freshness?.state === 'stale' || row.freshness?.state === 'unknown') flags.push('stale_no_signal')
  if (!row.actor?.name) flags.push('unknown_reporter')
  if (row.stage?.key === 'deployment') flags.push('deployed_unverified')
  if (row.stage?.key === 'deployment' || row.stage?.key === 'verification') flags.push('unverified')
  if ((row.attention?.level ?? 0) > 0 && !flags.some((flag) => flag !== 'unverified')) flags.push('other')
  return flags
}

function addRow(counts: WireCountSet, row: WireDelivery, calculatedMs: number) {
  counts.active_total += 1
  const stage = AGGREGATE_STAGES.includes(row.stage?.key as (typeof AGGREGATE_STAGES)[number])
    ? row.stage!.key!
    : 'unknown'
  counts.current_stage[stage] += 1

  const reasons = aggregateReasonFlags(row)
  if ((row.attention?.level ?? 0) > 0) counts.flags.attention += 1
  if (reasons.includes('waiting_needs_input')) counts.flags.waiting_needs_input += 1
  if (reasons.includes('blocked')) counts.flags.blocked += 1
  if (reasons.includes('stale_no_signal')) counts.flags.stale_no_signal += 1
  if (reasons.includes('failed_needs_retry')) counts.flags.failed_needs_retry += 1
  if (reasons.includes('deployed_unverified')) counts.flags.deployed_unverified += 1
  if (reasons.includes('unverified')) counts.flags.unverified += 1
  if (reasons.includes('unknown_reporter')) counts.flags.unknown_reporter += 1

  const eta = row.eta
  if (!eta || eta.trusted !== true || !eta.landing_at) {
    counts.landing.suppressed_or_unknown += 1
  } else if (eta.confidence === 'low') {
    counts.landing.range_only += 1
  } else {
    const remaining = Date.parse(eta.landing_at) - calculatedMs
    if (remaining <= 4 * 60 * 60_000) counts.landing.within_4h += 1
    else if (remaining <= 24 * 60 * 60_000) counts.landing.within_24h += 1
    else if (remaining <= 3 * 24 * 60 * 60_000) counts.landing.within_3d += 1
    else counts.landing.later += 1
  }
}

function addCountSet(target: WireCountSet, source: WireCountSet) {
  target.active_total += source.active_total
  for (const key of AGGREGATE_STAGES) target.current_stage[key] += source.current_stage[key]
  for (const key of AGGREGATE_FLAGS) target.flags[key] += source.flags[key]
  for (const key of AGGREGATE_LANDING) target.landing[key] += source.landing[key]
}

/** Deterministic strict schema-v1 fixture. Aggregate calculation lives only
 * in this test/DEV module; production never imports or reconstructs it. */
export function makeFixtureAggregateSnapshot(count: 1 | 10 | 100 | number, serverTime = FIXTURE_BASE_TIME): WireSnapshot {
  const snapshot = makeFixtureSnapshot(count, serverTime)
  const rows = snapshot.rows ?? []
  const calculatedMs = Date.parse(serverTime)
  const grouped = new Map<number, { key: string; name: string; lanes: Map<string, WireDelivery[]> }>()
  for (const row of rows) {
    const projectId = row.project_id!
    const project = grouped.get(projectId) ?? {
      key: row.project_key!,
      name: row.project_name!,
      lanes: new Map<string, WireDelivery[]>(),
    }
    const laneKey = laneKeyFor(projectId, row.epic_id ?? null)
    project.lanes.set(laneKey, [...(project.lanes.get(laneKey) ?? []), row])
    grouped.set(projectId, project)
  }

  const root = emptyCountSet()
  const projects = [...grouped.entries()]
    .sort(([left], [right]) => left - right)
    .map(([projectId, project]) => {
      const projectCounts = emptyCountSet()
      const lanes = [...project.lanes.entries()]
        .sort(([, leftRows], [, rightRows]) => {
          const leftEpic = leftRows[0].epic_id ?? null
          const rightEpic = rightRows[0].epic_id ?? null
          if (leftEpic == null && rightEpic != null) return 1
          if (leftEpic != null && rightEpic == null) return -1
          if (leftEpic != null && rightEpic != null && leftEpic !== rightEpic) return leftEpic - rightEpic
          return laneKeyFor(projectId, leftEpic) < laneKeyFor(projectId, rightEpic) ? -1 : 1
        })
        .map(([laneKey, laneRows]) => {
          const counts = emptyCountSet()
          for (const row of laneRows) addRow(counts, row, calculatedMs)
          addCountSet(projectCounts, counts)
          const first = laneRows[0]
          return {
            lane_key: laneKey,
            epic_id: first.epic_id ?? null,
            epic_key: first.epic_id == null ? null : first.epic_key ?? null,
            epic_title: first.epic_id == null ? null : first.epic_title ?? null,
            counts,
          }
        })
      addCountSet(root, projectCounts)
      return {
        project_id: projectId,
        project_key: project.key,
        project_name: project.name,
        counts: projectCounts,
        lanes,
      }
    })

  const attentionItems = rows
    .filter((row) => (row.attention?.level ?? 0) > 0)
    .map((row) => {
      const flags = aggregateReasonFlags(row)
      return {
        delivery_id: String(row.delivery_id),
        level: row.attention!.level,
        primary_reason: flags[0],
        flags,
        since: row.attention?.since ?? null,
      }
    })
    .sort((left, right) => {
      if (left.level !== right.level) return Number(right.level) - Number(left.level)
      const reason = ATTENTION_REASONS.indexOf(left.primary_reason) - ATTENTION_REASONS.indexOf(right.primary_reason)
      if (reason !== 0) return reason
      const leftSince = left.since ? Date.parse(left.since) : Number.POSITIVE_INFINITY
      const rightSince = right.since ? Date.parse(right.since) : Number.POSITIVE_INFINITY
      if (leftSince !== rightSince) return leftSince - rightSince
      return left.delivery_id.localeCompare(right.delivery_id)
    })

  snapshot.selected_delivery = rows[0]?.delivery_id ?? null
  snapshot.aggregates = {
    schema_version: 1,
    structural_revision: `fixture-structural-${count}`,
    classification_revision: `fixture-classification-${count}`,
    calculated_at: serverTime,
    next_refresh_at: new Date(calculatedMs + 10 * 60_000).toISOString(),
    root,
    projects,
    attention: { total: attentionItems.length, items: attentionItems.slice(0, 12) },
  }
  return snapshot
}
