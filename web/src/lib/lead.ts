// SPDX-License-Identifier: AGPL-3.0-only
// AEON-741: the project lead as people see it. One lead per project; the server's
// lifecycle (AEON-734) is authoritative and every write carries its revision.
// Inside PAIMOS it is always "lead"; people read the workspace word (LeadWords).
import { reactive } from 'vue'
import { api, APIError } from './api.ts'
import type { Question, QuestionPage } from './decisionDeskApi.ts'

export type LeadState = 'none' | 'starting' | 'working' | 'waiting_for_room' | 'paused' | 'cannot_start'
export interface ProjectLead {
  project_id: string; revision: number; generation: number; session_id: string | null
  state: LeadState; reason: string; process_active: boolean
}
export type GateKind = 'dial' | 'harness' | 'account_room' | 'host_load'
export type DecisionStage = 'queue' | 'admission' | 'review' | 'release_handoff'
export type DecisionOutcome = 'selected' | 'wait' | 'requested' | 'passed' | 'failed' | 'handoff' | 'partial'
export interface LeadDecision {
  event_id: number; project_id: string; session_id: string; recorded_at: string
  request: { ticket_node_id?: string; stage: DecisionStage; outcome: DecisionOutcome; reason_codes: string[]; attempt: number; gates?: { kind: GateKind; state: 'ready' | 'full' | 'unreadable'; observed_at?: string | null }[] }
  outcome: DecisionOutcome; reason_codes: string[]
  gate_freshness: { kind: GateKind; freshness: 'fresh' | 'stale' | 'unreadable' }[]
  results: { kind: 'run' | 'review' | 'release'; id: string }[]
}
interface DecisionPage { items: LeadDecision[]; next_after: number | null }
export interface LeadOverride { allowed_host_ids?: string[] | null; allowed_account_ids?: string[] | null; work_kind_id?: string | null; bucket?: 'normal' | 'complex' | null; recovery?: { max_attempts: number; agent_hours: number } | null }
export interface LeadSettings {
  revision: number; details_redacted: boolean; automatic_launch_enabled: false
  owner_person_id: string | null; overrides?: LeadOverride; effective?: LeadOverride
  model_selector?: { cell?: { mode: string; family?: string; line?: string; effort?: string; harness?: string }; set_by?: string } | null
  wait_reason?: 'acceptance_pending' | 'owner_unavailable' | 'selector_unavailable'
  required_start_gates?: string[]
}
async function leadRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(response.status, typeof data.error === 'string' ? data.error : `The lead request failed (${response.status})`, data)
  return data as T
}
const project = (id: string) => `/projects/${encodeURIComponent(id)}`
export const readLead = (id: string) => leadRequest<ProjectLead>(`${project(id)}/lead`)
/** Requests a lead; launches nothing. Also the restart after a confirmed stop. */
export const startLead = (id: string, revision: number) => leadRequest<ProjectLead>(`${project(id)}/lead`, 'POST', { expected_revision: revision })
/** Checkpoints and pauses: new dispatch stops now; the process keeps its slot until it exits. */
export const pauseLead = (id: string, revision: number, generation: number) => leadRequest<ProjectLead>(`${project(id)}/lead/pause`, 'POST', { expected_revision: revision, generation })
export const readLeadSettings = (id: string) => leadRequest<LeadSettings>(`${project(id)}/lead-settings`)
/** Revision-checked; only the owning person may narrow the project's hosts. */
export const writeLeadSettings = (id: string, revision: number, overrides: LeadOverride) => leadRequest<LeadSettings>(`${project(id)}/lead-settings`, 'PUT', { revision, overrides })
export function modelByRole(settings: LeadSettings | null): string {
  const cell = settings?.model_selector?.cell
  if (!cell || cell.mode === 'auto' || !(cell.line || cell.family)) return ''
  return [cell.line || cell.family, cell.effort && `${cell.effort} thinking`].filter(Boolean).join(', ')
}
/** The project's whole history across lead generations, oldest first: at most `pages` × 200 decisions per call. */
export async function readLeadDecisions(id: string, after = 0, pages = 5): Promise<{ items: LeadDecision[]; after: number; caughtUp: boolean }> {
  const items: LeadDecision[] = []
  let cursor = after
  for (let page = 0; page < pages; page++) {
    const result = await leadRequest<DecisionPage>(`${project(id)}/lead-decisions?limit=200&after=${cursor}`)
    if (result.items.length > 200) throw new Error('The lead history page exceeded its limit.')
    items.push(...result.items)
    if (result.next_after === null || result.next_after <= cursor) return { items, after: items.at(-1)?.event_id ?? cursor, caughtUp: true }
    cursor = result.next_after
  }
  return { items, after: cursor, caughtUp: false }
}
/** Open questions in one project, at most `pages` × 100; `more` says some were not read. */
export async function readOpenQuestions(id: string, pages = 5): Promise<{ items: Question[]; more: boolean }> {
  const items = new Map<string, Question>()
  for (let page = 0; page < pages; page++) {
    let result: QuestionPage
    try {
      result = await leadRequest<QuestionPage>(`${project(id)}/questions?state=open&limit=100&offset=${page * 100}`)
      if (result.items.length > 100) throw new Error('The question page exceeded its limit.')
    } catch (e) {
      // A later page failed: keep what was read and report the rest as unread.
      if (page === 0) throw e
      return { items: [...items.values()], more: true }
    }
    for (const question of result.items) items.set(question.id, question)
    if (!result.has_more) return { items: [...items.values()], more: false }
  }
  return { items: [...items.values()], more: true }
}

