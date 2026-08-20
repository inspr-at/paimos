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

// PAI-805 — Agent Mode transport seam.
//
// This file is the ONLY place that knows the wire shape of the PAI-804
// delivery supervision API. Everything else in Agent Mode (composables,
// cards, lanes, selection, trust policy) consumes the normalized domain
// types from `./agentMode.ts`. When the backend branch for PAI-804 lands
// with a different payload, adjust the `Wire*` interfaces, the path
// builder, and `normalizeWireSnapshot()` here — nothing downstream should
// need to change.
//
// Design rules (PAI-801 invariants):
//   - Unknown or missing fields normalize to an explicit `unknown` /
//     `null`, never to a fabricated value. A delivery with no trust
//     metadata gets `trusted: false`, which the UI renders as "no
//     estimate", not as 0 %.
//   - The server time is retained so remaining-time math never relies
//     on the browser clock alone (PAI-803 clock-skew rule).

import type {
  ActivityKind,
  AgentModeSnapshot,
  AttentionLevel,
  Delivery,
  DeliveryActor,
  DeliveryHealth,
  DeliveryStageStatus,
  EstimateConfidence,
  FreshnessState,
  StageKey,
} from './agentMode'

/** Cross-project, ACL-filtered snapshot of all authorized active deliveries. */
export const AGENT_MODE_SNAPSHOT_PATH = '/agent-mode/deliveries'

export interface AgentModeSnapshotQuery {
  projectId?: number | null
  epicId?: number | null
  /** Persistent identity hint. PAI-804 returns an authorized terminal or
   * filtered selection separately as selected_outside_results. */
  selectedDelivery?: string | null
}

/** Builds the snapshot request path. Filters are optional server hints;
 * the client still applies its own filters so selection pinning works. */
export function buildSnapshotPath(query: AgentModeSnapshotQuery = {}): string {
  const params = new URLSearchParams()
  if (query.projectId != null && Number.isFinite(query.projectId)) {
    params.set('project_id', String(query.projectId))
  }
  if (query.epicId != null && Number.isFinite(query.epicId)) {
    params.set('epic_id', String(query.epicId))
  }
  if (typeof query.selectedDelivery === 'string' && query.selectedDelivery.trim() !== '') {
    params.set('selected_delivery', query.selectedDelivery.trim())
  }
  const qs = params.toString()
  return qs ? `${AGENT_MODE_SNAPSHOT_PATH}?${qs}` : AGENT_MODE_SNAPSHOT_PATH
}

// ── Wire shapes (PAI-804 direction, snake_case) ─────────────────────────

export interface WireActor {
  name?: string | null
  label?: string | null
  kind?: string | null
}

export interface WireActivity {
  kind?: string | null
  text?: string | null
  since?: string | null
}

export interface WireStage {
  key?: string | null
  label?: string | null
  index?: number | null
  total?: number | null
}

export interface WireAttention {
  level?: number | null
  reason?: string | null
  since?: string | null
}

export interface WireFreshness {
  state?: string | null
  last_report_at?: string | null
}

export interface WireBlocker {
  kind?: string | null
  text?: string | null
}

export interface WireProgress {
  percent?: number | null
  trusted?: boolean | null
  confidence?: string | null
  source?: string | null
  basis?: string | null
  revision?: string | number | null
}

export interface WireEvidence {
  evidence_id?: string | number | null
  kind?: string | null
  label?: string | null
  summary?: string | null
  status?: string | null
  reported_at?: string | null
  reporter?: WireActor | null
}

export interface WireDeliveryStage {
  key?: string | null
  label?: string | null
  status?: string | null
  required?: boolean | null
  owner?: WireActor | null
  activity?: string | null
  blockers?: WireBlocker[] | null
  evidence?: WireEvidence[] | null
  started_at?: string | null
  completed_at?: string | null
}

export interface WireHandoff {
  handoff_id?: string | number | null
  from?: WireActor | null
  to?: WireActor | null
  status?: string | null
  summary?: string | null
  reported_at?: string | null
}

