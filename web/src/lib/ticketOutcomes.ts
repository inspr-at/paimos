// SPDX-License-Identifier: AGPL-3.0-only

import { api } from './api'

export interface OutcomeEvent {
  id: string
  kind: string
  ticket_key: string
  rules_version: string | null
  release_title: string | null
  payload: Record<string, unknown>
  recorded_at: string
}

export interface OutcomeLine {
  id: string
  title: string
  detail: string
  full: string
  at: string
}

export async function loadTicketOutcomes(ticketId: string, signal?: AbortSignal): Promise<OutcomeEvent[]> {
  const response = await api(`/outcomes?ticket_node_id=${encodeURIComponent(ticketId)}&limit=40`, { signal })
  if (response.status === 404) return []
  if (!response.ok) throw new Error('Outcomes could not be loaded.')
  const body = await response.json() as { outcomes?: unknown }
  if (!body || !Array.isArray(body.outcomes)) return []
  return body.outcomes.flatMap(item => {
    const parsed = asOutcome(item)
    return parsed ? [parsed] : []
  })
}

export function outcomeLine(item: OutcomeEvent): OutcomeLine {
  const payload = item.payload ?? {}
  const title = lineTitle(item.kind, payload, item.release_title)
  const detail = joinDetail(detailParts(item.kind, payload), item.rules_version)
  return { id: item.id, title, detail, full: detail ? `${title}. ${detail}` : title, at: item.recorded_at }
}

function lineTitle(kind: string, payload: Record<string, unknown>, releaseTitle: string | null): string {
  switch (kind) {
    case 'review_verdict':
      return verdictTitle(text(payload.verdict))
    case 'fix_round': {
      const round = whole(payload.round)
      return round == null ? 'Fix round' : `Fix round ${round}`
    }
    case 'ci_result':
      return payload.result === 'fail' ? 'CI failed' : payload.result === 'pass' ? 'CI passed' : 'CI result'
    case 'revert':
      return 'Reverted'
    case 'ticket_done':
      return marked(text(payload.to_state))
    case 'released': {
      const title = (releaseTitle ?? '').trim()
      return title ? `Released in ${title}` : 'Released'
    }
    default: {
      const words = kind.replace(/[_-]+/g, ' ').trim()
      return words ? words.charAt(0).toUpperCase() + words.slice(1) : 'Outcome'
    }
  }
}

function detailParts(kind: string, payload: Record<string, unknown>): string[] {
  switch (kind) {
    case 'review_verdict':
      return [text(payload.route), text(payload.reviewer_model), text(payload.author_family), counted('round', payload.round), counted('blocking', payload.blocking_count), counted('finding', payload.findings), text(payload.summary)]
    case 'fix_round':
      return [text(payload.summary)]
    case 'ci_result':
      return [pullRequest(payload), text(payload.name), text(payload.summary)]
    case 'revert':
      return [text(payload.target), text(payload.summary)]
    case 'ticket_done':
      return [elapsed(payload.elapsed_seconds)]
    case 'released':
      return [text(payload.version)]
    default:
      return []
  }
}

function verdictTitle(verdict: string): string {
  switch (verdict) {
    case 'ok': return 'Review ok'
    case 'changes': return 'Changes requested'
    case 'pass': return 'Review passed'
    case 'fail': return 'Review failed'
    default: return 'Review'
  }
}

function pullRequest(payload: Record<string, unknown>): string {
  const repo = text(payload.repo)
  const number = whole(payload.number)
  if (repo && number != null) return `${repo} #${number}`
  return repo
}

function marked(state: string): string {
  switch (state) {
    case 'done': return 'Marked done'
    case 'accepted': return 'Marked accepted'
    case 'delivered': return 'Marked delivered'
    default: return state ? `Marked ${state}` : 'Marked done'
  }
}

function elapsed(value: unknown): string {
  const n = whole(value)
  if (n == null) return ''
  const hours = Math.floor(n / 3600)
  const minutes = Math.floor((n % 3600) / 60)
  const seconds = n % 60
  const parts: string[] = []
  if (hours) parts.push(hours === 1 ? '1 hour' : `${hours} hours`)
  if (minutes) parts.push(minutes === 1 ? '1 minute' : `${minutes} minutes`)
  if (seconds || !parts.length) parts.push(seconds === 1 ? '1 second' : `${seconds} seconds`)
  return parts.join(' ')
}

function counted(label: string, value: unknown): string {
  const n = whole(value)
  if (n == null) return ''
  if (label === 'finding') return n === 1 ? '1 finding' : `${n} findings`
  if (label === 'blocking') return n === 1 ? '1 blocking' : `${n} blocking`
  return `${label} ${n}`
}

function joinDetail(parts: string[], rules: string | null): string {
  const items = parts.map(part => part.trim()).filter(Boolean)
  const version = (rules ?? '').trim()
  if (version) items.push(`rules ${version}`)
  return items.join(' · ')
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function whole(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : null
}

function asOutcome(value: unknown): OutcomeEvent | null {
  if (!value || typeof value !== 'object') return null
  const item = value as Partial<OutcomeEvent>
  if (typeof item.id !== 'string' || typeof item.kind !== 'string' || typeof item.recorded_at !== 'string') return null
  const payload = item.payload && typeof item.payload === 'object' ? item.payload : {}
  return {
    id: item.id,
    kind: item.kind,
    ticket_key: typeof item.ticket_key === 'string' ? item.ticket_key : '',
    rules_version: typeof item.rules_version === 'string' ? item.rules_version : null,
    release_title: typeof item.release_title === 'string' ? item.release_title : null,
    payload,
    recorded_at: item.recorded_at,
  }
}
