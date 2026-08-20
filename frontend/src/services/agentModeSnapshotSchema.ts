/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-804 — strict, privacy-preserving snapshot schema-v1 boundary.
// Every object is closed. Optional keys correspond only to Go `omitempty`
// fields in backend/agentmode/types.go; legacy mock aliases and raw identities
// are rejected before a Delivery reaches reactive application state.

import type {
  ActivityKind,
  AgentModeSnapshot,
  AttentionLevel,
  Delivery,
  DeliveryActor,
  DeliveryHealth,
  DeliveryStage,
  DeliveryStageStatus,
  DeliveryTrust,
  EstimateConfidence,
  FreshnessState,
  StageKey,
} from './agentMode'
import { compareIsoInstants, parseAgentModeAggregates, parseIsoInstant } from './agentModeAggregateSchema'
import { isCanonicalAgentModeCursor } from './agentModeCursor'

type UnknownRecord = Record<string, unknown>

export class AgentModeSnapshotContractError extends Error {
  constructor(message = 'invalid Agent Mode snapshot schema') {
    super(message)
    this.name = 'AgentModeSnapshotContractError'
  }
}

const ROW_REQUIRED_KEYS = [
  'delivery_id', 'issue_id', 'issue_key', 'title', 'project_id', 'project_key', 'project_name',
  'epic_id', 'epic_key', 'epic_title', 'lane_key', 'attempt_id', 'attempt_number',
  'attempt_status', 'plan_revision', 'delivery_revision', 'trust_revision', 'tags', 'actor',
  'activity', 'stage', 'stages', 'evidence', 'capabilities', 'health', 'attention', 'freshness',
  'blockers', 'progress', 'eta', 'trust',
] as const
const ROW_OPTIONAL_KEYS = ['status_text', 'updated_at'] as const
const SNAPSHOT_REQUIRED_KEYS = [
  'schema_version', 'server_time', 'cursor', 'rows', 'selected_delivery',
] as const

const ACTIVITY_KINDS = new Set<ActivityKind>([
  'working', 'testing', 'deploying', 'verifying', 'waiting', 'blocked', 'idle', 'unknown',
])
const ACTOR_KINDS = new Set<DeliveryActor['kind']>(['agent', 'external', 'system', 'human', 'unknown'])
const STAGE_KEYS = new Set<StageKey>(['specification', 'implementation', 'qa', 'deployment', 'verification', 'unknown'])
const PIPELINE_STAGE_KEYS = new Set<StageKey>(['specification', 'implementation', 'qa', 'deployment', 'verification'])
const STAGE_STATUSES = new Set<DeliveryStageStatus>([
  'pending', 'active', 'waiting', 'blocked', 'failed', 'cancelled', 'draft_ready', 'succeeded',
  'not_applicable', 'unknown',
])
const ATTEMPT_STATUSES = new Set([
  'pending', 'active', 'completed', 'failed_needs_retry', 'deployed_unverified', 'cancelled', 'unknown',
])
const HEALTHS = new Set<DeliveryHealth>(['healthy', 'attention', 'blocked', 'stale', 'unknown'])
const FRESHNESS = new Set<FreshnessState>(['fresh', 'aging', 'stale', 'unknown'])
type WireConfidence = 'high' | 'medium' | 'low' | 'unknown'
const WIRE_CONFIDENCES = new Set<WireConfidence>(['high', 'medium', 'low', 'unknown'])
const PROGRESS_SOURCES = new Set(['stage_evidence', 'owner_estimate'])
const REPORTER_KINDS = new Set<DeliveryTrust['reporterKind']>(['agent_run', 'external', 'user', 'system', 'unknown'])
const TRUST_SOURCE_KINDS = new Set<DeliveryTrust['sourceKind']>(['owner_estimate', 'stage_evidence', 'history'])
const EVIDENCE_STATUSES = new Set(['passed', 'failed', 'unknown'])
const BLOCKER_KINDS = new Set(['input', 'dependency', 'permission', 'environment', 'external', 'unknown', 'blocked'])
const ATTENTION_REASONS = new Set([
  'blocked', 'waiting_needs_input', 'failed_needs_retry', 'stale_no_signal', 'unknown_reporter',
  'deployed_unverified',
])
const OUTSIDE_REASONS = new Set<NonNullable<AgentModeSnapshot['selectedOutsideReason']>>([
  'filter_excluded', 'terminal', 'active_fallback', 'terminal_fallback',
])
const TRUST_SUPPRESSIONS = new Set([
  'terminal_complete', 'cancelled', 'terminal_failed', 'waiting_on_human', 'blocked', 'stale',
  'unknown_reporter', 'no_signal', 'estimate_expired', 'outlier_heavy', 'insufficient_basis',
  'missing_contributor',
])
const TRUST_FLAGS = new Set([
  'source_backslide_ignored', 'agent_history_disagreement', 'history_quality_downgraded',
  'history_outlier_heavy', 'history_insufficient_basis', 'owner_estimate_invalid',
  'owner_estimate_expired', 'deployed_unverified', 'failed_needs_retry',
])
const TRUST_REVISION = /^tr1_[0-9a-f]{64}$/
const DELIVERY_KEY = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/

