// SPDX-License-Identifier: AGPL-3.0-only
import type { IconName } from '../components/AppIcon.vue'
import { effortVerb, effortWord, routineState, waitReasonWords, type Recurrence, type RecurrenceTrigger, type RoutineScopeKind, type RoutineState } from './recurrences'
import { compactCount } from './usageFormat'

// The routine logbook in the UI (AEON-1192, S24): S21's closed run states, its usage
// and its evidence (AEON-1180), said in plain words. Only Done may look finished: a
// created ticket is never a finished run, and a failed run never shows the Done tint.

// ---------- S21 shapes (api/areas/recurrences.yaml) ----------
export type RunState = 'created' | 'running' | 'waiting_for_person' | 'held' | 'failed' | 'done' | 'skipped'
export interface RunMeasure { measured: string | null; known: number; unknown: number }
export interface RunUsage {
  tokens: RunMeasure; paid_microusd: string | null; estimated_api_usd: string | null
  reserved_tokens: string | null; reserved_microusd: string | null; reserved_recovery_ms: string | null
  partial: boolean; truncated: boolean; gaps: string[]
}
export interface RoutineRun {
  id: string; recurrence_id: string; definition_revision: number; owner_principal_id: string; state: RunState; reason: string
  work_node_id: string | null; work_order_id: string | null; agent_run_id: string | null; created_at: string; updated_at: string; usage: RunUsage
}
export interface RunAttempt { id: string; role: string; state: RunState; reason: string; agent_run_id: string | null; exit_confirmed: boolean; exit_code: number | null; created_at: string }
export interface RunAction {
  id: string; attempt_id: string | null; kind: string; state: string; target_node_id: string | null; evaluation_id: string | null
  evaluation_result: 'allow' | 'needs_person' | 'block' | null; approval_id: string | null; approval_state: 'pending' | 'approved' | 'denied' | 'revoked' | 'expired' | null
  effect_id: string | null; pull_request_number: number | null; created_at: string
}
export interface RunEffect { id: string; action_id: string | null; kind: string; state: string; created_at: string }
export interface EvidencePage<T> { items: T[]; next_cursor: string | null }
export interface RunPage { items: RoutineRun[]; next_cursor: string | null }
export interface RunDetail { run: RoutineRun; attempts: EvidencePage<RunAttempt>; actions: EvidencePage<RunAction>; effects: EvidencePage<RunEffect> }
/** The logbook reads 20 runs and 50 history entries a page. */
export const RUNS_PAGE = 20, HISTORY_PAGE = 50

// ---------- Run states ----------
export type Tone = 'ok' | 'wait' | 'bad' | 'live' | 'calm'
export const RUN_LOOK: Record<RunState, { icon: IconName | 'live'; tone: Tone }> = {
  created: { icon: 'ticket', tone: 'calm' }, running: { icon: 'live', tone: 'live' }, waiting_for_person: { icon: 'person-wait', tone: 'wait' },
  held: { icon: 'pause', tone: 'wait' }, failed: { icon: 'close', tone: 'bad' }, done: { icon: 'check', tone: 'ok' }, skipped: { icon: 'not', tone: 'calm' },
}
/** A state a newer server adds reads as held, like the server's own fallback for unknown outcomes. */
export const runState = (value: string): RunState => Object.hasOwn(RUN_LOOK, value) ? value as RunState : 'held'
const open = (state: RunState) => state === 'created' || state === 'running' || state === 'waiting_for_person' || state === 'held'
function sinceWords(at: string, now: number) {
  const minutes = Math.max(0, Math.floor((now - Date.parse(at)) / 60_000))
  return minutes < 1 ? 'under a minute' : minutes < 120 ? `${minutes} min` : `${Math.floor(minutes / 60)} h ${minutes % 60} min`
}
/** The run's headline: its state, never more finished than it is. */
export function runTitle(run: Pick<RoutineRun, 'state' | 'created_at'>, now: number): string {
  const state = runState(run.state)
  if (state === 'created') return 'Ticket created · no agent run'
  if (state === 'running') return `Running · ${sinceWords(run.created_at, now)} so far`
  return { waiting_for_person: 'Waiting for a person', held: 'Held', failed: 'Failed', done: 'Done', skipped: 'Skipped' }[state]
}
const sentence = (text: string) => text.trim().replace(/\.$/, '')
/** The line under the headline: when, the server's reason, and for a created ticket that it is not a finished run. */
export function runLine(run: Pick<RoutineRun, 'state' | 'reason'>, at: string, agent: boolean): string {
  const state = runState(run.state), parts = [at]
  if (state !== 'done' && state !== 'running' && run.reason) parts.push(sentence(run.reason))
  if (state === 'created' && agent) parts.push('a created ticket is not a finished run')
  return parts.join(' · ')
}

