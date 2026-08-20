/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-807 — strict PAI-804 aggregate schema-v1 boundary.
//
// This parser is intentionally independent from delivery-card presentation.
// Production portfolio totals are accepted only when the complete aggregate
// object is internally consistent. Nothing in this file derives or repairs a
// CountSet from delivery rows, and missing fields are never zero-filled.

export const AGENT_MODE_AGGREGATE_SCHEMA_VERSION = 1 as const

export const AGGREGATE_STAGE_KEYS = [
  'specification',
  'implementation',
  'qa',
  'deployment',
  'verification',
  'unknown',
] as const
export type AggregateStageKey = (typeof AGGREGATE_STAGE_KEYS)[number]

export const AGGREGATE_LANDING_KEYS = [
  'within_4h',
  'within_24h',
  'within_3d',
  'later',
  'range_only',
  'suppressed_or_unknown',
] as const
export type AggregateLandingKey = (typeof AGGREGATE_LANDING_KEYS)[number]

export const AGGREGATE_FLAG_KEYS = [
  'attention',
  'waiting_needs_input',
  'blocked',
  'stale_no_signal',
  'failed_needs_retry',
  'deployed_unverified',
  'unverified',
  'unknown_reporter',
] as const
export type AggregateFlagKey = (typeof AGGREGATE_FLAG_KEYS)[number]

export const ATTENTION_REASON_KEYS = [
  'blocked',
  'waiting_needs_input',
  'failed_needs_retry',
  'stale_no_signal',
  'unknown_reporter',
  'deployed_unverified',
  'unverified',
  'other',
] as const
export type AttentionReasonKey = (typeof ATTENTION_REASON_KEYS)[number]

export type AggregatePartition<T extends string> = Record<T, number>

export interface AgentModeCountSet {
  activeTotal: number
  currentStage: AggregatePartition<AggregateStageKey>
  flags: AggregatePartition<AggregateFlagKey>
  landing: AggregatePartition<AggregateLandingKey>
}

export interface AgentModeLaneAggregate {
  laneKey: string
  epicId: number | null
  epicKey: string | null
  epicTitle: string | null
  counts: AgentModeCountSet
}

export interface AgentModeProjectAggregate {
  projectId: number
  projectKey: string
  projectName: string
  counts: AgentModeCountSet
  lanes: AgentModeLaneAggregate[]
}

export interface AgentModeAttentionAggregateItem {
  deliveryId: string
  level: 1 | 2 | 3
  primaryReason: AttentionReasonKey
  flags: AttentionReasonKey[]
  since: string | null
}

export interface AgentModeAttentionAggregate {
  total: number
  items: AgentModeAttentionAggregateItem[]
}

export interface AgentModeAggregates {
  schemaVersion: typeof AGENT_MODE_AGGREGATE_SCHEMA_VERSION
  structuralRevision: string
  classificationRevision: string
  calculatedAt: string
  nextRefreshAt: string | null
  root: AgentModeCountSet
  projects: AgentModeProjectAggregate[]
  attention: AgentModeAttentionAggregate
}

/** Normalized row facts used only to validate aggregate hierarchy membership.
 * Stage, flag and landing counts remain wholly server-authoritative and are
 * deliberately absent so this boundary cannot reconstruct them from cards. */
export interface AgentModeAggregateStructuralRow {
  id: string
  lane: {
    key: string
    projectId: number
    projectKey: string
    projectName: string
    epicId: number | null
    epicKey: string | null
    epicTitle: string | null
  }
}

export type AgentModeAggregateUnavailableReason = 'missing' | 'unsupported-schema' | 'malformed'

export type AgentModeAggregateParseResult =
  | { ok: true; value: AgentModeAggregates }
  | { ok: false; reason: AgentModeAggregateUnavailableReason }

type UnknownRecord = Record<string, unknown>

const COUNT_SET_KEYS = ['active_total', 'current_stage', 'flags', 'landing'] as const
const AGGREGATE_KEYS = [
  'schema_version',
  'structural_revision',
  'classification_revision',
  'calculated_at',
  'next_refresh_at',
  'root',
  'projects',
  'attention',
] as const
const PROJECT_KEYS = ['project_id', 'project_key', 'project_name', 'counts', 'lanes'] as const
const LANE_KEYS = ['lane_key', 'epic_id', 'epic_key', 'epic_title', 'counts'] as const
const ATTENTION_KEYS = ['total', 'items'] as const
const ATTENTION_ITEM_KEYS = ['delivery_id', 'level', 'primary_reason', 'flags', 'since'] as const