function record(value: unknown): UnknownRecord | null {
  return value != null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function containsControlText(value: unknown): boolean {
  if (typeof value === 'string') return /\p{Cc}/u.test(value)
  if (Array.isArray(value)) return value.some(containsControlText)
  const source = record(value)
  return source ? Object.values(source).some(containsControlText) : false
}

function exactKeys(
  value: UnknownRecord,
  required: readonly string[],
  optional: readonly string[] = [],
): boolean {
  const actual = Object.keys(value)
  const allowed = new Set([...required, ...optional])
  return required.every((key) => Object.prototype.hasOwnProperty.call(value, key))
    && actual.every((key) => allowed.has(key))
}

function text(value: unknown): string | null {
  return typeof value === 'string' ? value : null
}

function nonEmptyText(value: unknown): string | null {
  return typeof value === 'string' && value.length > 0 ? value : null
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null
}

function nonNegativeInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}

function nullableText(value: unknown): string | null | undefined {
  return value === null ? null : typeof value === 'string' ? value : undefined
}

function nullablePositiveInteger(value: unknown): number | null | undefined {
  return value === null ? null : positiveInteger(value) ?? undefined
}

function nullableInstant(value: unknown): string | null | undefined {
  return value === null ? null : parseIsoInstant(value) ?? undefined
}

function enumValue<T extends string>(value: unknown, allowed: ReadonlySet<T>): T | null {
  return typeof value === 'string' && allowed.has(value as T) ? value as T : null
}

function parseActor(value: unknown): DeliveryActor | null | undefined {
  if (value === null) return null
  const source = record(value)
  if (!source || !exactKeys(source, ['name', 'label', 'kind'])) return undefined
  // Public display labels are plain OpenAPI strings, not identities. Empty
  // and edge-whitespace values can be emitted for legacy rows and must round
  // trip exactly rather than turning an otherwise valid snapshot into a
  // transport failure.
  const name = text(source.name)
  const label = text(source.label)
  const kind = enumValue(source.kind, ACTOR_KINDS)
  return name !== null && label !== null && kind ? { name, label, kind } : undefined
}

function parseActivity(value: unknown): Delivery['activity'] | null {
  const source = record(value)
  if (!source || !exactKeys(source, ['kind', 'since'], ['text'])) return null
  const kind = enumValue(source.kind, ACTIVITY_KINDS)
  const since = nullableInstant(source.since)
  const hasText = Object.prototype.hasOwnProperty.call(source, 'text')
  const activityText = hasText ? text(source.text) : null
  return kind && since !== undefined && (!hasText || activityText !== null)
    ? { kind, text: activityText, since }
    : null
}

function parseStageSummary(value: unknown): Delivery['stage'] | null {
  const source = record(value)
  if (!source || !exactKeys(source, ['key', 'label', 'index', 'total'])) return null
  const key = enumValue(source.key, STAGE_KEYS)
  const label = text(source.label)
  const index = nullablePositiveInteger(source.index)
  const total = nullablePositiveInteger(source.total)
  if (!key || label === null || index === undefined || total === undefined) return null
  if ((index === null) !== (total === null) || (index != null && total != null && index > total)) return null
  return { key, label, index, total }
}

