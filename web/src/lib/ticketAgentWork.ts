// SPDX-License-Identifier: AGPL-3.0-only
// Agent work on a ticket or epic (api/openapi.yaml TicketAgentWork). Token
// counts stay decimal integer strings. Estimated cost stays an exact USD
// decimal string. A JSON number is rejected so money never passes through a
// binary float.

import { api, APIError } from './api.ts'

export type FigureState = 'known' | 'partial' | 'unknown'
export type CostState = 'estimated' | 'provisional' | 'partial' | 'unknown'
export type DurationState = 'known' | 'ongoing' | 'partial' | 'unknown'
export type BillingMode = 'unknown' | 'api' | 'subscription'

export interface TicketAgentUsageModel {
  model: string
  input_tokens: string | null
  output_tokens: string | null
  cached_input_tokens: string | null
  tokens_state: 'known' | 'unknown'
  cached_state: 'known' | 'unknown'
  estimated_cost_usd: string | null
  cost_state: 'estimated' | 'unknown'
  provisional: boolean
  price_version: string | null
  billing_mode: BillingMode
  subscription_label: string | null
}

export interface TicketAgentSession {
  id: string
  ticket_node_id: string
  ticket_key: string
  ticket_title: string
  harness: string
  label: string | null
  model: string | null
  model_state: 'known' | 'missing'
  effort: string | null
  effort_state: 'known' | 'missing'
  phase: string
  started_at: string
  ended_at: string | null
  duration_seconds: number | null
  duration_state: 'known' | 'ongoing' | 'unknown'
  usage_reported: boolean
  models: TicketAgentUsageModel[]
  models_truncated: boolean
  input_tokens: string | null
  output_tokens: string | null
  cached_input_tokens: string | null
  tokens_state: FigureState
  cached_state: FigureState
  estimated_cost_usd: string | null
  cost_state: CostState
  unknown_token_models: number
  unknown_cost_models: number
}

export interface TicketAgentTotals {
  session_count: number
  input_tokens: string | null
  output_tokens: string | null
  cached_input_tokens: string | null
  tokens_state: FigureState
  cached_state: FigureState
  estimated_cost_usd: string | null
  cost_state: CostState
  currency: string
  duration_seconds: number | null
  duration_state: DurationState
  unknown_token_sessions: number
  unknown_cost_sessions: number
  unknown_token_models: number
  unknown_cost_models: number
}

export interface TicketAgentWork {
  node_id: string
  kind: string
  currency: string
  usage_available: boolean
  includes_descendants: boolean
  scope_truncated: boolean
  list_truncated: boolean
  sessions: TicketAgentSession[]
  totals: TicketAgentTotals
}

const INT = /^(0|[1-9]\d{0,18})$/
const DEC = /^(0|[1-9]\d{0,17})(\.\d{1,12})?$/

export function integerString(value: unknown): string | null {
  if (typeof value !== 'string' || !INT.test(value)) return null
  return value
}

export function decimalString(value: unknown): string | null {
  if (typeof value !== 'string' || !DEC.test(value)) return null
  return value
}

export function formatUsd(value: string | null): string | null {
  const text = decimalString(value)
  if (text == null) return null
  const [whole, frac = ''] = text.split('.')
  const trimmed = frac.replace(/0+$/, '')
  const shown = trimmed.length === 1 ? `${trimmed}0` : trimmed
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return shown ? `$${grouped}.${shown}` : `$${grouped}`
}

export function formatTokenCount(value: string | null): string | null {
  const text = integerString(value)
  if (text == null) return null
  return text.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

export function formatWorkDuration(seconds: number | null, state: string): string {
  if (state === 'unknown' || typeof seconds !== 'number' || !Number.isSafeInteger(seconds) || seconds < 0) return 'Time unknown'
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60
  const parts: string[] = []
  if (hours) parts.push(`${hours}h`)
  if (minutes) parts.push(`${minutes}m`)
  if (rest && !hours) parts.push(`${rest}s`)
  const text = parts.length ? parts.join(' ') : '0s'
  if (state === 'ongoing') return `${text} so far`
  if (state === 'partial') return `at least ${text}`
  return text
}

export function harnessLabel(harness: string): string {
  const text = harness.trim()
  if (!text) return 'Harness unknown'
  return text.slice(0, 1).toUpperCase() + text.slice(1)
}

function text(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value : null
}

function count(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], fallback: T): T {
  return typeof value === 'string' && (allowed as readonly string[]).includes(value) ? value as T : fallback
}

function usageModel(value: unknown): TicketAgentUsageModel | null {
  const row = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const model = text(row.model)
  if (!model) return null
  const input = integerString(row.input_tokens)
  const output = integerString(row.output_tokens)
  const pair = input != null && output != null
  const cached = integerString(row.cached_input_tokens)
  const cost = decimalString(row.estimated_cost_usd)
  const costState = oneOf(row.cost_state, ['estimated', 'unknown'] as const, 'unknown')
  return {
    model,
    input_tokens: pair ? input : null,
    output_tokens: pair ? output : null,
    cached_input_tokens: cached,
    tokens_state: pair ? 'known' : 'unknown',
    cached_state: cached ? 'known' : 'unknown',
    estimated_cost_usd: costState === 'estimated' ? cost : null,
    cost_state: cost == null || costState !== 'estimated' ? 'unknown' : 'estimated',
    provisional: row.provisional === true,
    price_version: integerString(row.price_version),
    billing_mode: oneOf(row.billing_mode, ['unknown', 'api', 'subscription'] as const, 'unknown'),
    subscription_label: text(row.subscription_label),
  }
}

