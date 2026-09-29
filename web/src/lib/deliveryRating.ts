// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'

export interface DeliverySignals {
  review_rounds: number
  ci_failures: number
  reverts: number
}

export interface DeliveryVote {
  score: number
  tags: string[]
  comment: string
  updated_at: string
}

export interface SessionRating {
  session_id: string
  votes: number
  average: string | null
  signals: DeliverySignals
  mine: DeliveryVote | null
  harness: string
}

const tags = ['quality', 'rework', 'taste'] as const
const averagePattern = /^(?:[1-4]\.\d{2}|5\.00)$/

export function formatRating(value: string | null | undefined): string {
  if (!value || !averagePattern.test(value)) return ''
  return value.replace(/\.00$/, '').replace(/(\.\d)0$/, '$1')
}

export function signalLine(signals: DeliverySignals | null | undefined): string {
  if (!signals) return ''
  const parts = [
    signals.review_rounds > 0 ? plural(signals.review_rounds, 'review round', 'review rounds') : '',
    signals.ci_failures > 0 ? plural(signals.ci_failures, 'CI failure', 'CI failures') : '',
    signals.reverts > 0 ? plural(signals.reverts, 'revert', 'reverts') : '',
  ].filter(Boolean)
  return parts.join(' · ')
}

export function crowdLine(votes: number, average: string | null): string {
  if (votes < 2) return ''
  const shown = formatRating(average)
  return shown ? `${shown} from ${votes}` : ''
}

export function parseNodeRatings(value: unknown): SessionRating[] | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const sessions = (value as { sessions?: unknown }).sessions
  if (!Array.isArray(sessions)) return null
  const out: SessionRating[] = []
  for (const item of sessions) {
    const rating = parseRating(item)
    if (!rating) return null
    out.push(rating)
  }
  return out
}

export function parseRating(value: unknown): SessionRating | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const row = value as Record<string, unknown>
  if (typeof row.session_id !== 'string' || row.session_id === '') return null
  if (typeof row.votes !== 'number' || !Number.isInteger(row.votes) || row.votes < 0) return null
  if (row.average !== null && (typeof row.average !== 'string' || !averagePattern.test(row.average))) return null
  const signals = parseSignals(row.signals)
  if (!signals) return null
  const mine = parseMine(row.mine)
  if (mine === undefined) return null
  return {
    session_id: row.session_id,
    votes: row.votes,
    average: row.average as string | null,
    signals,
    mine,
    harness: typeof row.harness === 'string' ? row.harness : '',
  }
}

export async function loadNodeRatings(nodeId: string, signal?: AbortSignal): Promise<SessionRating[] | null> {
  const response = await api(`/nodes/${encodeURIComponent(nodeId)}/delivery-ratings`, { signal })
  if (!response.ok) return null
  return parseNodeRatings(await response.json().catch(() => null))
}

export async function loadSessionRating(sessionId: string, signal?: AbortSignal): Promise<SessionRating | null> {
  const response = await api(`/harness-sessions/${encodeURIComponent(sessionId)}/delivery-rating`, { signal })
  if (!response.ok) return null
  return parseRating(await response.json().catch(() => null))
}

export async function saveSessionRating(sessionId: string, body: { score: number; tags: string[]; comment: string }): Promise<SessionRating> {
  const response = await api(`/harness-sessions/${encodeURIComponent(sessionId)}/delivery-rating`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const payload = await response.json().catch(() => null)
  const rating = parseRating(payload)
  if (!response.ok || !rating) {
    const message = payload && typeof payload === 'object' && typeof (payload as { error?: unknown }).error === 'string'
      ? (payload as { error: string }).error
      : 'Could not save the rating'
    throw new Error(message)
  }
  return rating
}

function parseSignals(value: unknown): DeliverySignals | null {
  if (!value || typeof value !== 'object') return null
  const row = value as Record<string, unknown>
  const review = count(row.review_rounds)
  const ci = count(row.ci_failures)
  const reverts = count(row.reverts)
  if (review === null || ci === null || reverts === null) return null
  return { review_rounds: review, ci_failures: ci, reverts }
}

function parseMine(value: unknown): DeliveryVote | null | undefined {
  if (value === null) return null
  if (!value || typeof value !== 'object') return undefined
  const row = value as Record<string, unknown>
  if (typeof row.score !== 'number' || !Number.isInteger(row.score) || row.score < 1 || row.score > 5) return undefined
  if (!Array.isArray(row.tags) || row.tags.length > tags.length) return undefined
  const seen = new Set<string>()
  for (const tag of row.tags) {
    if (typeof tag !== 'string' || !tags.includes(tag as typeof tags[number]) || seen.has(tag)) return undefined
    seen.add(tag)
  }
  if (typeof row.comment !== 'string') return undefined
  return { score: row.score, tags: row.tags as string[], comment: row.comment, updated_at: typeof row.updated_at === 'string' ? row.updated_at : '' }
}

function count(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : null
}

function plural(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`
}