function record(value: unknown): UnknownRecord | null {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) return null
  return value as UnknownRecord
}

function exactKeys(value: UnknownRecord, expected: readonly string[]): boolean {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  return actual.length === wanted.length && actual.every((key, index) => key === wanted[index])
}

function safeCount(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}

function positiveSafeInteger(value: unknown): number | null {
  const parsed = safeCount(value)
  return parsed != null && parsed > 0 ? parsed : null
}

function nonEmptyString(value: unknown): string | null {
  return typeof value === 'string' && value.trim() !== '' && value === value.trim() ? value : null
}

function nullableNonEmptyString(value: unknown): string | null | undefined {
  if (value === null) return null
  const parsed = nonEmptyString(value)
  return parsed ?? undefined
}

/** Strict ISO instant parser for schema-v1 clocks. Date.parse alone is not
 * sufficient because engines normalize impossible calendar dates such as
 * February 31 instead of rejecting them. */
export function parseIsoInstant(value: unknown): string | null {
  const parsed = nonEmptyString(value)
  if (!parsed) return null
  const match = parsed.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-](\d{2}):(\d{2}))$/)
  if (!match) return null
  const [, yearRaw, monthRaw, dayRaw, hourRaw, minuteRaw, secondRaw, offsetHourRaw, offsetMinuteRaw] = match
  const year = Number(yearRaw)
  const month = Number(monthRaw)
  const day = Number(dayRaw)
  const hour = Number(hourRaw)
  const minute = Number(minuteRaw)
  const second = Number(secondRaw)
  const offsetHour = offsetHourRaw == null ? 0 : Number(offsetHourRaw)
  const offsetMinute = offsetMinuteRaw == null ? 0 : Number(offsetMinuteRaw)
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const daysInMonth = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (
    month < 1 || month > 12
    || day < 1 || day > daysInMonth[month - 1]
    || hour > 23 || minute > 59 || second > 59
    || offsetHour > 23 || offsetMinute > 59
  ) return null
  return Number.isFinite(Date.parse(parsed)) ? parsed : null
}

function nullableIsoInstant(value: unknown): string | null | undefined {
  if (value === null) return null
  return parseIsoInstant(value) ?? undefined
}

function partition<K extends string>(value: unknown, keys: readonly K[]): AggregatePartition<K> | null {
  const source = record(value)
  if (!source || !exactKeys(source, keys)) return null
  const parsed = {} as AggregatePartition<K>
  for (const key of keys) {
    const count = safeCount(source[key])
    if (count == null) return null
    parsed[key] = count
  }
  return parsed
}

function safeSum(values: readonly number[]): number | null {
  let total = 0
  for (const value of values) {
    total += value
    if (!Number.isSafeInteger(total)) return null
  }
  return total
}

function parseCountSet(value: unknown): AgentModeCountSet | null {
  const source = record(value)
  if (!source || !exactKeys(source, COUNT_SET_KEYS)) return null
  const activeTotal = safeCount(source.active_total)
  const currentStage = partition(source.current_stage, AGGREGATE_STAGE_KEYS)
  const flags = partition(source.flags, AGGREGATE_FLAG_KEYS)
  const landing = partition(source.landing, AGGREGATE_LANDING_KEYS)
  if (activeTotal == null || !currentStage || !flags || !landing) return null
  if (safeSum(Object.values(currentStage)) !== activeTotal) return null
  if (safeSum(Object.values(landing)) !== activeTotal) return null
  if (Object.values(flags).some((count) => count > activeTotal)) return null
  if (flags.deployed_unverified > flags.unverified) return null
  return { activeTotal, currentStage, flags, landing }
}

function equalCountSet(left: AgentModeCountSet, right: AgentModeCountSet): boolean {
  if (left.activeTotal !== right.activeTotal) return false
  for (const key of AGGREGATE_STAGE_KEYS) if (left.currentStage[key] !== right.currentStage[key]) return false
  for (const key of AGGREGATE_FLAG_KEYS) if (left.flags[key] !== right.flags[key]) return false
  for (const key of AGGREGATE_LANDING_KEYS) if (left.landing[key] !== right.landing[key]) return false
  return true
}