export interface WireCapabilities {
  view_issue?: boolean | null
  edit_issue?: boolean | null
  comment?: boolean | null
  attach?: boolean | null
  live_note?: boolean | null
  one_shot_run_active?: boolean | null
}

export interface WireEta {
  landing_at?: string | null
  optimistic_at?: string | null
  pessimistic_at?: string | null
  trusted?: boolean | null
  confidence?: string | null
  basis?: string | null
  calculated_at?: string | null
}

export interface WireDelivery {
  delivery_id?: string | number | null
  issue_id?: number | null
  issue_key?: string | null
  title?: string | null
  project_id?: number | null
  project_key?: string | null
  project_name?: string | null
  epic_id?: number | null
  epic_key?: string | null
  epic_title?: string | null
  lane_key?: string | null
  attempt_id?: string | number | null
  attempt_number?: number | null
  attempt_status?: string | null
  plan_revision?: string | number | null
  delivery_revision?: string | number | null
  trust_revision?: string | number | null
  estimate_suppression_codes?: string[] | null
  estimate_disagreement_codes?: string[] | null
  tags?: string[] | null
  actor?: WireActor | null
  activity?: WireActivity | null
  stage?: WireStage | null
  stages?: WireDeliveryStage[] | null
  evidence?: WireEvidence[] | null
  handoffs?: WireHandoff[] | null
  capabilities?: WireCapabilities | null
  health?: string | null
  attention?: WireAttention | null
  freshness?: WireFreshness | null
  blockers?: WireBlocker[] | null
  progress?: WireProgress | null
  eta?: WireEta | null
  status_text?: string | null
  updated_at?: string | null
}

export interface WireSnapshot {
  server_time?: string | null
  revision?: string | number | null
  stream_cursor?: string | number | null
  selected_delivery?: string | number | null
  selected_outside_results?: WireDelivery | null
  deliveries?: WireDelivery[] | null
}

// ── Normalization ───────────────────────────────────────────────────────

const HEALTHS: ReadonlySet<DeliveryHealth> = new Set(['healthy', 'attention', 'at_risk', 'blocked', 'unknown'])
const ACTIVITIES: ReadonlySet<ActivityKind> = new Set([
  'working', 'testing', 'deploying', 'verifying', 'waiting', 'blocked', 'idle', 'unknown',
])
const STAGES: ReadonlySet<StageKey> = new Set([
  'specification', 'implementation', 'qa', 'deployment', 'verification', 'unknown',
])
const FRESHNESS: ReadonlySet<FreshnessState> = new Set(['fresh', 'aging', 'stale', 'unknown'])
const CONFIDENCES: ReadonlySet<EstimateConfidence> = new Set(['high', 'medium', 'low', 'none'])
const STAGE_STATUSES: ReadonlySet<DeliveryStageStatus> = new Set([
  'pending', 'active', 'waiting', 'blocked', 'failed', 'succeeded', 'not_applicable', 'unknown',
])
const ACTOR_KINDS = new Set(['agent', 'system', 'human', 'unknown'])

function pickEnum<T extends string>(value: unknown, allowed: ReadonlySet<T>, fallback: T): T {
  return typeof value === 'string' && allowed.has(value as T) ? (value as T) : fallback
}

function str(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const trimmed = value.trim()
  return trimmed === '' ? null : trimmed
}