function parseBlocker(value: unknown): { kind: string; text: string } | null {
  const source = record(value)
  if (!source || !exactKeys(source, ['kind'], ['text'])) return null
  const kind = enumValue(source.kind, BLOCKER_KINDS)
  const blockerText = Object.prototype.hasOwnProperty.call(source, 'text') ? text(source.text) : ''
  return kind && blockerText !== null ? { kind, text: blockerText } : null
}

function parseBlockers(value: unknown): Array<{ kind: string; text: string }> | null {
  if (!Array.isArray(value)) return null
  const out: Array<{ kind: string; text: string }> = []
  for (const raw of value) {
    const blocker = parseBlocker(raw)
    if (!blocker) return null
    out.push(blocker)
  }
  return out
}

function parseEvidence(value: unknown): Delivery['evidence'][number] | null {
  const source = record(value)
  if (!source || !exactKeys(source, ['kind', 'status', 'reported_at'])) return null
  const kind = text(source.kind)
  const status = enumValue(source.status, EVIDENCE_STATUSES)
  const reportedAt = nullableInstant(source.reported_at)
  if (kind === null || !status || reportedAt === undefined) return null
  return { id: null, kind, label: null, summary: null, status, reportedAt, reporter: null }
}

function parseEvidenceList(value: unknown): Delivery['evidence'] | null {
  if (!Array.isArray(value)) return null
  const out: Delivery['evidence'] = []
  for (const raw of value) {
    const item = parseEvidence(raw)
    if (!item) return null
    out.push(item)
  }
  return out
}

function parseStage(value: unknown): DeliveryStage | null {
  const source = record(value)
  if (!source || !exactKeys(source, [
    'key', 'label', 'status', 'required', 'owner', 'blockers', 'evidence', 'started_at', 'completed_at',
  ], ['activity'])) return null
  const key = enumValue(source.key, PIPELINE_STAGE_KEYS)
  const label = text(source.label)
  const status = enumValue(source.status, STAGE_STATUSES)
  const owner = parseActor(source.owner)
  const blockers = parseBlockers(source.blockers)
  const evidence = parseEvidenceList(source.evidence)
  const startedAt = nullableInstant(source.started_at)
  const completedAt = nullableInstant(source.completed_at)
  const hasActivity = Object.prototype.hasOwnProperty.call(source, 'activity')
  const activity = hasActivity ? text(source.activity) : null
  if (
    !key || label === null || !status || typeof source.required !== 'boolean' || owner === undefined
    || !blockers || !evidence || startedAt === undefined || completedAt === undefined
    || (hasActivity && activity === null)
  ) return null
  if (startedAt && completedAt && compareIsoInstants(completedAt, startedAt)! < 0) return null
  return { key, label, status, required: source.required, owner, activity, blockers, evidence, startedAt, completedAt }
}

function parseStages(value: unknown): DeliveryStage[] | null {
  if (!Array.isArray(value)) return null
  if (value.length !== 0 && value.length !== 5) return null
  const out: DeliveryStage[] = []
  const canonical = ['specification', 'implementation', 'qa', 'deployment', 'verification'] as const
  for (const [index, raw] of value.entries()) {
    const stage = parseStage(raw)
    if (!stage || stage.key !== canonical[index]) return null
    out.push(stage)
  }
  return out
}

function parseCapabilities(value: unknown): Delivery['capabilities'] | null {
  const source = record(value)
  const keys = ['view_issue', 'edit_issue', 'comment', 'attach', 'live_note', 'one_shot_run_active'] as const
  if (!source || !exactKeys(source, keys) || keys.some((key) => typeof source[key] !== 'boolean')) return null
  return {
    viewIssue: source.view_issue as boolean,
    editIssue: source.edit_issue as boolean,
    comment: source.comment as boolean,
    attach: source.attach as boolean,
    liveNote: source.live_note as boolean,
    oneShotRunActive: source.one_shot_run_active as boolean,
  }
}