export function sumCountSets(sets: readonly AgentModeCountSet[]): AgentModeCountSet | null {
  const emptyStage = Object.fromEntries(AGGREGATE_STAGE_KEYS.map((key) => [key, 0])) as AggregatePartition<AggregateStageKey>
  const emptyFlags = Object.fromEntries(AGGREGATE_FLAG_KEYS.map((key) => [key, 0])) as AggregatePartition<AggregateFlagKey>
  const emptyLanding = Object.fromEntries(AGGREGATE_LANDING_KEYS.map((key) => [key, 0])) as AggregatePartition<AggregateLandingKey>
  const result: AgentModeCountSet = { activeTotal: 0, currentStage: emptyStage, flags: emptyFlags, landing: emptyLanding }
  for (const set of sets) {
    const active = safeSum([result.activeTotal, set.activeTotal])
    if (active == null) return null
    result.activeTotal = active
    for (const key of AGGREGATE_STAGE_KEYS) {
      const next = safeSum([result.currentStage[key], set.currentStage[key]])
      if (next == null) return null
      result.currentStage[key] = next
    }
    for (const key of AGGREGATE_FLAG_KEYS) {
      const next = safeSum([result.flags[key], set.flags[key]])
      if (next == null) return null
      result.flags[key] = next
    }
    for (const key of AGGREGATE_LANDING_KEYS) {
      const next = safeSum([result.landing[key], set.landing[key]])
      if (next == null) return null
      result.landing[key] = next
    }
  }
  return result
}

function bytewise(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}

function parseLane(value: unknown, projectId: number): AgentModeLaneAggregate | null {
  const source = record(value)
  if (!source || !exactKeys(source, LANE_KEYS)) return null
  const laneKey = nonEmptyString(source.lane_key)
  let epicId: number | null
  if (source.epic_id === null) epicId = null
  else {
    const parsed = positiveSafeInteger(source.epic_id)
    if (parsed == null) return null
    epicId = parsed
  }
  const epicKey = nullableNonEmptyString(source.epic_key)
  const epicTitle = nullableNonEmptyString(source.epic_title)
  const counts = parseCountSet(source.counts)
  if (!laneKey || epicId === undefined || epicKey === undefined || epicTitle === undefined || !counts || counts.activeTotal <= 0) return null
  const expectedLaneKey = epicId == null
    ? `project:${projectId}/ungrouped`
    : `project:${projectId}/epic:${epicId}`
  if (laneKey !== expectedLaneKey) return null
  if (epicId == null ? epicKey !== null || epicTitle !== null : epicKey === null || epicTitle === null) return null
  return { laneKey, epicId, epicKey, epicTitle, counts }
}

function compareLanes(left: AgentModeLaneAggregate, right: AgentModeLaneAggregate): number {
  if (left.epicId == null && right.epicId != null) return 1
  if (left.epicId != null && right.epicId == null) return -1
  if (left.epicId != null && right.epicId != null && left.epicId !== right.epicId) return left.epicId - right.epicId
  return bytewise(left.laneKey, right.laneKey)
}

function parseProject(value: unknown): AgentModeProjectAggregate | null {
  const source = record(value)
  if (!source || !exactKeys(source, PROJECT_KEYS) || !Array.isArray(source.lanes)) return null
  const projectId = positiveSafeInteger(source.project_id)
  const projectKey = nonEmptyString(source.project_key)
  const projectName = nonEmptyString(source.project_name)
  const counts = parseCountSet(source.counts)
  if (
    projectId == null || !projectKey || !projectName || !counts
    || counts.activeTotal <= 0
    || source.lanes.length === 0
    || source.lanes.length > counts.activeTotal
  ) return null
  const lanes: AgentModeLaneAggregate[] = []
  const seen = new Set<string>()
  for (const raw of source.lanes) {
    const lane = parseLane(raw, projectId)
    if (!lane || seen.has(lane.laneKey)) return null
    if (lanes.length > 0 && compareLanes(lanes[lanes.length - 1], lane) >= 0) return null
    seen.add(lane.laneKey)
    lanes.push(lane)
  }
  const laneSum = sumCountSets(lanes.map((lane) => lane.counts))
  if (!laneSum || !equalCountSet(counts, laneSum)) return null
  return { projectId, projectKey, projectName, counts, lanes }
}

