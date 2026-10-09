// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'
import type { KeyTrimProposal } from './keyTrim'
import type { Approval } from './agents'
import type { DoctrineInboxItem } from './doctrine'
// P6 presentation model. Wire contracts live in decisionDeskApi.ts.
export type DeskOutcome = 'once' | 'always' | 'requirement' | 'doctrine'
export type DeskKind = 'question' | 'handover' | 'approval' | 'action' | 'rule' | 'tier' | 'key_trim'
// Outside the server option-ID alphabet, so an agent option cannot collide.
export const CUSTOM_ANSWER = '@custom'
export interface DeskChoice { id: string; title: string; description: string; answer: string; field?: boolean; unavailable?: string }
export interface DeskDoctrineTarget { source_id: string; path: string; rule_key: string; rule_sha256: string; tldr_en?: string; tldr_de?: string }
export interface DeskOutcomeAvailability { outcome: DeskOutcome; available: boolean; why: string; mapping_present?: boolean }
export interface DeskEffectReview { kind: 'criterion' | 'doctrine'; ref: string; why: string }
export interface DeskOutcomeEffect {
  id: string; revision: number; kind: 'inbox' | 'comment' | 'outcome'; state: 'pending' | 'delivered' | 'failed' | 'replaced'; deliver_after: string
  receipt_state?: 'queued' | 'handed_off' | 'failed'; receipt_failure?: string; error_code?: string; error_message?: string; effect_ref?: string; doctrine_state?: string
  effect_data?: { retryable?: boolean; review_required?: DeskEffectReview[]; ticket_id?: string; criterion?: string; knowledge_id?: string; doctrine_id?: string; supersedes?: string; superseded_by?: string }
}
export interface DeskItem {
  id: string; kind: DeskKind; projectId: string; projectName: string; ticketId?: string; ticketKey?: string
  title: string; context: string; findings: string; meanwhile: string; destination: string
  choices: DeskChoice[]; recommended?: string; why: string; outcome: DeskOutcome; suggestion: string
  revision: number; createdAt: string; expiresAt?: string; held: boolean; decided: boolean
  answer?: string; optionId?: string; reason?: string; delivery?: string; fromRecord?: string
  outcomes?: DeskOutcomeAvailability[]; doctrine?: DeskDoctrineTarget; outcomeEffects?: DeskOutcomeEffect[]
  unavailable?: string; prUrl?: string; source?: string; keyTrim?: KeyTrimProposal; approval?: Approval; rule?: DoctrineInboxItem
}
export interface DeskDraft { optionId: string; answer: string; reason: string; outcome: DeskOutcome; dirty: boolean }
export const outcomeLabels: Record<DeskOutcome, string> = { once: 'Once', always: 'Always', requirement: 'Requirement', doctrine: 'Doctrine' }
export const kindLabels: Record<DeskKind, string> = { question: 'Question', handover: 'Handover question', approval: 'Approval', action: 'Action request', rule: 'Rule change', tier: 'Tier request', key_trim: 'Key trim approval' }
export function draftFor(item: DeskItem): DeskDraft {
  const protectedChoice = item.kind === 'approval' || item.kind === 'tier' || item.kind === 'key_trim'
  return { optionId: item.optionId ?? (protectedChoice ? '' : item.recommended ?? item.choices[0]?.id ?? ''), answer: item.answer ?? '', reason: item.reason ?? '', outcome: item.outcome, dirty: false }
}
export function answerFor(item: DeskItem, draft: DeskDraft): string {
  const choice = item.choices.find(option => option.id === draft.optionId)
  return (choice?.field ? draft.answer : choice?.answer ?? draft.answer).trim()
}
export function outcomeUnavailable(item: DeskItem, outcome: DeskOutcome): string {
  if (item.kind !== 'question' && item.kind !== 'handover') return 'This action uses its own protected decision flow.'
  const capability = item.outcomes?.find(row => row.outcome === outcome)
  // Older servers offered Once only. Publishing always needs a current server
  // capability; the agent's suggested stamp is never authority.
  if (!capability) return outcome === 'once' && !item.outcomes ? '' : `${outcomeLabels[outcome]} availability could not be confirmed.`
  if (!capability.available) return capability.why || `${outcomeLabels[outcome]} is unavailable to this person.`
  if (outcome === 'doctrine' && (!capability.mapping_present || !item.doctrine)) return 'Doctrine needs a confirmed source, rule and exact base mapping.'
  return ''
}
/** IDs freeze at an explicit round boundary. Refresh only updates arrivals. */
export function newRound(items: DeskItem[]): string[] { return items.map(item => item.id) }
export function arrivals(items: DeskItem[], round: string[]): DeskItem[] {
  const ids = new Set(round)
  return items.filter(item => !ids.has(item.id) && !item.decided)
}
export function roundCounts(items: DeskItem[], round: string[], skipped: Set<string>) {
  const byId = new Map(items.map(item => [item.id, item]))
  let decided = 0, open = 0, skip = 0
  for (const id of round) {
    if (byId.get(id)?.decided) decided++
    else if (skipped.has(id)) skip++
    else open++
  }
  return { decided, open, skipped: skip }
}
export function fieldTarget(target: EventTarget | null): target is HTMLElement {
  return target instanceof HTMLElement && (!!target.closest('input, textarea, select, [contenteditable="true"], [role="textbox"]'))
}
export function macPlatform(platform: string): boolean { return /Mac|iPhone|iPad|iPod/i.test(platform) }
export function submitModifier(event: Pick<KeyboardEvent, 'metaKey' | 'ctrlKey' | 'altKey' | 'shiftKey'>, mac: boolean): boolean {
  return !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)
}