function parseAttention(value: unknown): Delivery['attention'] | null {
  const source = record(value)
  if (!source || !exactKeys(source, ['level', 'since'], ['reason'])) return null
  const level = nonNegativeInteger(source.level)
  const since = nullableInstant(source.since)
  const hasReason = Object.prototype.hasOwnProperty.call(source, 'reason')
  const reason = hasReason ? enumValue(source.reason, ATTENTION_REASONS) : null
  if (level == null || level > 3 || since === undefined) return null
  if (hasReason && reason === null) return null
  if ((level === 0 && (reason !== null || since !== null)) || (level > 0 && reason === null)) return null
  return { level: level as AttentionLevel, reason, since }
}

function parseFreshness(value: unknown): Delivery['freshness'] | null {
  const source = record(value)
  if (!source || !exactKeys(source, ['state', 'last_report_at'])) return null
  const state = enumValue(source.state, FRESHNESS)
  const lastReportAt = nullableInstant(source.last_report_at)
  return state && lastReportAt !== undefined ? { state, lastReportAt } : null
}

function confidence(value: unknown): EstimateConfidence | null {
  const parsed = enumValue(value, WIRE_CONFIDENCES)
  return parsed === 'unknown' ? 'none' : parsed
}

function parseTrustScope(value: unknown): DeliveryTrust['scope'] | undefined {
  if (value === null) return null
  const source = record(value)
  const keys = ['attempt_id', 'plan_id', 'execution_id', 'authority_id', 'reset_id'] as const
  if (!source || !exactKeys(source, keys)) return undefined
  const values = keys.map((key) => text(source[key]))
  if (values.some((item) => item === null)) return undefined
  return {
    attemptId: values[0]!,
    planId: values[1]!,
    executionId: values[2]!,
    authorityId: values[3]!,
    resetId: values[4]!,
  }
}

function parseTrust(value: unknown): DeliveryTrust | null {
  const source = record(value)
  if (!source || !exactKeys(source, [
    'schema_version', 'trust_revision', 'progress_known', 'progress_percent', 'confidence_label',
    'reporter_kind', 'source_kind', 'optimistic_landing_at', 'pessimistic_landing_at', 'landing_at',
    'range_only', 'scope', 'flags',
  ], ['basis', 'suppression'])) return null
  const trustRevision = nonEmptyText(source.trust_revision)
  const progressPercent = source.progress_percent === null ? null : nonNegativeInteger(source.progress_percent)
  const parsedConfidence = confidence(source.confidence_label)
  const reporterKind = enumValue(source.reporter_kind, REPORTER_KINDS)
  const sourceKind = enumValue(source.source_kind, TRUST_SOURCE_KINDS)
  const optimisticLandingAt = nullableInstant(source.optimistic_landing_at)
  const pessimisticLandingAt = nullableInstant(source.pessimistic_landing_at)
  const landingAt = nullableInstant(source.landing_at)
  const scope = parseTrustScope(source.scope)
  const basis = Object.prototype.hasOwnProperty.call(source, 'basis') ? text(source.basis) : null
  const suppression = Object.prototype.hasOwnProperty.call(source, 'suppression')
    ? enumValue(source.suppression, TRUST_SUPPRESSIONS)
    : null
  if (
    source.schema_version !== 1 || !trustRevision || !TRUST_REVISION.test(trustRevision)
    || typeof source.progress_known !== 'boolean' || progressPercent === null && source.progress_percent !== null
    || !parsedConfidence || !reporterKind || !sourceKind || optimisticLandingAt === undefined
    || pessimisticLandingAt === undefined || landingAt === undefined || typeof source.range_only !== 'boolean'
    || scope === undefined || basis === null && Object.prototype.hasOwnProperty.call(source, 'basis')
    || suppression === null && Object.prototype.hasOwnProperty.call(source, 'suppression')
    || !Array.isArray(source.flags)
  ) return null
  if (!source.progress_known && progressPercent !== null) return null
  if (source.progress_known && progressPercent === null) return null
  if ((optimisticLandingAt === null) !== (pessimisticLandingAt === null)) return null
  if (optimisticLandingAt && pessimisticLandingAt && compareIsoInstants(optimisticLandingAt, pessimisticLandingAt)! > 0) return null
  if (
    landingAt && optimisticLandingAt && pessimisticLandingAt
    && (compareIsoInstants(landingAt, optimisticLandingAt)! < 0
      || compareIsoInstants(landingAt, pessimisticLandingAt)! > 0)
  ) return null
  if (source.range_only && landingAt !== null) return null
  const flags: string[] = []
  const seen = new Set<string>()
  for (const raw of source.flags) {
    if (typeof raw !== 'string' || !TRUST_FLAGS.has(raw) || seen.has(raw)) return null
    seen.add(raw)
    flags.push(raw)
  }
  return {
    schemaVersion: 1,
    trustRevision,
    progressKnown: source.progress_known,
    progressPercent,
    confidence: parsedConfidence,
    reporterKind,
    sourceKind,
    basis,
    optimisticLandingAt,
    pessimisticLandingAt,
    landingAt,
    rangeOnly: source.range_only,
    suppression,
    scope,
    flags,
  }
}