// ---------- Usage words (accounts never show; vendor only) ----------
// Paid evidence missing only makes the paid part unknown; these gaps make the token count a lower bound.
const TOKEN_GAPS = new Set(['execution_usage_unknown', 'source_visibility_incomplete', 'run_lineage_limit', 'measurement_incomplete'])
const withheld = (usage: RunUsage) => usage.gaps.includes('usage_visibility_withheld')
const nothingRan = (usage: RunUsage) => usage.gaps.includes('execution_usage_unknown') && usage.tokens.measured === null
export const tokensPartial = (usage: RunUsage) => usage.truncated || usage.tokens.unknown > 0 || usage.gaps.some(gap => TOKEN_GAPS.has(gap))
const positive = (value: string | null) => !!value && /^\d+$/.test(value) && /[1-9]/.test(value)
const zero = (value: string | null) => value !== null && /^0+$/.test(value)
const dollars = (value: number) => `$${value.toFixed(2)}`
const tokensOf = (value: string | null) => { const count = compactCount(value); return count === null ? null : `${count} tokens` }
// Missing usage is unknown, never zero. Only a run that never started, with no agent session
// in its lineage and a balance that reports nothing held and nothing paid, used nothing.
const idle = (state: RunState, u: RunUsage) => (state === 'held' || state === 'waiting_for_person') && nothingRan(u) && zero(u.reserved_tokens) && zero(u.reserved_microusd) && zero(u.reserved_recovery_ms) && !positive(u.paid_microusd)
/** The usage next to a run: unknown or partial usage is never shown as zero or as complete. */
export function usageShort(run: Pick<RoutineRun, 'state' | 'usage'>): { text: string; warn: boolean } {
  const state = runState(run.state), u = run.usage, tokens = tokensOf(u.tokens.measured), soFar = open(state) ? ' so far' : ''
  if (state === 'created' || state === 'skipped' || idle(state, u)) return { text: '', warn: false }
  if (tokens === null) return { text: 'Usage unknown', warn: true }
  if (tokensPartial(u)) return { text: `At least ${tokens}${soFar}`, warn: true }
  return { text: `${tokens}${soFar}`, warn: false }
}
/** The Settled step: measured facts, the paid part, the API list price and what stays reserved. */
export function settledWords(run: Pick<RoutineRun, 'state' | 'usage'>): { tone: StepTone; strong: string; text: string } {
  const state = runState(run.state), u = run.usage, tokens = tokensOf(u.tokens.measured), parts: string[] = []
  if (withheld(u)) return { tone: 'off', strong: '', text: 'Usage isn’t shown to you; it needs harness and outcome read rights in its project' }
  if (idle(state, u)) return { tone: 'off', strong: '', text: 'Nothing used, nothing held' }
  const reserved = [
    positive(u.reserved_tokens) ? `${tokensOf(u.reserved_tokens)} stay reserved until the run settles` : '', positive(u.reserved_microusd) ? `${dollars(Number(u.reserved_microusd) / 1_000_000)} stays reserved until the run settles` : '',
    u.reserved_tokens === null || u.reserved_microusd === null ? 'what stays reserved is unknown' : '',
  ].filter(Boolean)
  const estimate = u.estimated_api_usd !== null && Number.isFinite(Number(u.estimated_api_usd)) ? `about ${dollars(Number(u.estimated_api_usd))} at API list prices, not charged` : ''
  if (open(state)) {
    parts.push(tokens === null ? 'usage unknown so far' : tokensPartial(u) ? `at least ${tokens} so far` : `${tokens} so far`, ...reserved)
    return { tone: 'hold', strong: 'Not yet', text: parts.join(' · ') }
  }
  const paid = u.paid_microusd !== null && /^\d+$/.test(u.paid_microusd) ? `${dollars(Number(u.paid_microusd) / 1_000_000)} paid` : 'no paid charge recorded'
  if (tokens === null) return { tone: 'warn', strong: 'Usage unknown', text: [nothingRan(u) ? 'no agent session reported a count' : 'its agent sessions reported no count', paid, estimate, ...reserved].filter(Boolean).join(' · ') }
  if (tokensPartial(u)) return { tone: 'warn', strong: 'Partial usage', text: [`at least ${tokens}`, paid, estimate, ...reserved].filter(Boolean).join(' · ') }
  return { tone: 'ok', strong: '', text: [tokens, paid, estimate, ...reserved].filter(Boolean).join(' · ') }
}