// ---------- Words ----------
export interface LeadWords { S: string; l: string; P: string; pl: string }
// Acronyms stay as typed ("AI"), ordinary words read lower case mid-sentence.
const lower = (word: string) => word === word.toUpperCase() ? word : word.toLowerCase()
export function pluralWord(word: string): string {
  return /s$/i.test(word) ? word : /[^aeiou]y$/i.test(word) ? `${word.slice(0, -1)}ies` : `${word}s`
}
export function leadWords(singular = '', plural = ''): LeadWords {
  const S = singular.trim() || 'Lead'
  const P = plural.trim() || (singular.trim() ? pluralWord(S) : 'Leads')
  return { S, l: lower(S), P, pl: lower(P) }
}
// The workspace word (AEON-791): Settings › Vocabulary › Agent names saves it
// with the work vocabulary; every screen reads it from here, reactively.
export const LEAD_WORDS: LeadWords = reactive(leadWords())
export function setLeadWords(names?: { singular: string; plural: string } | null): void {
  Object.assign(LEAD_WORDS, leadWords(names?.singular, names?.plural))
}

// ---------- State copy ----------
export type LeadAction = 'start' | 'cancel' | 'pause' | 'resume' | 'dial' | 'computers' | 'none'
export interface LeadBandModel {
  state: LeadState; tone: 'live' | 'wait' | 'warn' | 'rest'; busy: boolean
  title: string; status: string; detail: string; now: string; foot: string; action: LeadAction; actionLabel: string
}
const GATE_WORD: Record<string, string> = { dial: 'the dial', harness: 'a harness limit', account: 'account room', host: 'host load' }
export function waitCopy(reason: string): { status: string; now: string; action: LeadAction } {
  const gate = /^(dial|harness|account|host)_(full|unavailable)$/.exec(reason)
  if (gate) {
    const [, kind, kindState] = gate
    const word = GATE_WORD[kind!]!
    if (kindState === 'full') return kind === 'dial'
      ? { status: 'Waiting for room · the dial is full', now: 'It starts by itself when an agent finishes anywhere, or when you raise the dial.', action: 'dial' }
      : { status: `Waiting for room · ${word} is full`, now: 'It starts by itself when room frees up. Nothing is lost while it waits; the queue keeps its order.', action: kind === 'host' ? 'computers' : 'dial' }
    return { status: `Waiting · ${word} can’t be read`, now: `While ${word} can’t be read, nothing new starts. Running work continues.`, action: kind === 'host' ? 'computers' : 'dial' }
  }
  switch (reason) {
    case 'awaiting_generation': return { status: 'Requested · waiting for its session', now: 'Nothing runs yet. It starts when a lead session on one of your computers takes over; queued work keeps its order.', action: 'none' }
    case 'start_checks_unavailable': return { status: 'Waiting · start checks can’t be read', now: 'Before every start PAIMOS checks the dial, harness limits, account room and host load. One of them can’t be read, so nothing new starts.', action: 'dial' }
    case 'generation_unavailable': return { status: 'Waiting · its session stopped reporting', now: 'Its session hasn’t reported for a while. Nothing new starts until it reports again or stops.', action: 'none' }
    case 'project_turn_wait': return { status: 'Waiting for its turn', now: 'Other projects have waited longer. It gets the next turn within the dial; nothing is lost.', action: 'none' }
    case 'work_not_eligible': return { status: 'Waiting · no queued work is ready', now: 'Queued work needs an estimate, acceptance criteria or a free blocker before it can start.', action: 'none' }
    case 'worker_priority': return { status: 'Waiting · a worker goes first', now: 'It yielded its slot so a waiting worker can start first.', action: 'none' }
    case 'capacity_full': return { status: 'Waiting for room', now: 'No account has room right now. It starts by itself when room frees up.', action: 'dial' }
    case 'capacity_unavailable': case 'forecast_unavailable': return { status: 'Waiting · room can’t be measured', now: 'Account room can’t be measured right now, so nothing new starts.', action: 'dial' }
    default: return { status: 'Waiting for room', now: 'Nothing new starts until every start check passes.', action: 'dial' }
  }
}
function pausedCopy(lead: ProjectLead, w: LeadWords): { status: string; now: string } {
  if (!lead.session_id) return { status: 'Paused before it started', now: 'No session had started. Queued work stays queued; nothing new starts.' }
  if (lead.process_active) return lead.reason === 'handover_delivery_unavailable'
    ? { status: 'Pausing · the handover request can’t be delivered', now: `No new work starts. The ${w.l} keeps its slot until its session confirms it stopped.` }
    : { status: 'Pausing · handing over', now: `No new work starts. The ${w.l} finishes its step, writes a handover and keeps its slot until it stops.` }
  if (lead.reason === 'idle_yield') return { status: 'Paused · nothing to do', now: 'It checkpointed while the queue was empty. Resume restarts it through the usual start checks.' }
  return { status: 'Paused · handover saved', now: 'Queued work stays queued; nothing new starts. Resume continues from the saved handover.' }
}
export function leadBand(lead: ProjectLead | null, projectKey: string, queued: number | null, w: LeadWords = LEAD_WORDS): LeadBandModel {
  const name = `${projectKey} ${w.l}`
  const checks = 'Every start is checked: dial, harness limits, account room and host load. Anything unreadable means no start.'
  if (!lead || lead.state === 'none') return {
    state: 'none', tone: 'wait', busy: false, title: `No ${w.l} in ${projectKey}`,
    status: queued === null ? 'The queue can’t be read right now' : queued ? `${queued} queued work ${queued === 1 ? 'item waits' : 'items wait'} for one` : 'Nothing is queued yet',
    detail: '', now: '', foot: 'One per project. Host, accounts and models come from Settings; nothing to fill in.', action: 'start', actionLabel: `Start ${w.l}`,
  }
  switch (lead.state) {
    case 'starting': return { state: lead.state, tone: 'live', busy: true, title: name, status: 'Starting', detail: '', now: 'Reading the queue and the project. Nothing has started yet.', foot: checks, action: 'pause', actionLabel: 'Cancel start' }
    case 'working': return { state: lead.state, tone: 'live', busy: true, title: name, status: 'Working', detail: '', now: '', foot: checks, action: 'pause', actionLabel: 'Pause…' }
    case 'waiting_for_room': {
      const copy = waitCopy(lead.reason)
      return { state: lead.state, tone: 'wait', busy: !!lead.session_id, title: name, status: copy.status, detail: '', now: copy.now,
        foot: lead.session_id ? 'Nothing is lost while it waits; the queue keeps its order.' : checks,
        action: lead.session_id ? copy.action : 'cancel', actionLabel: lead.session_id ? (copy.action === 'computers' ? 'Check computers' : copy.action === 'dial' ? 'Open dial' : '') : 'Cancel start' }
    }
    case 'paused': {
      const copy = pausedCopy(lead, w)
      return { state: lead.state, tone: 'rest', busy: false, title: name, status: copy.status, detail: '', now: copy.now,
        foot: lead.process_active ? 'Resume becomes available once its session has stopped.' : `Resume restarts the ${w.l} through the usual start checks.`,
        action: 'resume', actionLabel: 'Resume' }
    }
    case 'cannot_start': return { state: lead.state, tone: 'warn', busy: false, title: name, status: 'Can’t start new work',
      detail: '', now: lead.reason === 'project_archived' ? 'This project is archived. Unarchive it to start work again.' : lead.reason === 'owner_revoked' ? `The person who started this ${w.l} no longer has permission to run it. Its owner must start it again.` : 'A required permission or project setting is missing.',
      foot: 'Queued work stays queued.', action: lead.reason === 'owner_revoked' ? 'start' : 'none', actionLabel: lead.reason === 'owner_revoked' ? `Start ${w.l}` : '' }
  }
  return leadBand(null, projectKey, queued, w)
}
/** Resume is a restart through admission and is only possible after a confirmed stop. */
export const canResume = (lead: ProjectLead | null) => !!lead && lead.state === 'paused' && !lead.process_active
export const canPause = (lead: ProjectLead | null) => !!lead && lead.revision > 0 && ['starting', 'working', 'waiting_for_room'].includes(lead.state)