function expectedAttentionLevel(flags: readonly AttentionReasonKey[]): 1 | 2 | 3 | null {
  if (flags.includes('blocked')) return 3
  if (flags.includes('waiting_needs_input') || flags.includes('failed_needs_retry')) return 2
  if (flags.some((flag) => flag !== 'unverified')) return 1
  return null
}

function parseAttentionItem(value: unknown, deliveryIds: ReadonlySet<string>): AgentModeAttentionAggregateItem | null {
  const source = record(value)
  if (!source || !exactKeys(source, ATTENTION_ITEM_KEYS) || !Array.isArray(source.flags)) return null
  const deliveryId = nonEmptyString(source.delivery_id)
  const level = safeCount(source.level)
  const primaryReason = typeof source.primary_reason === 'string' && ATTENTION_REASON_KEYS.includes(source.primary_reason as AttentionReasonKey)
    ? source.primary_reason as AttentionReasonKey
    : null
  const since = nullableIsoInstant(source.since)
  if (!deliveryId || !deliveryIds.has(deliveryId) || (level !== 1 && level !== 2 && level !== 3) || !primaryReason || since === undefined) return null

  const flags: AttentionReasonKey[] = []
  let previousIndex = -1
  for (const raw of source.flags) {
    if (typeof raw !== 'string') return null
    const index = ATTENTION_REASON_KEYS.indexOf(raw as AttentionReasonKey)
    if (index < 0 || index <= previousIndex) return null
    previousIndex = index
    flags.push(raw as AttentionReasonKey)
  }
  if (flags.length === 0 || flags[0] !== primaryReason || expectedAttentionLevel(flags) !== level) return null
  return { deliveryId, level, primaryReason, flags, since }
}

function parseAttention(value: unknown, deliveryIds: ReadonlySet<string>): AgentModeAttentionAggregate | null {
  const source = record(value)
  if (!source || !exactKeys(source, ATTENTION_KEYS) || !Array.isArray(source.items)) return null
  const total = safeCount(source.total)
  if (total == null || total < source.items.length || source.items.length > 12) return null
  const items: AgentModeAttentionAggregateItem[] = []
  const seen = new Set<string>()
  for (const raw of source.items) {
    const item = parseAttentionItem(raw, deliveryIds)
    if (!item || seen.has(item.deliveryId)) return null
    const previous = items[items.length - 1]
    if (previous) {
      const order = previous.level !== item.level
        ? item.level - previous.level
        : ATTENTION_REASON_KEYS.indexOf(previous.primaryReason) !== ATTENTION_REASON_KEYS.indexOf(item.primaryReason)
          ? ATTENTION_REASON_KEYS.indexOf(previous.primaryReason) - ATTENTION_REASON_KEYS.indexOf(item.primaryReason)
          : previous.since !== item.since
            ? (previous.since == null ? 1 : item.since == null ? -1 : Date.parse(previous.since) - Date.parse(item.since))
            : bytewise(previous.deliveryId, item.deliveryId)
      if (order >= 0) return null
    }
    seen.add(item.deliveryId)
    items.push(item)
  }
  return { total, items }
}

/**
 * Parses the complete aggregate object or returns a reason suitable for an
 * explicit unavailable state. `deliveryIds` is the authorized in-filter row
 * set; every attention reference must resolve inside it.
 */