export interface DeskProjectionItem {
  id: string; kind: 'question' | 'approval' | 'action_request' | 'doctrine' | 'tier_request' | 'key_trim' | 'stepup'; project_id?: string
  revision: number; title: string; created_at: string; expires_at?: string; held: boolean; can_decide?: boolean; href: string; source: string
}
export interface DeskProjection {
  items: DeskProjectionItem[]; counts: { open: number; held: number; chores: number }
  has_more: boolean; next_cursor?: string; as_of: string
}

export function deskItemID(item: Pick<DeskProjectionItem, 'kind' | 'id'>): string {
  const prefix = { question: 'q', approval: 'a', action_request: 'm', doctrine: 'r', tier_request: 't', key_trim: 'k', stepup: 's' }[item.kind]
  return `${prefix}:${item.id}`
}
// Compatibility links are data, never a selector or a destination to execute.
export function deskLinkItem(value: unknown): string {
  return typeof value === 'string' && /^[qamrtks]:[a-zA-Z0-9][a-zA-Z0-9-]{0,79}$/.test(value) ? value : ''
}

export async function readDeskProjection(limit = 100, cursor?: string, signal?: AbortSignal): Promise<DeskProjection> {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 || (cursor?.length ?? 0) > 512) throw new Error('Invalid Decision Desk page.')
  const params = new URLSearchParams({ limit: String(limit), ...(cursor ? { cursor } : {}) })
  const response = await api(`/decision-desk/projection?${params}`, { signal })
  if (!response.ok) throw new APIError(response.status, `Decision Desk could not be read (${response.status}).`)
  // Bound bytes before decoding. The API timeout also covers its response body.
  const maxBytes = 2 * 1024 * 1024
  if (Number(response.headers.get('Content-Length')) > maxBytes) { await response.body?.cancel(); throw new Error('Decision Desk page is too large.') }
  if (!response.body) throw new Error('Decision Desk returned an empty projection.')
  const reader = response.body.getReader(), decoder = new TextDecoder()
  let bytes = 0, body = ''
  try {
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) break
      bytes += chunk.value.byteLength
      if (bytes > maxBytes) { await reader.cancel(); throw new Error('Decision Desk page is too large.') }
      body += decoder.decode(chunk.value, { stream: true })
    }
    body += decoder.decode()
  } finally { reader.releaseLock() }
  const page: DeskProjection = JSON.parse(body)
  if (!Array.isArray(page.items) || page.items.length > limit || !page.counts ||
    ![page.counts.open, page.counts.held, page.counts.chores].every(n => Number.isSafeInteger(n) && n >= 0) || typeof page.has_more !== 'boolean') {
    throw new Error('Decision Desk returned an invalid projection. Try again.')
  }
  return page
}

// Preserve the server order. Bounded coverage and a changing queue are explicit;
// a count never comes from this client-side list or from a page's item count.
export async function loadDeskProjection(signal?: AbortSignal, read = readDeskProjection): Promise<DeskProjection & { truncated: boolean }> {
  let page = await read(100, undefined, signal)
  const first = page
  const items: DeskProjectionItem[] = [], ids = new Set<string>(), cursors = new Set<string>()
  for (let n = 0; n < 10; n++) {
    for (const item of page.items) {
      const key = `${item.kind}:${item.id}`
      if (!ids.has(key)) { ids.add(key); items.push(item) }
    }
    if (!page.has_more) return { ...first, items, has_more: false, truncated: items.length !== first.counts.open }
    if (!page.next_cursor || cursors.has(page.next_cursor)) throw new Error('Decision Desk pagination did not advance. Try again.')
    if (n === 9) break
    cursors.add(page.next_cursor)
    page = await read(100, page.next_cursor, signal)
  }
  return { ...first, items, has_more: true, truncated: true }
}