function parseProgress(value: unknown, trust: DeliveryTrust): Delivery['progress'] | null | undefined {
  if (value === null) return trust.progressKnown ? undefined : null
  const source = record(value)
  if (!source || !exactKeys(source, ['percent', 'trusted', 'confidence', 'source', 'basis', 'revision'])) return undefined
  const percent = source.percent === null ? null : nonNegativeInteger(source.percent)
  const parsedConfidence = confidence(source.confidence)
  const progressSource = source.source === null ? null : enumValue(source.source, PROGRESS_SOURCES)
  const basis = nullableText(source.basis)
  const revision = nonEmptyText(source.revision)
  if (
    percent === null && source.percent !== null || (percent != null && percent > 100)
    || source.trusted !== true || !parsedConfidence || (source.source !== null && progressSource === null)
    || basis === undefined || !revision || !trust.progressKnown
    || percent !== trust.progressPercent || parsedConfidence !== trust.confidence
    || revision !== trust.trustRevision
    || (progressSource === 'owner_estimate' && trust.sourceKind !== 'owner_estimate')
  ) return undefined
  return { percent, trusted: true, confidence: parsedConfidence, source: progressSource, basis, revision }
}

function parseEta(value: unknown, trust: DeliveryTrust): Delivery['eta'] | null | undefined {
  const trustHasEta = trust.landingAt !== null || trust.optimisticLandingAt !== null || trust.pessimisticLandingAt !== null
  if (value === null) return trustHasEta ? undefined : null
  const source = record(value)
  if (!source || !exactKeys(source, [
    'landing_at', 'optimistic_at', 'pessimistic_at', 'trusted', 'confidence', 'basis', 'calculated_at',
  ])) return undefined
  const landingAt = nullableInstant(source.landing_at)
  const optimisticAt = nullableInstant(source.optimistic_at)
  const pessimisticAt = nullableInstant(source.pessimistic_at)
  const parsedConfidence = confidence(source.confidence)
  const basis = nullableText(source.basis)
  const calculatedAt = parseIsoInstant(source.calculated_at)
  if (
    landingAt === undefined || optimisticAt === undefined || pessimisticAt === undefined
    || typeof source.trusted !== 'boolean' || !parsedConfidence || basis === undefined || !calculatedAt
    || !trustHasEta || landingAt !== trust.landingAt || optimisticAt !== trust.optimisticLandingAt
    || pessimisticAt !== trust.pessimisticLandingAt || parsedConfidence !== trust.confidence
    || source.trusted !== (trust.suppression === null)
  ) return undefined
  return { landingAt, optimisticAt, pessimisticAt, trusted: source.trusted, confidence: parsedConfidence, basis, calculatedAt }
}

function parseTags(value: unknown): string[] | null {
  if (!Array.isArray(value)) return null
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of value) {
    const tag = text(raw)
    if (tag === null || seen.has(tag)) return null
    seen.add(tag)
    out.push(tag)
  }
  return out
}