function session(value: unknown): TicketAgentSession {
  const row = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const input = integerString(row.input_tokens)
  const output = integerString(row.output_tokens)
  const pair = input != null && output != null
  const cached = integerString(row.cached_input_tokens)
  const cost = decimalString(row.estimated_cost_usd)
  const costState = oneOf(row.cost_state, ['estimated', 'provisional', 'partial', 'unknown'] as const, 'unknown')
  const models = Array.isArray(row.models) ? row.models.map(usageModel).filter((item): item is TicketAgentUsageModel => item != null) : []
  return {
    id: text(row.id) ?? '',
    ticket_node_id: text(row.ticket_node_id) ?? '',
    ticket_key: text(row.ticket_key) ?? '',
    ticket_title: text(row.ticket_title) ?? '',
    harness: text(row.harness) ?? '',
    label: text(row.label),
    model: text(row.model),
    model_state: oneOf(row.model_state, ['known', 'missing'] as const, 'missing'),
    effort: text(row.effort),
    effort_state: oneOf(row.effort_state, ['known', 'missing'] as const, 'missing'),
    phase: text(row.phase) ?? '',
    started_at: text(row.started_at) ?? '',
    ended_at: text(row.ended_at),
    duration_seconds: typeof row.duration_seconds === 'number' && Number.isSafeInteger(row.duration_seconds) && row.duration_seconds >= 0 ? row.duration_seconds : null,
    duration_state: oneOf(row.duration_state, ['known', 'ongoing', 'unknown'] as const, 'unknown'),
    usage_reported: row.usage_reported === true,
    models,
    models_truncated: row.models_truncated === true,
    input_tokens: pair ? input : null,
    output_tokens: pair ? output : null,
    cached_input_tokens: cached,
    tokens_state: pair ? oneOf(row.tokens_state, ['known', 'partial', 'unknown'] as const, 'unknown') : 'unknown',
    cached_state: cached ? oneOf(row.cached_state, ['known', 'partial', 'unknown'] as const, 'unknown') : 'unknown',
    estimated_cost_usd: costState === 'unknown' ? null : cost,
    cost_state: cost == null ? 'unknown' : costState,
    unknown_token_models: count(row.unknown_token_models),
    unknown_cost_models: count(row.unknown_cost_models),
  }
}

export function parseTicketAgentWork(value: unknown): TicketAgentWork {
  if (!value || typeof value !== 'object') throw new Error('Agent work response was not usable.')
  const row = value as Record<string, unknown>
  if (!Array.isArray(row.sessions) || !row.totals || typeof row.totals !== 'object') throw new Error('Agent work response was not usable.')
  const totals = row.totals as Record<string, unknown>
  const input = integerString(totals.input_tokens)
  const output = integerString(totals.output_tokens)
  const cached = integerString(totals.cached_input_tokens)
  const cost = decimalString(totals.estimated_cost_usd)
  const costState = oneOf(totals.cost_state, ['estimated', 'provisional', 'partial', 'unknown'] as const, 'unknown')
  return {
    node_id: text(row.node_id) ?? '',
    kind: text(row.kind) ?? '',
    currency: text(row.currency) ?? 'USD',
    usage_available: row.usage_available === true,
    includes_descendants: row.includes_descendants === true,
    scope_truncated: row.scope_truncated === true,
    list_truncated: row.list_truncated === true,
    sessions: row.sessions.map(session),
    totals: {
      session_count: count(totals.session_count),
      input_tokens: input,
      output_tokens: output,
      cached_input_tokens: cached,
      tokens_state: input == null || output == null ? 'unknown' : oneOf(totals.tokens_state, ['known', 'partial', 'unknown'] as const, 'unknown'),
      cached_state: cached == null ? 'unknown' : oneOf(totals.cached_state, ['known', 'partial', 'unknown'] as const, 'unknown'),
      estimated_cost_usd: costState === 'unknown' ? null : cost,
      cost_state: cost == null ? 'unknown' : costState,
      currency: text(totals.currency) ?? 'USD',
      duration_seconds: typeof totals.duration_seconds === 'number' && Number.isSafeInteger(totals.duration_seconds) && totals.duration_seconds >= 0 ? totals.duration_seconds : null,
      duration_state: oneOf(totals.duration_state, ['known', 'ongoing', 'partial', 'unknown'] as const, 'unknown'),
      unknown_token_sessions: count(totals.unknown_token_sessions),
      unknown_cost_sessions: count(totals.unknown_cost_sessions),
      unknown_token_models: count(totals.unknown_token_models),
      unknown_cost_models: count(totals.unknown_cost_models),
    },
  }
}

export async function loadTicketAgentWork(nodeId: string, signal?: AbortSignal): Promise<TicketAgentWork> {
  const response = await api(`/nodes/${encodeURIComponent(nodeId)}/agent-work`, signal ? { signal } : {})
  const textBody = await response.text()
  let data: unknown = null
  try { data = textBody ? JSON.parse(textBody) : null } catch { data = null }
  if (!response.ok) {
    const body = data && typeof data === 'object' ? data as Record<string, unknown> : {}
    const reason = typeof body.error === 'string' && body.error ? body.error : `Request failed (${response.status})`
    throw new APIError(response.status, reason, body)
  }
  return parseTicketAgentWork(data)
}
