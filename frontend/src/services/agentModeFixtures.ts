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

import type { WireDelivery, WireSnapshot } from './agentModeTransport'

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
    server_time: serverTime,
    revision: `fx-${count}-1`,
    deliveries: Array.from({ length: count }, (_, i) => makeFixtureDelivery(i)),
  }
}