// ---------- The line ----------
export type StationId = 'queued' | 'working' | 'gate' | 'merged'
export interface Station { id: StationId; label: string; count: number | null; keys: string[]; more: number; tone: '' | 'live' | 'hold' | 'done' }
export interface LineInput {
  state: LeadState; queued: string[] | null; waiting: boolean; working: string[]; gate: string[] | null; merged: string[] | null
}
const station = (id: StationId, label: string, keys: string[] | null, tone: Station['tone'], shown = 2): Station =>
  ({ id, label, count: keys ? keys.length : null, keys: (keys ?? []).slice(0, shown), more: Math.max(0, (keys?.length ?? 0) - shown), tone: keys?.length ? tone : '' })
export function leadLine(input: LineInput): Station[] {
  const live = input.state === 'working' || input.state === 'waiting_for_room' || input.state === 'starting'
  return [
    station('queued', 'queued', input.queued, input.waiting ? 'hold' : ''),
    station('working', 'working', input.working, live ? 'live' : '', 1),
    station('gate', 'at the gate', input.gate, live ? 'live' : '', 1),
    station('merged', 'merged today', input.merged, 'done'),
  ]
}

// ---------- Decisions: what the lead reported ----------
/** Per-ticket totals folded as pages arrive, so trimming the retained decisions never loses them. */
export interface DecisionFold { review: Record<string, DecisionOutcome>; handoff: Record<string, string> }
export const emptyFold = (): DecisionFold => ({ review: {}, handoff: {} })
/** Folds oldest-first decisions: the latest review per ticket and its latest release handoff. */
export function foldDecisions(fold: DecisionFold, decisions: LeadDecision[]): DecisionFold {
  for (const d of decisions) {
    const id = d.request.ticket_node_id
    if (!id) continue
    if (d.request.stage === 'review') fold.review[id] = d.outcome
    else if (d.request.stage === 'release_handoff' && d.outcome === 'handoff') fold.handoff[id] = d.recorded_at
  }
  return fold
}
const sameDay = (iso: string, now: Date) => { const d = new Date(iso); return d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate() }
/** Latest review decision per ticket says whether it waits at the gate. */
export const gateOf = (fold: DecisionFold) => Object.entries(fold.review).filter(([, outcome]) => outcome === 'requested').map(([id]) => id)
/** Release handoffs happen after the merge; today's handoffs are today's merges. */
export const mergedOf = (fold: DecisionFold, now = new Date()) => Object.entries(fold.handoff).filter(([, at]) => sameDay(at, now)).map(([id]) => id)
export const atGate = (decisions: LeadDecision[]) => gateOf(foldDecisions(emptyFold(), decisions))
export const mergedToday = (decisions: LeadDecision[], now = new Date()) => mergedOf(foldDecisions(emptyFold(), decisions), now)
export function decisionLine(d: LeadDecision, key: (id: string) => string): string {
  const t = d.request.ticket_node_id ? key(d.request.ticket_node_id) : 'a ticket'
  const why = d.reason_codes.map(code => code.replaceAll('_', ' ')).join(', ')
  switch (d.request.stage) {
    case 'queue': return d.outcome === 'selected' ? `Picked up ${t} from the queue` : d.outcome === 'wait' ? `Waited for queued work${why ? `: ${why}` : ''}` : `Queue: ${d.outcome} for ${t}`
    case 'admission': return d.outcome === 'selected' || d.outcome === 'passed' ? `Started a worker on ${t}` : d.outcome === 'wait' ? `Waited before starting ${t}${why ? `: ${why}` : ''}` : `Start ${d.outcome} for ${t}`
    case 'review': return d.outcome === 'requested' ? `Asked for a cross-family review of ${t}` : d.outcome === 'passed' ? `${t} passed its cross-family review` : d.outcome === 'failed' ? `${t} needs changes after its review` : `Review ${d.outcome} for ${t}`
    case 'release_handoff': return d.outcome === 'handoff' ? `Merged ${t} and handed it to release` : `Release handoff ${d.outcome} for ${t}`
  }
  return `${d.outcome} for ${t}`
}
export type CheckState = 'ok' | 'full' | 'unreadable' | 'unknown'
export interface StartCheck { kind: GateKind; label: string; state: CheckState; detail: string }
const GATE_REASON: Record<GateKind, string> = { dial: 'dial', harness: 'harness', account_room: 'account', host_load: 'host' }
const GATE_LABEL: Record<GateKind, string> = { dial: 'Dial', harness: 'Harness limits', account_room: 'Account room', host_load: 'Host load' }
/** One row per mandatory gate: the current wait reason wins, then the lead's latest admission report.
 *  A report is a reading taken at that start, never a promise about the next one. */