export function parseAgentModeAggregates(
  value: unknown,
  activeRows: readonly AgentModeAggregateStructuralRow[],
): AgentModeAggregateParseResult {
  if (value == null) return { ok: false, reason: 'missing' }
  const source = record(value)
  if (!source) return { ok: false, reason: 'malformed' }
  if (source.schema_version !== AGENT_MODE_AGGREGATE_SCHEMA_VERSION) {
    return { ok: false, reason: 'unsupported-schema' }
  }
  if (!exactKeys(source, AGGREGATE_KEYS) || !Array.isArray(source.projects)) {
    return { ok: false, reason: 'malformed' }
  }

  const structuralRevision = nonEmptyString(source.structural_revision)
  const classificationRevision = nonEmptyString(source.classification_revision)
  const calculatedAt = parseIsoInstant(source.calculated_at)
  const nextRefreshAt = nullableIsoInstant(source.next_refresh_at)
  const root = parseCountSet(source.root)
  if (!structuralRevision || !classificationRevision || !calculatedAt || nextRefreshAt === undefined || !root) {
    return { ok: false, reason: 'malformed' }
  }
  if (nextRefreshAt != null && Date.parse(nextRefreshAt) <= Date.parse(calculatedAt)) {
    return { ok: false, reason: 'malformed' }
  }
  if (source.projects.length > root.activeTotal) return { ok: false, reason: 'malformed' }

  const projects: AgentModeProjectAggregate[] = []
  const seenProjects = new Set<number>()
  const seenLanes = new Set<string>()
  let laneCardinality = 0
  for (const raw of source.projects) {
    const rawProject = record(raw)
    if (!rawProject || !Array.isArray(rawProject.lanes)) return { ok: false, reason: 'malformed' }
    laneCardinality += rawProject.lanes.length
    if (!Number.isSafeInteger(laneCardinality) || laneCardinality > root.activeTotal) {
      return { ok: false, reason: 'malformed' }
    }
    const project = parseProject(raw)
    if (!project || seenProjects.has(project.projectId)) return { ok: false, reason: 'malformed' }
    if (projects.length > 0 && projects[projects.length - 1].projectId >= project.projectId) {
      return { ok: false, reason: 'malformed' }
    }
    for (const lane of project.lanes) {
      if (seenLanes.has(lane.laneKey)) return { ok: false, reason: 'malformed' }
      seenLanes.add(lane.laneKey)
    }
    seenProjects.add(project.projectId)
    projects.push(project)
  }
  const projectSum = sumCountSets(projects.map((project) => project.counts))
  if (!projectSum || !equalCountSet(root, projectSum) || root.activeTotal !== activeRows.length) {
    return { ok: false, reason: 'malformed' }
  }
  const rowsByLane = new Map<string, AgentModeAggregateStructuralRow[]>()
  const deliveryIds = new Set<string>()
  for (const row of activeRows) {
    if (deliveryIds.has(row.id)) return { ok: false, reason: 'malformed' }
    deliveryIds.add(row.id)
    rowsByLane.set(row.lane.key, [...(rowsByLane.get(row.lane.key) ?? []), row])
  }
  for (const project of projects) {
    let projectRowCount = 0
    for (const lane of project.lanes) {
      const laneRows = rowsByLane.get(lane.laneKey) ?? []
      if (laneRows.length !== lane.counts.activeTotal) return { ok: false, reason: 'malformed' }
      projectRowCount += laneRows.length
      for (const row of laneRows) {
        if (
          row.lane.projectId !== project.projectId
          || row.lane.projectKey !== project.projectKey
          || row.lane.projectName !== project.projectName
          || row.lane.epicId !== lane.epicId
          || row.lane.epicKey !== lane.epicKey
          || row.lane.epicTitle !== lane.epicTitle
        ) return { ok: false, reason: 'malformed' }
      }
      rowsByLane.delete(lane.laneKey)
    }
    if (projectRowCount !== project.counts.activeTotal) return { ok: false, reason: 'malformed' }
  }
  if (rowsByLane.size !== 0) return { ok: false, reason: 'malformed' }
  const attention = parseAttention(source.attention, deliveryIds)
  if (!attention || attention.total !== root.flags.attention) return { ok: false, reason: 'malformed' }
  const visibleReasonCounts: Partial<Record<AggregateFlagKey, number>> = {}
  for (const item of attention.items) {
    for (const reason of item.flags) {
      if (reason === 'other') continue
      visibleReasonCounts[reason] = (visibleReasonCounts[reason] ?? 0) + 1
    }
  }
  for (const [key, count] of Object.entries(visibleReasonCounts) as Array<[AggregateFlagKey, number]>) {
    if (count > root.flags[key]) return { ok: false, reason: 'malformed' }
  }

  return {
    ok: true,
    value: {
      schemaVersion: AGENT_MODE_AGGREGATE_SCHEMA_VERSION,
      structuralRevision,
      classificationRevision,
      calculatedAt,
      nextRefreshAt,
      root,
      projects,
      attention,
    },
  }
}