function num(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function opaque(value: unknown): string | null {
  if (typeof value === 'number' && Number.isFinite(value)) return String(value)
  return str(value)
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.map(str).filter((v): v is string => v !== null)
}

function evidence(wire: WireEvidence | null | undefined) {
  if (!wire || typeof wire !== 'object') return null
  return {
    id: opaque(wire.evidence_id),
    kind: str(wire.kind) ?? 'unknown',
    label: str(wire.label),
    summary: str(wire.summary),
    status: str(wire.status),
    reportedAt: str(wire.reported_at),
    reporter: actor(wire.reporter),
  }
}

function evidenceList(value: WireEvidence[] | null | undefined) {
  return Array.isArray(value)
    ? value.map(evidence).filter((v): v is NonNullable<ReturnType<typeof evidence>> => v !== null)
    : []
}

function clampPercent(value: number | null): number | null {
  if (value == null) return null
  return Math.max(0, Math.min(100, Math.round(value)))
}

function attentionLevel(value: unknown): AttentionLevel {
  const n = num(value)
  if (n == null) return 0
  const clamped = Math.max(0, Math.min(3, Math.round(n)))
  return clamped as AttentionLevel
}

function actor(wire: WireActor | null | undefined): DeliveryActor | null {
  if (!wire) return null
  const name = str(wire.name)
  const label = str(wire.label) ?? name
  if (!name && !label) return null
  const kind = typeof wire.kind === 'string' && ACTOR_KINDS.has(wire.kind)
    ? (wire.kind as DeliveryActor['kind'])
    : 'unknown'
  return { name: name ?? label ?? 'unknown', label: label ?? name ?? 'unknown', kind }
}

/** Stable lane key: project, then epic (or the explicit Ungrouped lane). */
export function laneKeyFor(projectId: number, epicId: number | null): string {
  return epicId == null ? `project:${projectId}/ungrouped` : `project:${projectId}/epic:${epicId}`
}

export function normalizeWireDelivery(wire: WireDelivery): Delivery | null {
  const issueId = num(wire.issue_id)
  const projectId = num(wire.project_id)
  const rawId = wire.delivery_id
  const id = typeof rawId === 'string' && rawId.trim() !== ''
    ? rawId.trim()
    : typeof rawId === 'number' && Number.isFinite(rawId)
      ? String(rawId)
      : issueId != null
        ? `issue:${issueId}`
        : null
  // Without a stable identity and a project the delivery cannot be placed
  // in a truthful lane or selected deterministically — drop it rather
  // than invent a lane.
  if (id == null || issueId == null || projectId == null) return null

  const epicId = num(wire.epic_id)
  const progressWire = wire.progress ?? null
  const etaWire = wire.eta ?? null

  return {
    id,
    issueId,
    issueKey: str(wire.issue_key) ?? `#${issueId}`,
    title: str(wire.title) ?? '',
    lane: {
      key: str(wire.lane_key) ?? laneKeyFor(projectId, epicId),
      projectId,
      projectKey: str(wire.project_key) ?? `P${projectId}`,
      projectName: str(wire.project_name) ?? str(wire.project_key) ?? `Project ${projectId}`,
      epicId,
      epicKey: epicId == null ? null : str(wire.epic_key),
      epicTitle: epicId == null ? null : str(wire.epic_title),
    },
    attempt: {
      id: opaque(wire.attempt_id),
      number: num(wire.attempt_number),
      planRevision: opaque(wire.plan_revision),
      status: str(wire.attempt_status),
    },
    deliveryRevision: opaque(wire.delivery_revision),
    trustRevision: opaque(wire.trust_revision),
    suppressionCodes: stringList(wire.estimate_suppression_codes),
    disagreementCodes: stringList(wire.estimate_disagreement_codes),
    tags: Array.isArray(wire.tags) ? wire.tags.filter((t): t is string => typeof t === 'string' && t !== '') : [],
    actor: actor(wire.actor),
    activity: {
      kind: pickEnum(wire.activity?.kind, ACTIVITIES, 'unknown'),
      text: str(wire.activity?.text),
      since: str(wire.activity?.since),
    },
    stage: {
      key: pickEnum(wire.stage?.key, STAGES, 'unknown'),
      label: str(wire.stage?.label),
      index: num(wire.stage?.index),
      total: num(wire.stage?.total),
    },
    stages: Array.isArray(wire.stages)
      ? wire.stages.map((s) => ({
          key: pickEnum(s?.key, STAGES, 'unknown'),
          label: str(s?.label),
          status: pickEnum(s?.status, STAGE_STATUSES, 'unknown'),
          required: typeof s?.required === 'boolean' ? s.required : null,
          owner: actor(s?.owner),
          activity: str(s?.activity),
          blockers: Array.isArray(s?.blockers)
            ? s.blockers.map((b) => ({ kind: str(b?.kind) ?? 'unknown', text: str(b?.text) ?? '' })).filter((b) => b.text !== '')
            : [],
          evidence: evidenceList(s?.evidence),
          startedAt: str(s?.started_at),
          completedAt: str(s?.completed_at),
        }))
      : [],
    evidence: evidenceList(wire.evidence),
    handoffs: Array.isArray(wire.handoffs)
      ? wire.handoffs.map((h) => ({
          id: opaque(h?.handoff_id),
          from: actor(h?.from),
          to: actor(h?.to),
          status: str(h?.status),
          summary: str(h?.summary),
          reportedAt: str(h?.reported_at),
        }))
      : [],
    capabilities: {
      viewIssue: typeof wire.capabilities?.view_issue === 'boolean' ? wire.capabilities.view_issue : null,
      editIssue: typeof wire.capabilities?.edit_issue === 'boolean' ? wire.capabilities.edit_issue : null,
      comment: typeof wire.capabilities?.comment === 'boolean' ? wire.capabilities.comment : null,
      attach: typeof wire.capabilities?.attach === 'boolean' ? wire.capabilities.attach : null,
      liveNote: typeof wire.capabilities?.live_note === 'boolean' ? wire.capabilities.live_note : null,
      oneShotRunActive: typeof wire.capabilities?.one_shot_run_active === 'boolean'
        ? wire.capabilities.one_shot_run_active
        : null,
    },
    health: pickEnum(wire.health, HEALTHS, 'unknown'),
    attention: {
      level: attentionLevel(wire.attention?.level),
      reason: str(wire.attention?.reason),
      since: str(wire.attention?.since),
    },
    freshness: {
      state: pickEnum(wire.freshness?.state, FRESHNESS, 'unknown'),
      lastReportAt: str(wire.freshness?.last_report_at),
    },
    blockers: Array.isArray(wire.blockers)
      ? wire.blockers
          .map((b) => ({ kind: str(b?.kind) ?? 'unknown', text: str(b?.text) ?? '' }))
          .filter((b) => b.text !== '')
      : [],
    progress: progressWire
      ? {
          percent: clampPercent(num(progressWire.percent)),
          trusted: progressWire.trusted === true,
          confidence: pickEnum(progressWire.confidence, CONFIDENCES, 'none'),
          source: str(progressWire.source),
          basis: str(progressWire.basis),
          revision: typeof progressWire.revision === 'number' && Number.isFinite(progressWire.revision)
            ? progressWire.revision
            : str(progressWire.revision),
        }
      : null,
    eta: etaWire
      ? {
          landingAt: str(etaWire.landing_at),
          optimisticAt: str(etaWire.optimistic_at),
          pessimisticAt: str(etaWire.pessimistic_at),
          trusted: etaWire.trusted === true,
          confidence: pickEnum(etaWire.confidence, CONFIDENCES, 'none'),
          basis: str(etaWire.basis),
          calculatedAt: str(etaWire.calculated_at),
        }
      : null,
    statusText: str(wire.status_text),
    updatedAt: str(wire.updated_at),
  }
}

export function normalizeWireSnapshot(wire: WireSnapshot | null | undefined, receivedAt: number): AgentModeSnapshot {
  const list = Array.isArray(wire?.deliveries) ? wire!.deliveries! : []
  const deliveries: Delivery[] = []
  const seen = new Set<string>()
  for (const item of list) {
    if (!item || typeof item !== 'object') continue
    const normalized = normalizeWireDelivery(item)
    if (!normalized || seen.has(normalized.id)) continue
    seen.add(normalized.id)
    deliveries.push(normalized)
  }
  const serverTime = str(wire?.server_time)
  const revision = wire?.revision == null ? null : String(wire.revision)
  const outside = normalizeWireDelivery(wire?.selected_outside_results ?? {})
  const selectedDeliveryId = opaque(wire?.selected_delivery) ?? outside?.id ?? null
  const selectedOutsideResults = outside
    && !seen.has(outside.id)
    && outside.id === selectedDeliveryId
    ? outside
    : null
  const cursor = opaque(wire?.stream_cursor)
  return { serverTime, revision, cursor, deliveries, selectedOutsideResults, selectedDeliveryId, receivedAt }
}
