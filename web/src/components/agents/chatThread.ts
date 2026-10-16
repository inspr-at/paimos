// SPDX-License-Identifier: AGPL-3.0-only
// The thread a person reads (AEON-1438): day separators, one Decision Desk card
// per delivered question, at most one chip per message, and a receipt only on
// the viewer's latest message. A Desk delivery keeps the agent's instruction in
// the data (internal/questions/delivery.go); a person sees the card, never the
// question id or the CLI words.
import { parseJson } from '../../lib/json.ts'
import type { ProjectMessage } from '../../lib/agents.ts'
import type { MessageGroup } from './sessionMessages.ts'

const UUID = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
// The synthetic question root the server records for the asking agent.
const DESK_QUESTION = new RegExp(`^Decision Desk question (${UUID}); read through ask status$`, 'i')
const QUESTION_ID = new RegExp(`^${UUID}$`, 'i')
// An answer body is a small JSON record; anything larger is ordinary text.
const ANSWER_LIMIT = 64 * 1024

export interface DeskDelivery { kind: 'question' | 'answer'; questionId: string; answer?: string; correction?: boolean }

// A Desk delivery is either the question root or the recorded answer (or its
// correction). Everything else is an ordinary message.
export function deskDelivery(message: Pick<ProjectMessage, 'body'>): DeskDelivery | null {
  const body = (message.body ?? '').trim()
  const root = DESK_QUESTION.exec(body)
  if (root) return { kind: 'question', questionId: root[1]!.toLowerCase() }
  if (!body.startsWith('{') || body.length > ANSWER_LIMIT) return null
  let value: unknown
  try { value = parseJson(body) } catch { return null }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const record = value as Record<string, unknown>
  if (record.type !== 'question.answer' && record.type !== 'question.correction') return null
  if (typeof record.question_id !== 'string' || !QUESTION_ID.test(record.question_id)) return null
  const answer = typeof record.answer === 'string' ? record.answer.trim() : ''
  return { kind: 'answer', questionId: record.question_id.toLowerCase(), correction: record.type === 'question.correction', ...(answer ? { answer } : {}) }
}

export interface DayRow { type: 'day'; key: string; label: string }
export interface MessageRow { type: 'message'; key: string; message: MessageGroup }
// One card per question: the first delivery places it, later ones fold in.
// members lists every post folded into the card; lastEvent is the newest of them.
export interface DeskRow { type: 'desk'; key: string; message: MessageGroup; questionId: string; answer?: string; corrected: boolean; members: string[]; lastEvent: number }
export type ThreadRow = DayRow | MessageRow | DeskRow

export type ThreadLanguage = 'en' | 'de'
const dayWords = { en: { today: 'Today', yesterday: 'Yesterday' }, de: { today: 'Heute', yesterday: 'Gestern' } } as const
const locale = (language: ThreadLanguage) => language === 'de' ? 'de-AT' : 'en-GB'
const localDay = (ms: number) => { const d = new Date(ms); return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}` }
const midnight = (ms: number) => { const d = new Date(ms); d.setHours(0, 0, 0, 0); return d.getTime() }

// "Today", "Yesterday", else the weekday and date; another year adds the year.
export function dayLabel(at: number, now: number, language: ThreadLanguage): string {
  const days = Math.round((midnight(now) - midnight(at)) / 86_400_000)
  if (days === 0) return dayWords[language].today
  if (days === 1) return dayWords[language].yesterday
  const sameYear = new Date(at).getFullYear() === new Date(now).getFullYear()
  return new Intl.DateTimeFormat(locale(language), { weekday: 'long', day: 'numeric', month: 'long', ...(sameYear ? {} : { year: 'numeric' }) }).format(at)
}

// The time on the meta line: the clock, 24 hours.
export function clockTime(iso: string | undefined): string {
  const at = Date.parse(iso ?? '')
  return Number.isFinite(at) ? new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }) : ''
}

export function threadRows(items: readonly MessageGroup[], now: number, language: ThreadLanguage): ThreadRow[] {
  const rows: ThreadRow[] = []
  const desks = new Map<string, DeskRow>()
  let day = ''
  for (const message of items) {
    const desk = deskDelivery(message)
    const known = desk ? desks.get(desk.questionId) : undefined
    if (desk && known) {
      known.members.push(...message.members)
      known.lastEvent = Math.max(known.lastEvent, message.last_event)
      if (desk.answer) known.answer = desk.answer
      if (desk.correction) known.corrected = true
      continue
    }
    const at = Date.parse(message.created_at ?? '')
    if (Number.isFinite(at) && localDay(at) !== day) {
      day = localDay(at)
      rows.push({ type: 'day', key: `day-${day}`, label: dayLabel(at, now, language) })
    }
    if (!desk) { rows.push({ type: 'message', key: message.id, message }); continue }
    const row: DeskRow = { type: 'desk', key: message.id, message, questionId: desk.questionId, corrected: !!desk.correction, members: [...message.members], lastEvent: message.last_event, ...(desk.answer ? { answer: desk.answer } : {}) }
    desks.set(desk.questionId, row)
    rows.push(row)
  }
  return rows
}

// Receipts appear only on the viewer's latest own message in the thread. A
// Desk answer is shown as its card, so it never carries the receipt.
export function receiptTarget(rows: readonly ThreadRow[], me: string): string | undefined {
  for (let i = rows.length - 1; i >= 0; i--) {
    const row = rows[i]!
    if (row.type === 'message' && row.message.sender_principal_id === me) return row.message.id
  }
  return undefined
}

export interface ChipWords { actionRequest: string; awaitingReply: string; resolved: string; dismissed: string; resolvedState: string; dismissedState: string; awaitingState: string }
// At most one chip: an action request names its state after a dot.
export function messageChip(message: Pick<MessageGroup, 'is_action_request' | 'reply_obligation' | 'answered' | 'human_resolution_outcome'>, words: ChipWords): string {
  const awaiting = message.reply_obligation === 'open' && !message.answered
  if (message.is_action_request) {
    const state = message.human_resolution_outcome === 'resolved' ? words.resolvedState
      : message.human_resolution_outcome === 'dismissed' ? words.dismissedState
        : awaiting ? words.awaitingState : ''
    return state ? `${words.actionRequest} · ${state}` : words.actionRequest
  }
  if (message.human_resolution_outcome === 'resolved') return words.resolved
  if (message.human_resolution_outcome === 'dismissed') return words.dismissed
  return awaiting ? words.awaitingReply : ''
}
