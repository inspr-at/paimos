// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1212: opening is a choice made once, from the shared projection. Admission
// is history of a real start, never a preflight or a promise of current capacity.
import type { GateKind, LeadDecision } from './lead.ts'
import type { HarnessSession, ModelProfile } from './agents.ts'
import { modelNames, nameOf } from '../components/agents/sessionRow.ts'

// The panel and row resolve the same registry name, fallback and pinned level.
export function panelModelLine(session: Pick<HarnessSession, 'harness' | 'model' | 'reasoning_effort'>, profiles: readonly ModelProfile[], fallback = ''): string {
  const identity = session.model?.trim() || fallback.trim()
  if (!identity) return ''
  const named = nameOf(identity, session.harness, modelNames(profiles))
  return [named.name, session.reasoning_effort?.trim() || named.level].filter(Boolean).join(' · ')
}

export type LeadTab = 'lead' | 'session' | 'chat'
export type PanelTab = LeadTab | 'overview'
export const TAB_LABEL: Record<PanelTab, string> = { lead: 'Lead', session: 'Session', chat: 'Chat', overview: 'Overview' }
export function leadTab(value: unknown): LeadTab | null { return value === 'lead' || value === 'session' || value === 'chat' ? value : null }
export function panelQueryTab(value: unknown, lead: boolean): PanelTab | null {
  const tab = Array.isArray(value) ? value[0] : value
  if (tab === 'messages' || tab === 'chat') return 'chat'
  if (tab === 'overview' || tab === 'session') return lead ? 'session' : 'overview'
  return lead && tab === 'lead' ? 'lead' : null
}
export function openingTab(input: { forced?: PanelTab | null; lead: boolean; asksYou: boolean; held: boolean; ended: boolean; workerProblem: boolean; workerLost: boolean; pinned?: LeadTab | null; last?: PanelTab | null }): { tab: PanelTab; why: string } {
  if (input.forced) return { tab: input.forced, why: 'from this link' }
  if (!input.lead) return { tab: input.last === 'chat' ? 'chat' : 'overview', why: '' }
  const need = input.asksYou ? { tab: 'chat' as const, why: 'it asks you something' }
    : input.held ? { tab: 'lead' as const, why: 'a start is held' }
    : input.ended ? { tab: 'lead' as const, why: 'its session ended' }
    : input.workerLost ? { tab: 'lead' as const, why: 'a worker has no heartbeat' }
    : input.workerProblem ? { tab: 'lead' as const, why: 'a worker has a problem' } : null
  if (need) return { ...need, why: need.why + (input.pinned && input.pinned !== need.tab ? ` · ${TAB_LABEL[input.pinned]} is your default` : '') }
  if (input.pinned) return { tab: input.pinned, why: 'your default' }
  const last = leadTab(input.last)
  return { tab: last ?? 'lead', why: last ? 'last used · no default set' : 'Lead first' }
}