export function startChecks(lead: ProjectLead | null, decisions: LeadDecision[], dial: { running: number; total: number } | null): StartCheck[] {
  const admission = [...decisions].reverse().find(d => d.request.stage === 'admission' && d.gate_freshness.length)
  const at = admission ? new Date(admission.recorded_at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : ''
  return (['dial', 'harness', 'account_room', 'host_load'] as GateKind[]).map(kind => {
    const reason = lead?.reason ?? '', prefix = GATE_REASON[kind]
    const reported = admission?.gate_freshness.find(g => g.kind === kind)?.freshness
    // A fresh reading can still be a full gate: the server records it as a reason code.
    const reportedFull = !!admission && (admission.reason_codes.includes(`${kind}_full`) || !!admission.request.gates?.some(g => g.kind === kind && g.state === 'full'))
    const dialText = kind === 'dial' && dial ? `${dial.running} of ${dial.total} running` : ''
    if (reason === `${prefix}_full`) return { kind, label: GATE_LABEL[kind], state: 'full', detail: dialText ? `${dial!.running} of ${dial!.total} · full` : 'Full' }
    if (reason === `${prefix}_unavailable` || reported === 'unreadable' || reported === 'stale') return { kind, label: GATE_LABEL[kind], state: 'unreadable', detail: reported === 'stale' ? 'Last reading too old' : 'Can’t be read' }
    if (reported === 'fresh' && reportedFull) return { kind, label: GATE_LABEL[kind], state: 'full', detail: `Full at ${at}` }
    if (reported === 'fresh') return { kind, label: GATE_LABEL[kind], state: 'ok', detail: dialText || `Passed at ${at}` }
    return { kind, label: GATE_LABEL[kind], state: 'unknown', detail: dialText || 'Checked at the next start' }
  })
}
/** The heading over the checks: past readings never read as current availability. */
export function checksSummary(checks: StartCheck[]): string {
  if (checks.some(c => c.state === 'unreadable')) return 'A check can’t be read'
  if (checks.some(c => c.state === 'full')) return 'A limit is full'
  if (checks.length && checks.every(c => c.state === 'ok')) return 'All passed at the last start'
  return 'Checked again at every start'
}

// ---------- A ticket on the line ----------
export type StepId = 'queued' | 'sized' | 'working' | 'gate' | 'merged'
export interface TicketStep { id: StepId; label: string; note: string; state: '' | 'now' | 'done' }
export interface TicketStepInput {
  queuedPosition: number | null; queuedAt: string | null; estimateHours: number | null; status: 'open' | 'progress' | 'qa' | 'done' | 'other'
  worker: string | null; review: 'none' | 'running' | 'passed' | 'changes'
}
export function ticketSteps(input: TicketStepInput, w: LeadWords = LEAD_WORDS): TicketStep[] {
  const done = input.status === 'done'
  const gate = !done && (input.review === 'running' || input.review === 'changes' || input.status === 'qa')
  const started = done || gate || input.status === 'progress' || input.review === 'passed'
  const waiting = input.queuedPosition !== null && !started
  const at = input.queuedAt ? new Date(input.queuedAt).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : ''
  const hours = input.estimateHours ? `${input.estimateHours} h` : ''
  return [
    { id: 'queued', label: 'Queued', note: waiting ? (input.queuedPosition === 1 ? 'Next in line' : `#${input.queuedPosition} in line`) : at, state: waiting ? 'now' : started ? 'done' : '' },
    { id: 'sized', label: 'Sized', note: started ? hours : hours ? `${hours} estimated` : `When the ${w.l} picks it up`, state: started ? 'done' : '' },
    { id: 'working', label: 'Working', note: input.worker ?? '', state: done || gate || input.review === 'passed' ? 'done' : started ? 'now' : '' },
    { id: 'gate', label: 'Gate', note: input.review === 'changes' ? 'Changes requested' : input.review === 'passed' ? 'Review passed' : 'Cross-family review', state: done || (input.review === 'passed' && !gate) ? 'done' : gate ? 'now' : '' },
    { id: 'merged', label: 'Merged', note: '', state: done ? 'done' : '' },
  ]
}