export function normalizeStrictWireDelivery(value: unknown): Delivery | null {
  const source = record(value)
  if (!source || !exactKeys(source, ROW_REQUIRED_KEYS, ROW_OPTIONAL_KEYS)) return null
  const id = typeof source.delivery_id === 'string' && DELIVERY_KEY.test(source.delivery_id)
    ? source.delivery_id
    : null
  const issueId = positiveInteger(source.issue_id)
  const issueKey = text(source.issue_key)
  const title = text(source.title)
  const projectId = positiveInteger(source.project_id)
  const projectKey = text(source.project_key)
  const projectName = text(source.project_name)
  const epicId = nullablePositiveInteger(source.epic_id)
  const epicKey = nullableText(source.epic_key)
  const epicTitle = nullableText(source.epic_title)
  const laneKey = nonEmptyText(source.lane_key)
  const attemptId = nullableText(source.attempt_id)
  const attemptNumber = nullablePositiveInteger(source.attempt_number)
  const attemptStatus = enumValue(source.attempt_status, ATTEMPT_STATUSES)
  const planRevision = nullableText(source.plan_revision)
  const deliveryRevision = text(source.delivery_revision)
  const trustRevision = nonEmptyText(source.trust_revision)
  const tags = parseTags(source.tags)
  const actor = parseActor(source.actor)
  const activity = parseActivity(source.activity)
  const stage = parseStageSummary(source.stage)
  const stages = parseStages(source.stages)
  const evidence = parseEvidenceList(source.evidence)
  const capabilities = parseCapabilities(source.capabilities)
  const health = enumValue(source.health, HEALTHS)
  const attention = parseAttention(source.attention)
  const freshness = parseFreshness(source.freshness)
  const blockers = parseBlockers(source.blockers)
  const trust = parseTrust(source.trust)
  if (
    !id || !issueId || issueKey === null || title === null || !projectId || projectKey === null || projectName === null
    || epicId === undefined || epicKey === undefined || epicTitle === undefined || !laneKey
    || attemptId === undefined || attemptNumber === undefined || !attemptStatus || planRevision === undefined
    || deliveryRevision === null || !trustRevision || !TRUST_REVISION.test(trustRevision) || !tags
    || actor === undefined || !activity || !stage || !stages || !evidence || !capabilities || !health
    || !attention || !freshness || !blockers || !trust || trustRevision !== trust.trustRevision
  ) return null
  const expectedLane = epicId === null
    ? `project:${projectId}/ungrouped`
    : `project:${projectId}/epic:${epicId}`
  if (laneKey !== expectedLane) return null
  if (epicId === null ? epicKey !== null || epicTitle !== null : epicKey === null || epicTitle === null) return null
  if (!((attemptId === null && attemptNumber === null && planRevision === null)
    || (attemptId !== null && attemptNumber !== null && planRevision !== null))) return null
  if (stages.length > 0) {
    if (stage.index == null || stage.total !== stages.length || stage.key !== stages[stage.index - 1]?.key
      || stage.label !== stages[stage.index - 1]?.label) return null
  }
  const progress = parseProgress(source.progress, trust)
  const eta = parseEta(source.eta, trust)
  if (progress === undefined || eta === undefined) return null
  const hasStatusText = Object.prototype.hasOwnProperty.call(source, 'status_text')
  const hasUpdatedAt = Object.prototype.hasOwnProperty.call(source, 'updated_at')
  const statusText = hasStatusText ? text(source.status_text) : null
  const updatedAt = hasUpdatedAt ? parseIsoInstant(source.updated_at) : null
  if (hasStatusText && statusText === null) return null
  if (hasUpdatedAt && updatedAt === null) return null
  return {
    id,
    issueId,
    issueKey,
    title,
    lane: { key: laneKey, projectId, projectKey, projectName, epicId, epicKey, epicTitle },
    attempt: { id: attemptId, number: attemptNumber, planRevision, status: attemptStatus },
    deliveryRevision,
    trustRevision,
    trust,
    suppressionCodes: trust.suppression ? [trust.suppression] : [],
    disagreementCodes: trust.flags.filter((flag) => flag === 'agent_history_disagreement'),
    tags,
    actor,
    activity,
    stage,
    stages,
    evidence,
    handoffs: [],
    capabilities,
    health,
    attention,
    freshness,
    blockers,
    progress,
    eta,
    statusText,
    updatedAt,
  }
}