const LABEL: Record<GateKind, string> = { dial: 'Dial', harness: 'Harness limits', account_room: 'Account room', host_load: 'Host load' }
export interface AdmissionRow { kind: GateKind; label: string; state: 'passed' | 'held' | 'unreadable' | 'unknown'; detail: string; reason?: string }
export interface AdmissionHistory { caption: string; tone: 'rest' | 'held' | 'unreadable' | 'stale'; rows: AdmissionRow[]; note: string }
const FULL: Record<GateKind, string> = { dial: 'the dial was full', harness: 'a harness limit was reached', account_room: 'no account had room', host_load: 'the computer was too busy' }
const REASON_KIND: Record<string, GateKind> = { dial: 'dial', harness: 'harness', account: 'account_room', account_room: 'account_room', host: 'host_load', host_load: 'host_load' }
interface AdmissionReason { kind?: GateKind; state: AdmissionRow['state']; text: string }
const OTHER_REASONS: Record<string, AdmissionReason> = {
  daily_limit: { kind: 'account_room', state: 'held', text: 'the daily cap was reached' },
  daily_cap: { kind: 'account_room', state: 'held', text: 'the daily cap was reached' },
  daily_limit_unknown: { kind: 'account_room', state: 'unreadable', text: 'the daily cap could not be checked' },
  capacity_full: { kind: 'account_room', state: 'held', text: FULL.account_room },
  capacity_unavailable: { kind: 'account_room', state: 'unreadable', text: 'account room could not be read' },
  forecast_unavailable: { kind: 'account_room', state: 'unreadable', text: 'account room could not be read' },
  admission_unavailable: { state: 'unreadable', text: 'the start checks could not be read' },
  start_checks_unavailable: { state: 'unreadable', text: 'the start checks could not be read' },
}
function admissionReason(code: string): AdmissionReason | undefined {
  const known = Object.hasOwn(OTHER_REASONS, code) ? OTHER_REASONS[code] : undefined
  if (known) return known
  const match = /^(dial|harness|account_room|account|host_load|host)_(full|unavailable|unreadable|stale)$/.exec(code)
  if (!match) return undefined
  const kind = REASON_KIND[match[1]!]!
  if (match[2] === 'full') return { kind, state: 'held', text: FULL[kind] }
  return { kind, state: match[2] === 'stale' ? 'unknown' : 'unreadable', text: `${LABEL[kind].toLowerCase()} ${match[2] === 'stale' ? 'was out of date' : 'could not be read'}` }
}
const time = (at: number) => new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
export function admissionHistory(decisions: readonly LeadDecision[], input: { complete: boolean; error: string; sessionStarted?: string | null; now: number }): AdmissionHistory {
  const empty = (caption: string, tone: AdmissionHistory['tone'], note: string): AdmissionHistory => ({ caption, tone, rows: [], note })
  if (input.error) return empty("Couldn't read the last start", 'unreadable', input.error)
  if (!input.complete) return empty('Reading the last start…', 'rest', '')
  const report = [...decisions].reverse().find(d => d.request.stage === 'admission')
  if (!report) return empty('No start yet', 'rest', 'The checks run when this lead starts its next worker.')
  const at = Date.parse(report.recorded_at)
  const kinds = [...new Set([...(report.request.gates ?? []).map(g => g.kind), ...report.gate_freshness.map(g => g.kind)])].filter(k => k in LABEL)
  if (!Number.isFinite(at) || !kinds.length) return empty("Couldn't read the last start", 'unreadable', 'The admission report has no readable gate evidence.')
  const reasons = [...new Set([...report.reason_codes, ...report.request.reason_codes])]
  const unspecified = reasons.find(reason => reason !== 'gates_ready' && !admissionReason(reason)?.kind)
  const rows = kinds.map((kind): AdmissionRow => {
    const gate = report.request.gates?.find(g => g.kind === kind), fresh = report.gate_freshness.find(g => g.kind === kind)?.freshness
    const candidates = reasons.filter(reason => admissionReason(reason)?.kind === kind)
    // Keep a reported daily/account cause ahead of the gate's generic full code.
    const reason = candidates.find(reason => Object.hasOwn(OTHER_REASONS, reason)) ?? candidates[0] ?? (gate?.state === 'full' || report.outcome === 'wait' && kind === kinds[0] ? unspecified : undefined)
    const words = reason ? admissionReason(reason) : undefined
    const held = gate?.state === 'full' || words?.state === 'held'
    const unreadable = gate?.state === 'unreadable' || fresh === 'unreadable' || words?.state === 'unreadable'
    const passed = !reason && (gate?.state === 'ready' || fresh === 'fresh')
    return { kind, label: LABEL[kind], state: held ? 'held' : unreadable ? 'unreadable' : passed ? 'passed' : 'unknown',
      detail: held ? `${LABEL[kind]} held it: ${words?.text ?? (reason ? 'a check said no' : FULL[kind])}` : unreadable ? `${LABEL[kind]} couldn't be read at that start` : passed ? LABEL[kind] : `${LABEL[kind]}: ${words?.text ?? (reason ? 'a check said no' : 'evidence unknown')}`,
      ...(reason ? { reason } : {}) }
  })
  const sessionAt = Date.parse(input.sessionStarted ?? ''), stale = at < sessionAt || input.now - at > 24 * 60 * 60_000 || report.gate_freshness.some(g => g.freshness === 'stale')
  if (stale) return { caption: `Last start: ${new Date(at).toLocaleDateString('en-GB', { weekday: 'short', day: 'numeric', month: 'short' })}, ${time(at)} · may be out of date`, tone: 'stale', rows, note: '' }
  if (rows.some(row => row.state === 'unreadable')) return empty("Couldn't read the last start", 'unreadable', `Unreadable evidence: ${rows.filter(row => row.state === 'unreadable').map(row => row.label).join(', ')}.`)
  const held = rows.filter(row => row.state === 'held').length
  return { caption: held ? `${held} held the last start · ${time(at)}` : rows.every(row => row.state === 'passed') ? `All passed at the last start · ${time(at)}` : `Last start · ${time(at)} · evidence ${rows.some(row => row.state === 'unreadable') ? 'unreadable' : 'unknown'}`,
    tone: held ? 'held' : rows.some(row => row.state === 'unreadable') ? 'unreadable' : 'rest', rows, note: '' }
}