// ---------- The run chain: ticket → agent → actions → check → person → settled ----------
export type StepTone = 'ok' | 'bad' | 'warn' | 'wait' | 'hold' | 'off' | 'live'
export type StepPart = { kind: 'text'; text: string } | { kind: 'strong'; text: string } | { kind: 'code'; text: string } | { kind: 'key'; text: string; to: string } | { kind: 'go'; text: string; to: string }
export interface ChainStep { id: string; tone: StepTone; label: string; parts: StepPart[] }
export const STEP_ICON: Record<StepTone, IconName | 'live'> = { ok: 'check', bad: 'close', warn: 'alert', wait: 'person-wait', hold: 'pause', off: 'minus', live: 'live' }
export interface ChainTicket { key: string; title: string; state: string; number: number | null; projectKey: string }
export interface ChainContext { ticket: ChainTicket | 'loading' | 'missing' | null; when: (at: string) => string; stateWord: (state: string) => string }
const text = (value: string): StepPart => ({ kind: 'text', text: value }), strong = (value: string): StepPart => ({ kind: 'strong', text: value })
const roleWord = (role: string) => { const word = effortWord(role).replaceAll('_', ' '); return word.charAt(0).toUpperCase() + word.slice(1) }
const ACTION_WORDS: Record<string, [string, string]> = {
  'work.create': ['Create a ticket', 'Created a ticket'], 'work.update': ['Update its ticket', 'Updated its ticket'], 'knowledge.write': ['Write a knowledge entry', 'Wrote a knowledge entry'],
  'pr.open': ['Open a pull request', 'Opened a pull request'], 'pipeline.request': ['Ask to ship through the pipeline', 'Asked to ship through the pipeline'],
}
function actionName(action: RunAction, done: boolean) {
  const words = ACTION_WORDS[action.kind] ?? [action.kind.replaceAll('_', ' '), action.kind.replaceAll('_', ' ')]
  const name = words[done ? 1 : 0]
  return action.kind === 'pr.open' && action.pull_request_number ? `${name.replace(/a pull request$/, 'pull request')} #${action.pull_request_number}` : name
}
const actionDone = (state: string) => ['succeeded', 'done', 'completed', 'applied'].includes(state)
function actionStep(action: RunAction): ChainStep {
  const id = `action-${action.id}`
  if (actionDone(action.state)) return { id, tone: 'ok', label: 'Actions', parts: [text(actionName(action, true))] }
  if (action.state === 'failed' || action.state === 'rejected') return { id, tone: 'bad', label: 'Actions', parts: [text(`${actionName(action, false)}: `), strong('failed'), text(' · nothing was filed')] }
  if (action.state === 'blocked') return { id, tone: 'bad', label: 'Actions', parts: [text(`${actionName(action, false)}: `), strong('blocked by a rule'), text(' · nothing was filed')] }
  if (action.state === 'needs_person') return { id, tone: 'wait', label: 'Actions', parts: [text(`${actionName(action, false)}: waits for a person`)] }
  if (action.state === 'pending') return { id, tone: 'hold', label: 'Actions', parts: [text(`${actionName(action, false)}: not decided yet`)] }
  return { id, tone: 'off', label: 'Actions', parts: [text(`${actionName(action, false)}: ${action.state.replaceAll('_', ' ')}`)] }
}
function checkStep(action: RunAction): ChainStep | null {
  const id = `check-${action.id}`, name = actionName(action, false)
  if (action.evaluation_result === 'allow') return { id, tone: 'ok', label: 'Check', parts: [text(`${name}: allowed`)] }
  if (action.evaluation_result === 'needs_person') return { id, tone: 'wait', label: 'Check', parts: [text(`${name}: `), strong('asks a person')] }
  if (action.evaluation_result === 'block') return { id, tone: 'bad', label: 'Check', parts: [text(`${name}: `), strong('blocked')] }
  return null
}
function personStep(action: RunAction): ChainStep | null {
  const id = `person-${action.id}`, name = actionName(action, false)
  if (action.approval_state === 'pending') return { id, tone: 'wait', label: 'Person', parts: [text(`${name}: waiting for a person · `), ...(action.approval_id ? [{ kind: 'go', text: 'Open on the Decision Desk', to: `/decision-desk?item=a:${encodeURIComponent(action.approval_id)}` } as StepPart] : [])] }
  if (action.approval_state === 'approved') return { id, tone: 'ok', label: 'Person', parts: [text(`${name}: approved`)] }
  if (action.approval_state) return { id, tone: 'bad', label: 'Person', parts: [text(`${name}: `), strong(action.approval_state)] }
  if (action.evaluation_result === 'needs_person') return { id, tone: 'wait', label: 'Person', parts: [text(`${name}: a person decides; the request isn’t visible to you`)] }
  return null
}
function agentStep(attempt: RunAttempt, when: (at: string) => string): ChainStep {
  const id = `attempt-${attempt.id}`, role = roleWord(attempt.role), state = runState(attempt.state)
  if (state === 'running') return { id, tone: 'live', label: 'Agent', parts: [text(`${role} · running since ${when(attempt.created_at)}`)] }
  if (attempt.exit_confirmed && attempt.exit_code === 0) return { id, tone: 'ok', label: 'Agent', parts: [text(`${role} · finished; its exit was confirmed`)] }
  if (attempt.exit_confirmed && attempt.exit_code !== null) return { id, tone: 'bad', label: 'Agent', parts: [text(`${role} · `), strong(`stopped with an error (exit ${attempt.exit_code})`)] }
  if (state === 'created') return { id, tone: 'off', label: 'Agent', parts: [text(`${role} · not started yet`)] }
  if (state === 'failed') return { id, tone: 'bad', label: 'Agent', parts: [text(`${role} · `), strong('failed'), text(` · ${sentence(attempt.reason || 'its exit wasn’t confirmed')}`)] }
  return { id, tone: 'warn', label: 'Agent', parts: [text(`${role} · exit `), strong('unknown'), text(` · ${sentence(attempt.reason || 'the process exit wasn’t confirmed')}`)] }
}
/** The chain for one run's evidence, oldest step first. truncated: an evidence page has more than shown. */
export function runChain(detail: RunDetail, ctx: ChainContext): { steps: ChainStep[]; truncated: boolean } {
  const run = detail.run, state = runState(run.state), steps: ChainStep[] = []
  const ticket = ctx.ticket
  if (!run.work_node_id) steps.push({ id: 'ticket', tone: 'off', label: 'Ticket', parts: [text(state === 'skipped' ? 'No ticket was created' : 'Its ticket isn’t visible to you')] })
  else if (ticket === 'loading' || ticket === null) steps.push({ id: 'ticket', tone: 'ok', label: 'Ticket', parts: [text('Reading its ticket…')] })
  else if (ticket === 'missing') steps.push({ id: 'ticket', tone: 'off', label: 'Ticket', parts: [text('Its ticket is no longer available')] })
  else steps.push({ id: 'ticket', tone: 'ok', label: 'Ticket', parts: [{ kind: 'key', text: ticket.key, to: `/p/${encodeURIComponent(ticket.projectKey)}/${encodeURIComponent(ticket.key)}` }, text(` ${ticket.title} · ${ctx.stateWord(ticket.state)}${ticket.number ? ` · #${ticket.number}` : ''}`)] })
  const attempts = [...detail.attempts.items].reverse(), actions = [...detail.actions.items].reverse(), effects = [...detail.effects.items].reverse()
  if (attempts.length) steps.push(...attempts.map(attempt => agentStep(attempt, ctx.when)))
  else if (state === 'held' || state === 'waiting_for_person') steps.push({ id: 'agent', tone: 'wait', label: 'Agent', parts: [text(`Waiting: ${sentence(run.reason || 'it hasn’t started')}`)] })
  else if (state === 'running') steps.push({ id: 'agent', tone: 'live', label: 'Agent', parts: [text('Starting')] })
  else steps.push({ id: 'agent', tone: 'off', label: 'Agent', parts: [text(state === 'created' || state === 'skipped' ? `Didn’t start${run.reason ? `: ${sentence(run.reason)}` : ''}` : 'No attempt recorded')] })
  const ran = attempts.length > 0 || state === 'running'
  if (state === 'created' || state === 'skipped') return { steps, truncated: false }
  if (ran || actions.length) {
    if (actions.length) steps.push(...actions.map(actionStep))
    else steps.push({ id: 'actions', tone: 'off', label: 'Actions', parts: [text('No actions recorded')] })
    const checks = actions.map(checkStep).filter((step): step is ChainStep => !!step)
    steps.push(...(checks.length ? checks : [{ id: 'check', tone: 'off', label: 'Check', parts: [text(actions.length ? 'No checks recorded' : 'Nothing to check: no actions recorded')] } as ChainStep]))
    const people = actions.map(personStep).filter((step): step is ChainStep => !!step)
    steps.push(...(people.length ? people : [{ id: 'person', tone: 'off', label: 'Person', parts: [text('Not needed')] } as ChainStep]))
    for (const effect of effects) {
      if (effect.state === 'failed') steps.push({ id: `effect-${effect.id}`, tone: 'bad', label: 'Effect', parts: [text(`${effect.kind.replaceAll('_', ' ')}: `), strong('failed'), text(' · nothing was filed')] })
      else if (effect.state === 'pending') steps.push({ id: `effect-${effect.id}`, tone: 'hold', label: 'Effect', parts: [text(`${effect.kind.replaceAll('_', ' ')}: waiting to be applied`)] })
    }
  }
  const settled = settledWords(run)
  steps.push({ id: 'settled', tone: settled.tone, label: 'Settled', parts: [...(settled.strong ? [strong(settled.strong), text(settled.text ? ` · ${settled.text}` : '')] : [text(settled.text)])] })
  return { steps, truncated: !!(detail.attempts.next_cursor || detail.actions.next_cursor || detail.effects.next_cursor) }
}
/** A run page or evidence answer the logbook can trust; anything else is a read failure, never an empty logbook. */
export function checkedRuns(page: unknown): RunPage {
  const value = page as RunPage | null
  if (!value || !Array.isArray(value.items) || !(value.next_cursor === null || typeof value.next_cursor === 'string')) throw new Error('The logbook answer was not a run page.')
  return { items: value.items.map(run => ({ ...run, state: runState(run.state) })), next_cursor: value.next_cursor }
}
export function checkedDetail(detail: unknown, runId: string): RunDetail {
  const value = detail as RunDetail | null
  const page = (part: unknown) => !!part && Array.isArray((part as EvidencePage<unknown>).items)
  if (!value || value.run?.id !== runId || !page(value.attempts) || !page(value.actions) || !page(value.effects)) throw new Error('The run’s evidence could not be read.')
  return value
}

// ---------- Rows on Agents › Routines ----------
export type ScopeFilter = 'all' | RoutineScopeKind
export const SCOPE_FILTERS: readonly [ScopeFilter, string][] = [['all', 'All'], ['project', 'Projects'], ['personal', 'Yours'], ['workspace', 'Workspace']]
export const STATE_FILTERS: readonly RoutineState[] = ['running', 'waiting', 'held', 'failed', 'stopped', 'scheduled']
export const STATE_LOOK: Record<RoutineState, { icon: IconName | 'live'; tone: Tone }> = {
  scheduled: { icon: 'clock', tone: 'calm' }, running: { icon: 'live', tone: 'live' }, waiting: { icon: 'person-wait', tone: 'wait' },
  held: { icon: 'pause', tone: 'wait' }, stopped: { icon: 'stop', tone: 'calm' }, failed: { icon: 'close', tone: 'bad' },
}
export const scopeOf = (item: Pick<Recurrence, 'definition'>): RoutineScopeKind => item.definition?.scope.kind ?? 'project'
export const matchesScope = (item: Pick<Recurrence, 'definition'>, scope: ScopeFilter) => scope === 'all' || scopeOf(item) === scope
export function parseScope(value: unknown): ScopeFilter { return SCOPE_FILTERS.some(([key]) => key === value) ? value as ScopeFilter : 'all' }
export function parseState(value: unknown): RoutineState | '' { return STATE_FILTERS.includes(value as RoutineState) ? value as RoutineState : '' }
/** Counts per state; the loaded pages only, so a remaining cursor reads as "at least". */
export function stateCounts(items: readonly Recurrence[]): Record<RoutineState, number> {
  const counts = Object.fromEntries(STATE_FILTERS.map(state => [state, 0])) as Record<RoutineState, number>
  for (const item of items) counts[routineState(item)]++
  return counts
}
export const countWords = (count: number, more: boolean) => `${count}${more ? '+' : ''}`
const DAY_SHORT: Record<string, string> = { MO: 'Mon', TU: 'Tue', WE: 'Wed', TH: 'Thu', FR: 'Fri', SA: 'Sat', SU: 'Sun' }
/** The trigger in a few words for a row; the logbook head says it in full, with its zone. */
export function triggerShort(trigger: RecurrenceTrigger): string {
  if (trigger.kind === 'event') return ({ 'release.published': 'After each release', 'node.done': 'When work is done', 'knowledge.changed': 'When knowledge changes', 'external.tag': 'After a signed tag', 'external.deploy': 'After a signed deploy' } as Record<string, string>)[trigger.event || 'release.published'] ?? 'After an event'
  const rule = Object.fromEntries((trigger.rrule ?? '').split(';').map(part => part.split('='))) as Record<string, string>
  const at = trigger.time_of_day ?? ''
  if (rule.FREQ === 'WEEKLY') { const days = (rule.BYDAY ?? '').split(',').map(day => DAY_SHORT[day]).filter(Boolean); return `${days.length === 7 ? 'Daily' : days.join(', ') || 'Weekly'} ${at}`.trim() }
  if (rule.FREQ === 'MONTHLY') return `${rule.BYMONTHDAY === '-1' ? 'Last day' : `Day ${rule.BYMONTHDAY || '1'}`}${rule.INTERVAL && rule.INTERVAL !== '1' ? ` every ${rule.INTERVAL} months` : ' monthly'} ${at}`.trim()
  return `Daily ${at}`.trim()
}
export function whoWords(item: Pick<Recurrence, 'definition'>): string {
  const a = item.definition?.assignment
  return a ? `an agent ${effortVerb(a.role)}${a.runtime_requirements.needs_browser ? ' · browser' : ''}` : 'ticket only'
}
/** The next run in a row: the date in the routine's zone, or what it waits for. */
export function nextWords(item: Pick<Recurrence, 'paused' | 'trigger' | 'next_at'>, when: (at: string) => string): string {
  if (item.paused) return 'Paused'
  if (item.trigger.kind === 'event') return ({ 'release.published': 'Next release', 'node.done': 'Next done work', 'knowledge.changed': 'Next change', 'external.tag': 'Next signed tag', 'external.deploy': 'Next signed deploy' } as Record<string, string>)[item.trigger.event || 'release.published'] ?? 'Next event'
  return item.next_at ? when(item.next_at) : 'No next date'
}
export interface RowNoteContext { me: string; ownerName?: string; project?: string; when: (at: string) => string }
/** The reason sentence under a row's state chip. */
export function rowNote(item: Recurrence, ctx: RowNoteContext): string {
  const state = routineState(item), wait = item.execution_policy?.wait_reason ?? '', run = item.last_run
  if (!item.definition?.assignment) {
    if (item.paused) return 'Paused'
    const last = item.last_result
    return last ? `Last: ${last.outcome === 'skipped' ? 'skipped' : last.key || 'a ticket'} · #${last.number}` : 'Nothing created yet'
  }
  if (state === 'stopped') return 'Paused'
  if (state === 'waiting') {
    if (wait === 'execution_consent_required') return item.definition.owner_principal_id === ctx.me ? 'You to let agents run it' : `${ctx.ownerName || 'Its owner'} decides`
    return run?.reason ? sentence(run.reason) : 'A person decides'
  }
  if (state === 'held') return wait && wait !== 'execution_consent_required' ? sentence(waitReasonWords(wait, { project: ctx.project })) : run?.reason ? sentence(run.reason) : 'Held'
  if (state === 'running') return run ? `Since ${ctx.when(run.created_at)}` : 'Running'
  if (state === 'failed') return run?.reason ? sentence(run.reason) : 'Its last run failed'
  // Scheduled: the last run is created, done or skipped; a created ticket is not called done.
  if (run) return `Last run: ${({ created: 'ticket created', done: 'done', skipped: 'skipped' } as Record<string, string>)[run.state] ?? 'held'} · ${ctx.when(run.created_at)}`
  return 'No runs yet'
}
/** What the Agents page's Routines line says; null when nothing waits or failed. */
export function routinesLine(items: readonly Recurrence[], more: boolean, when: (item: Recurrence) => string): string | null {
  const counts = stateCounts(items), waiting = counts.waiting, failed = counts.failed
  if (!waiting && !failed) return null
  const next = items.filter(item => !item.paused && item.trigger.kind === 'time' && item.next_at).sort((a, b) => Date.parse(a.next_at!) - Date.parse(b.next_at!))[0]
  const plus = more ? '+' : ''
  return [waiting ? `${waiting}${plus} ${waiting === 1 && !more ? 'waits' : 'wait'} for a person` : '', failed ? `${failed}${plus} failed` : '', next ? `next ${when(next)}` : ''].filter(Boolean).join(' · ')
}