export function normalizeStrictWireSnapshot(value: unknown, receivedAt: number): AgentModeSnapshot {
  // Defense in depth for custom/test loaders and future backend regressions:
  // no response text containing Unicode control characters enters reactive
  // UI state. Do not include the rejected payload in the public error.
  if (containsControlText(value)) throw new AgentModeSnapshotContractError()
  const source = record(value)
  if (!source || !exactKeys(source, SNAPSHOT_REQUIRED_KEYS, ['aggregates', 'selected_outside'])) {
    throw new AgentModeSnapshotContractError()
  }
  if (source.schema_version !== 1) throw new AgentModeSnapshotContractError('unsupported Agent Mode snapshot schema')
  const serverTime = parseIsoInstant(source.server_time)
  if (!serverTime || !isCanonicalAgentModeCursor(source.cursor)
    || !Array.isArray(source.rows) || source.rows.length > 1000) {
    throw new AgentModeSnapshotContractError()
  }
  const deliveries: Delivery[] = []
  const seen = new Set<string>()
  for (const raw of source.rows) {
    const row = normalizeStrictWireDelivery(raw)
    if (!row || seen.has(row.id)) throw new AgentModeSnapshotContractError()
    seen.add(row.id)
    deliveries.push(row)
  }
  if (deliveries.some((row) => row.eta && compareIsoInstants(row.eta.calculatedAt ?? '', serverTime) !== 0)) {
    throw new AgentModeSnapshotContractError()
  }
  if (typeof source.selected_delivery !== 'string') throw new AgentModeSnapshotContractError()
  const selectedDeliveryId = source.selected_delivery === ''
    ? null
    : DELIVERY_KEY.test(source.selected_delivery) ? source.selected_delivery : null
  if (source.selected_delivery !== '' && !selectedDeliveryId) throw new AgentModeSnapshotContractError()

  let selectedOutsideResults: Delivery | null = null
  let selectedOutsideReason: AgentModeSnapshot['selectedOutsideReason'] = null
  if (Object.prototype.hasOwnProperty.call(source, 'selected_outside')) {
    const outside = record(source.selected_outside)
    if (!outside || !exactKeys(outside, ['reason', 'row'])) throw new AgentModeSnapshotContractError()
    const reason = enumValue(outside.reason, OUTSIDE_REASONS)
    const row = normalizeStrictWireDelivery(outside.row)
    if (!reason || !row || !selectedDeliveryId || row.id !== selectedDeliveryId || seen.has(row.id)) {
      throw new AgentModeSnapshotContractError()
    }
    selectedOutsideResults = row
    selectedOutsideReason = reason
    if (row.eta && compareIsoInstants(row.eta.calculatedAt ?? '', serverTime) !== 0) {
      throw new AgentModeSnapshotContractError()
    }
  } else if (selectedDeliveryId && !seen.has(selectedDeliveryId)) {
    throw new AgentModeSnapshotContractError()
  }
  if (!selectedDeliveryId && deliveries.length > 0) throw new AgentModeSnapshotContractError()

  const parsedAggregates = parseAgentModeAggregates(source.aggregates, deliveries)
  const aggregateResult = parsedAggregates.ok
    && compareIsoInstants(parsedAggregates.value.calculatedAt, serverTime) === 0
    ? parsedAggregates
    : parsedAggregates.ok
      ? { ok: false as const, reason: 'malformed' as const }
      : parsedAggregates
  return {
    serverTime,
    revision: aggregateResult.ok ? aggregateResult.value.structuralRevision : null,
    cursor: source.cursor,
    deliveries,
    selectedOutsideResults,
    selectedOutsideReason,
    selectedDeliveryId,
    aggregates: aggregateResult.ok ? aggregateResult.value : null,
    aggregateUnavailableReason: aggregateResult.ok ? null : aggregateResult.reason,
    receivedAt,
  }
}
