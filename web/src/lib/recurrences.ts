// SPDX-License-Identifier: AGPL-3.0-only
import type { ListItem } from './api'
import { parseEstimate } from './estimates'
import { harnessLabel } from './agentState'
import { fullName, lineFallback, type RegistryProfile } from './modelsSimple'

export interface RecurrenceTrigger {
  kind: 'time' | 'event'; rrule?: string; time_of_day?: string; timezone?: string; start_date?: string
  event?: 'release.published' | 'node.done' | 'knowledge.changed' | 'external.tag' | 'external.deploy'; event_start?: 'now' | 'hour' | 'morning'; event_timezone?: string
  filter?: { project_ids?: string[]; has_release_copy?: boolean; exclude_hidden?: boolean; entry_id?: string; knowledge_type?: string; tag?: string }
  external?: { principal_id: string; public_key: string }
}
export interface RecurrenceTemplate {
  name?: string; title: string; description: string; acceptance_criteria: string[]; estimate_hours: number
  priority: string; tags: string[]; type: 'work' | 'ticket' | 'task' | 'epic'
  pill_en?: string; pill_de?: string; benefit_en?: string; benefit_de?: string; hide_from_release_notes?: boolean
}
export interface RecurrenceInput {
  project_id: string; parent_id: string; template: RecurrenceTemplate; trigger: RecurrenceTrigger
  queue_each: boolean; overlap_policy: 'skip' | 'create'; catch_up_policy: 'one'
  // AEON-1084: optional routine intent. Omitted keeps a legacy definition as saved.
  definition?: RoutineDefinition | null
}
export interface Recurrence extends RecurrenceInput {
  id: string; paused: boolean; revision: number; occurrence_count: number; next_at: string | null
  created_at: string; updated_at: string; last_result?: RecurrenceResult; open_previous?: RecurrenceResult
  created_by_principal_id?: string; execution_policy?: RoutineExecutionPolicy
  // R0 (S21 vocabulary): the latest run, when the server sends it.
  last_run?: { id: string; state: string; reason: string; created_at: string }
}
export interface RecurrenceResult {
  recurrence_id: string; occurrence_key: string; number: number; scheduled_at: string; created_at: string
  node_id: string | null; source_event_id: number | null; outcome: 'created' | 'skipped'; reason: string
  key?: string; title?: string; state?: string
}
export interface RecurrenceHistoryEntry {
  id: number; type: string; at: string; actor_name: string
  before: Recurrence | null; after: Recurrence | RecurrenceResult | null
  node: { key: string; title: string; state: string } | null
}
export interface RecurrenceRelease { key: string; name: string; version: string; published_at: string; receipt?: RecurrenceResult }
export type HistoryFilter = 'all' | 'created' | 'skipped' | 'changes'
export interface RecurrencePreview { times: string[]; trigger_kind: 'time' | 'event'; execution_policy?: RoutineExecutionPolicy }
export const copyRecurrenceInput = (value: RecurrenceInput): RecurrenceInput => ({ ...value, ...(value.definition !== undefined ? { definition: copyDefinition(value.definition) } : {}), template: { ...value.template, tags: [...value.template.tags], acceptance_criteria: [...value.template.acceptance_criteria] }, trigger: { ...value.trigger, ...(value.trigger.filter ? { filter: { ...value.trigger.filter, ...(value.trigger.filter.project_ids ? { project_ids: [...value.trigger.filter.project_ids] } : {}) } } : {}), ...(value.trigger.external ? { external: { ...value.trigger.external } } : {}) } })
export const recurrenceName = (item: Pick<Recurrence, 'template'>) => item.template.name || item.template.title
export const recurrenceZone = (trigger: RecurrenceTrigger) => trigger.timezone || trigger.event_timezone || 'UTC'
export const weekdays = [['MO', 'Monday'], ['TU', 'Tuesday'], ['WE', 'Wednesday'], ['TH', 'Thursday'], ['FR', 'Friday'], ['SA', 'Saturday'], ['SU', 'Sunday']] as const
export const zones = ['Europe/Vienna', 'Europe/London', 'UTC', 'America/New_York', 'America/Los_Angeles', 'Asia/Tokyo', 'Australia/Sydney']
export function ruleParts(rule = ''): Record<string, string> { return Object.fromEntries(rule.split(';').map(part => part.split('='))) }
export function triggerWords(trigger: RecurrenceTrigger) {
  if (trigger.kind === 'event') return ({ 'release.published': 'After every release is published', 'node.done': 'After matching work reaches Done', 'knowledge.changed': 'After matching knowledge changes', 'external.tag': 'After a signed external tag', 'external.deploy': 'After a signed external deploy' }[trigger.event || 'release.published']) + (trigger.event_start === 'hour' ? ', an hour later' : trigger.event_start === 'morning' ? ', next morning at 06:00' : '')
  const rule = ruleParts(trigger.rrule)
  let words = 'Every day'
  if (rule.FREQ === 'WEEKLY') words = `Every ${weekdays.filter(([day]) => rule.BYDAY?.split(',').includes(day)).map(([, label]) => label).join(', ') || 'week'}`
  if (rule.FREQ === 'MONTHLY') words = `${!rule.INTERVAL || rule.INTERVAL === '1' ? 'Monthly' : `Every ${rule.INTERVAL} months`} on ${rule.BYMONTHDAY === '-1' ? 'the last day' : `day ${rule.BYMONTHDAY || 'of the start date'}`}`
  return `${words} at ${trigger.time_of_day} · ${trigger.timezone}`
}
export function when(at: string, zone = 'UTC') {
  return new Intl.DateTimeFormat('en-GB', { timeZone: zone, weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(new Date(at))
}
export function renderTitle(template: string, number: number | string, at: string, zone: string, release?: Pick<RecurrenceRelease, 'name' | 'version'>) {
  const parts = Object.fromEntries(new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(new Date(at)).map(part => [part.type, part.value]))
  const values: Record<string, string> = { occurrence: String(number), date: `${parts.year}-${parts.month}-${parts.day}`, release_name: release?.name || '', release_version: release?.version || '' }
  return template.replace(/\{\{([^{}]*)\}\}/g, (whole, key: string) => values[key] ?? whole)
}
export const reasonWords = (reason: string) => ({ previous_occurrence_open: 'The previous occurrence was still open.', target_unavailable: 'The parent, project or a tag was unavailable.', rendered_template_invalid: 'The rendered template did not pass validation.', template_schema_invalid: 'The template no longer matches the ticket schema.' }[reason] || reason.replaceAll('_', ' '))
export function criteriaText(value: unknown): string {
  if (Array.isArray(value)) return value.filter(item => typeof item === 'string').join('\n')
  return typeof value === 'string' ? value : ''
}
export function parseCriteria(value: string): string[] { return value.split('\n').map(line => line.replace(/^\s*[-*]\s*(?:\[[ xX]\]\s*)?/, '').trim()).filter(Boolean) }
export function recurrenceEstimate(raw: string): number | null { return parseEstimate(raw.toLowerCase().replace(/\s+/g, '').replace(/minutes?|mins?$/, 'm').replace(/hours?|hrs?$/, 'h')) }
export const recurrenceSourceIsParent = (source?: ListItem | null) => source?.kind_slug === 'epic' || source?.kind_slug === 'work' && (source.is_leaf === false || (source.work_children_count ?? 0) > 0)
export function templateFrom(source?: ListItem | null): RecurrenceTemplate {
  const fields = source?.fields ?? {}
  return { name: source?.title.slice(0, 80) ?? '', title: source ? `${source.title} · {{date}}` : '', description: source?.body ?? '', acceptance_criteria: parseCriteria(criteriaText(fields.acceptance_criteria)), estimate_hours: source?.estimate?.hours ?? (typeof fields.estimate_hours === 'number' ? fields.estimate_hours : 0), priority: source?.priority === 'none' ? 'medium' : source?.priority || 'medium', type: 'work', tags: Array.isArray(fields.tags) ? fields.tags.flatMap(tag => typeof tag === 'object' && tag && 'id' in tag && typeof tag.id === 'string' && /^[0-9a-f-]{36}$/i.test(tag.id) ? [tag.id] : []) : [],
    pill_en: typeof fields.pill_en === 'string' ? fields.pill_en : undefined,
    pill_de: typeof fields.pill_de === 'string' ? fields.pill_de : undefined,
    benefit_en: typeof fields.benefit_en === 'string' ? fields.benefit_en : undefined,
    benefit_de: typeof fields.benefit_de === 'string' ? fields.benefit_de : undefined,
    hide_from_release_notes: typeof fields.hide_from_release_notes === 'boolean' ? fields.hide_from_release_notes : true,
  }
}
export function templateProblems(input: RecurrenceInput, estimate: string): string[] {
  const t = input.template, problems: string[] = [], bytes = (s: string) => new TextEncoder().encode(s).length
  if (!t.name?.trim()) problems.push('Give it a name.')
  if (bytes(t.name || '') > 80) problems.push('Keep the name within 80 bytes.')
  if (!t.title.trim()) problems.push('Give each ticket a title.')
  if (bytes(t.title) > 512 || bytes(t.description) > 65536 || t.acceptance_criteria.length > 100 || t.acceptance_criteria.some(c => bytes(c) > 4096)) problems.push('The template exceeds its size limits.')
  if ([t.pill_en, t.pill_de].some(value => bytes(value || '') > 512) || [t.benefit_en, t.benefit_de].some(value => bytes(value || '') > 4096)) problems.push('The release copy exceeds its size limits.')
  const values = [t.title, t.description, t.pill_en, t.pill_de, t.benefit_en, t.benefit_de, ...t.acceptance_criteria].join('\n')
  const rest = values.replace(/\{\{(occurrence|date|release_name|release_version)\}\}/g, '')
  if (rest.includes('{{') || rest.includes('}}')) problems.push('Use Number, Date or Release for template variables.')
  if ((input.trigger.kind !== 'event' || input.trigger.event !== 'release.published') && /\{\{release_(name|version)\}\}/.test(values)) problems.push('Release variables require a release trigger.')
  if (input.trigger.kind === 'time' && input.trigger.rrule?.startsWith('FREQ=WEEKLY') && !ruleParts(input.trigger.rrule).BYDAY) problems.push('Pick at least one day.')
  if (estimate.trim() && recurrenceEstimate(estimate) === null) problems.push('Use an estimate such as 20 min or 2 h, up to 200 hours.')
  if (input.queue_each && (!(t.estimate_hours > 0) || !t.acceptance_criteria.length)) problems.push('To queue each one, add an estimate and criteria.')
  if (input.trigger.kind === 'time' && !/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(input.trigger.time_of_day || '')) problems.push('Pick a valid time.')
  return problems
}
export function changeWords(before: Recurrence | null, after: Recurrence | null): string {
  if (!before || !after) return ''
  const changes: string[] = []
  if (recurrenceName(before) !== recurrenceName(after)) changes.push(`name: ${recurrenceName(before)} to ${recurrenceName(after)}`)
  if (JSON.stringify(before.trigger) !== JSON.stringify(after.trigger)) changes.push(`${triggerWords(before.trigger)} to ${triggerWords(after.trigger)}`)
  if (before.queue_each !== after.queue_each) changes.push(after.queue_each ? 'queues each one' : 'no longer queues')
  if (before.overlap_policy !== after.overlap_policy) changes.push(after.overlap_policy === 'skip' ? 'skips while open' : 'creates while open')
  if (JSON.stringify(before.template) !== JSON.stringify(after.template)) changes.push('template changed')
  return changes.join(' · ')
}

// ---------- Routines (AEON-1182): a recurring ticket an agent may work on by itself ----------
// The same definition as above, plus the optional saved intent (AEON-1084) and the
// redacted execution policy. Saving never starts anything; consent is a separate write.
export type RoutineScopeKind = 'project' | 'personal' | 'workspace'
export type RoutineAction = 'work.create' | 'work.update' | 'knowledge.write' | 'pr.open' | 'pipeline.request'
export type RoutineBudgetMode = 'off' | 'tokens' | 'money' | 'both'
export interface RoutineBudget { mode: RoutineBudgetMode; token_ceiling?: number; money_ceiling_microusd?: number }
export interface RoutineRuntime { needs_native_host: true; needs_browser: boolean; runtime_class: 'native_coding' | 'native_browser' }
export interface RoutineSource { kind: 'node' | 'knowledge'; id: string }
export interface RoutineAssignment { goal: string; sources?: RoutineSource[]; role: string; work_kind_id: string; allowed_actions?: RoutineAction[]; runtime_requirements: RoutineRuntime; budget?: RoutineBudget }
export interface RoutineDefinition { scope: { kind: RoutineScopeKind; project_id?: string }; owner_principal_id: string; assignment?: RoutineAssignment }
export interface RoutineExecutionPolicy { eligible: boolean; consent_required: boolean; wait_reason: string; project_revision: number; consent_revision: number }
export interface RoutineExecutionSettings { revision: number; automatic_launch_enabled: boolean; consent_enabled: boolean; qualification_id: string | null; wait_reason: string }
export interface ExecutionPrincipal { id: string; kind: string; label: string }
export interface RoutineGuardRule { id: string; checkpoint: 'save' | 'action' | 'both'; method: 'words' | 'paths' | 'size' | 'template'; result: 'allow' | 'needs_person' | 'block'; values?: string[]; limit?: number; template?: string }
export interface RoutineGuardEntry { rule: RoutineGuardRule; level: 'hard' | 'recommended' | 'custom'; scope: { kind: 'installation' | 'tenant' | 'project' | 'user'; id: string }; version: string; superseded?: boolean }
export interface RoutineGuardPolicy { hard_version: string; preset_version: string; digest: string; sources: { scope: { kind: string; id: string }; revision: number }[]; rules: RoutineGuardEntry[] }

export function copyDefinition(value: RoutineDefinition | null | undefined): RoutineDefinition | null | undefined {
  if (!value) return value
  const a = value.assignment
  return { scope: { ...value.scope }, owner_principal_id: value.owner_principal_id, ...(a ? { assignment: { ...a, sources: (a.sources ?? []).map(source => ({ ...source })), allowed_actions: [...(a.allowed_actions ?? [])], runtime_requirements: { ...a.runtime_requirements }, ...(a.budget ? { budget: { ...a.budget } } : {}) } } : {}) }
}
export const isAgentRoutine = (item: Pick<RecurrenceInput, 'definition'>) => !!item.definition?.assignment
export const routineScope = (item: Pick<RecurrenceInput, 'definition'>): RoutineScopeKind => item.definition?.scope.kind ?? 'project'

// Effort is the registry role; there is no model picker. mechanical stays API/CLI-only.
export const ROUTINE_EFFORTS = [['scout', 'Look around'], ['build', 'Build'], ['build-hard', 'Hard build']] as const
export const effortWord = (role: string) => ({ scout: 'Look around', mechanical: 'Small change', build: 'Build', 'build-hard': 'Hard build' } as Record<string, string>)[role] ?? role
export const effortVerb = (role: string) => role === 'scout' ? 'looks around' : role === 'mechanical' ? 'makes it' : 'builds it'
// Today's route in the Models page's own words, "Opus 5.5 via Claude": its model name and
// harness label, never a model or profile id; a profile without a name reads as one.
export function routeWords(profile: RegistryProfile): string {
  const name = fullName(profile)
  return `${name.includes(profile.model) ? lineFallback(profile.model) : name} via ${harnessLabel(profile.harness)}`
}
// The closed outward action set; everything else stays inside the agent's own copy.
export const ROUTINE_ACTIONS: readonly { action: RoutineAction; title: string; detail: string; reach: boolean }[] = [
  { action: 'work.create', title: 'Create tickets', detail: 'New tickets under its parent.', reach: false },
  { action: 'work.update', title: 'Update its own tickets', detail: 'Status, comments and fields on tickets it created.', reach: false },
  { action: 'knowledge.write', title: 'Write knowledge entries', detail: 'A finding or report in this project’s knowledge.', reach: false },
  { action: 'pr.open', title: 'Open a pull request', detail: 'A pull request from its own branch. A person merges.', reach: true },
  { action: 'pipeline.request', title: 'Ask to ship through the pipeline', detail: 'Review, CI and the merge queue still decide; a person approves.', reach: true },
]
export const ROUTINE_NEVER = ['Merge, publish or deploy by itself', 'Change its own rules, checks or scripts', 'Read secrets or sign-ins', 'Contact people or send surveys'] as const
export function actionOutcome(action: RoutineAction, on: boolean): ['ok' | 'off', string] {
  if (!on) return ['off', 'Off']
  return ['ok', action === 'pr.open' ? 'Allowed · a person merges' : action === 'pipeline.request' ? 'Allowed · a person approves' : 'Allowed · checked first']
}
export function mayWords(actions: readonly RoutineAction[]) {
  const parts: string[] = [], pr = actions.includes('pr.open')
  if (actions.some(action => action.startsWith('work.'))) parts.push('tickets')
  if (actions.includes('knowledge.write')) parts.push('knowledge')
  if (pr) parts.push('pull requests')
  if (actions.includes('pipeline.request')) parts.push('pipeline requests')
  const list = parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}` : parts[0] ?? 'nothing outside its own copy'
  return list + (pr ? ' · a person merges' : '')
}

// Budget per run: tokens are saved as tokens and edited in thousands; money as paid micro-USD.
const thousands = (tokens: number) => (tokens / 1000).toLocaleString('en', { maximumFractionDigits: 1 })
const dollars = (micro: number) => (micro / 1_000_000).toLocaleString('en', { maximumFractionDigits: 2 })
export function budgetWords(budget?: RoutineBudget | null) {
  const b = budget ?? { mode: 'off' as const }
  if (b.mode === 'tokens' && b.token_ceiling) return `Stops at ${thousands(b.token_ceiling)}k tokens a run`
  if (b.mode === 'money' && b.money_ceiling_microusd) return `Stops at $${dollars(b.money_ceiling_microusd)} a run`
  if (b.mode === 'both' && b.token_ceiling && b.money_ceiling_microusd) return `Stops at ${thousands(b.token_ceiling)}k tokens or $${dollars(b.money_ceiling_microusd)} a run`
  return 'No limit per run'
}
export const budgetInputs = (budget?: RoutineBudget | null) => ({ tokens: budget?.token_ceiling ? String(budget.token_ceiling / 1000) : '400', dollars: budget?.money_ceiling_microusd ? String(budget.money_ceiling_microusd / 1_000_000) : '5' })
/** The saved budget for a mode and its two fields, or null when a needed field is not a positive amount. */
export function budgetFrom(mode: RoutineBudgetMode, tokens: string, money: string): RoutineBudget | null {
  if (mode === 'off') return { mode }
  const k = Number(tokens.trim()), usd = Number(money.trim().replace(/^\$/, ''))
  const tokenCeiling = Number.isFinite(k) && k > 0 && k <= 1_000_000_000 ? Math.round(k * 1000) : 0
  const moneyCeiling = Number.isFinite(usd) && usd > 0 && usd <= 1_000_000 ? Math.round(usd * 1_000_000) : 0
  if ((mode === 'tokens' || mode === 'both') && !tokenCeiling) return null
  if ((mode === 'money' || mode === 'both') && !moneyCeiling) return null
  return { mode, ...(mode !== 'money' ? { token_ceiling: tokenCeiling } : {}), ...(mode !== 'tokens' ? { money_ceiling_microusd: moneyCeiling } : {}) }
}

// The server's closed wait reasons, in words. ctx names the owner and the output project.
export function waitReasonWords(reason: string, ctx: { owner?: string; project?: string } = {}) {
  const project = ctx.project || 'this project'
  const words: Record<string, string> = {
    automatic_launch_disabled: `Agents can’t run routines in ${project} yet.`,
    project_unavailable: 'The project is archived or deleted.',
    qualification_unavailable: `${project} has no accepted qualification yet.`,
    runtime_unavailable: 'No qualified computer has reported in the last two minutes.',
    qualification_runtime_changed: `A computer or tool changed since ${project} was qualified.`,
    definition_unavailable: 'Its target or sources are no longer available.',
    owner_unavailable: 'Its owner lost a needed permission.',
    owner_policy_mismatch: `${project}’s qualification covers another owner’s routines.`,
    capability_unqualified: 'No computer is qualified for this kind of work yet (for example, a browser).',
    budget_mode_unqualified: 'This budget mode can’t be enforced on this route yet.',
    execution_consent_required: `Waits for ${ctx.owner || 'its owner'} to let agents run it.`,
    execution_identity_unavailable: 'Its agent is missing or no longer restricted to this scope.',
    execution_identity_required: 'Pick who it runs as first.',
    stale_revision: 'Changed elsewhere; shown as it is now.',
    ticket_only_routine: 'It only creates tickets, so there is nothing to let agents run.',
    interactive_person_required: 'Only a signed-in person can let agents run it.',
  }
  return words[reason] ?? (reason ? `${reason.replaceAll('_', ' ')}.` : '')
}

// One row state per definition (spec "Routine row state"). Ticket-only routines are
// only Scheduled or Stopped. A paused routine that was never let run still waits for
// a person (Keep paused); once consented, a pause is a deliberate stop.
export type RoutineState = 'stopped' | 'waiting' | 'held' | 'running' | 'failed' | 'scheduled'
export function routineState(item: Pick<Recurrence, 'paused' | 'definition' | 'execution_policy' | 'last_run'>): RoutineState {
  const agent = !!item.definition?.assignment, policy = item.execution_policy, wait = policy?.wait_reason ?? ''
  if (!agent) return item.paused ? 'stopped' : 'scheduled'
  if (item.paused) return wait === 'execution_consent_required' && !policy?.consent_revision ? 'waiting' : 'stopped'
  const run = item.last_run?.state
  if (run === 'waiting_for_person' || wait === 'execution_consent_required') return 'waiting'
  if (run === 'held' || (policy && !policy.eligible)) return 'held'
  if (run === 'running') return 'running'
  if (run === 'failed') return 'failed'
  return 'scheduled'
}
export const ROUTINE_STATE_WORDS: Record<RoutineState, string> = { stopped: 'Stopped', waiting: 'Waiting for a person', held: 'Held', running: 'Running', failed: 'Failed', scheduled: 'Scheduled' }

/** The consent write for the revision now shown. Project and workspace routines need a restricted agent; personal ones run as their owner (null). */
export function consentRequest(saved: Pick<Recurrence, 'revision' | 'definition'>, principal: string | null) {
  const personal = saved.definition?.scope.kind === 'personal'
  if (!personal && !principal) return null
  return { expected_revision: saved.revision, execute_consent: true as const, execution_principal_id: personal ? null : principal }
}
/** A late answer applies only to the record still open, at a revision at least the one shown. */
export const acceptsRecord = (open: { id: string; revision: number } | null | undefined, result: { id: string; revision: number }) => !!open && open.id === result.id && result.revision >= open.revision
export const isStale = (error: unknown) => (error as { status?: number })?.status === 409 && /revision changed|stale_revision/.test(error instanceof Error ? error.message : '')
// After a stale 409: the fields the server changed since the shown revision. Those take
// the server's value (with a soft tint); every other unsaved field is kept.
export type StaleField = 'name' | 'title' | 'description' | 'criteria' | 'estimate' | 'priority' | 'trigger' | 'queue' | 'overlap' | 'assignment'
export function staleFields(shown: RecurrenceInput, server: RecurrenceInput): StaleField[] {
  const pick: Record<StaleField, (v: RecurrenceInput) => unknown> = {
    name: v => v.template.name ?? '', title: v => v.template.title, description: v => v.template.description, criteria: v => v.template.acceptance_criteria,
    estimate: v => v.template.estimate_hours, priority: v => v.template.priority, trigger: v => v.trigger, queue: v => v.queue_each, overlap: v => v.overlap_policy,
    assignment: v => v.definition?.assignment ?? null,
  }
  return (Object.keys(pick) as StaleField[]).filter(field => JSON.stringify(pick[field](shown)) !== JSON.stringify(pick[field](server)))
}
// Routine rules, read-only (S23): one plain sentence, its outcome and its level each.
const listed = (values: readonly string[] = []) => values.length > 3 ? `${values.slice(0, 3).join(', ')} and ${values.length - 3} more` : values.length > 1 ? `${values.slice(0, -1).join(', ')} and ${values.at(-1)}` : values[0] ?? ''
export function ruleWords(rule: RoutineGuardRule): { text: string; full: string } {
  const templates: Record<string, string> = { protected_paths_v1: 'Changes to rules, gates, checks, their scripts or secrets', catalogue_v1: 'Anything outside its allowed actions', harmful_intent_v1: 'Harmful or abusive work, attacks, anything illegal', different_family_v1: 'A model of another family checks each action before it happens' }
  if (rule.method === 'template') { const text = templates[rule.template ?? ''] ?? rule.id; return { text, full: text } }
  if (rule.method === 'size') { const text = `Changes more than ${rule.limit ?? 0} lines`; return { text, full: text } }
  const verb = rule.method === 'paths' ? 'Touches' : 'Mentions'
  return { text: `${verb} ${listed(rule.values)}`, full: `${verb} ${(rule.values ?? []).join(', ')}` }
}
export function ruleOutcome(rule: RoutineGuardRule): ['bad' | 'ask' | 'ok', string] {
  if (rule.template === 'different_family_v1') return ['ask', 'Required']
  return rule.result === 'block' ? ['bad', 'Blocks'] : rule.result === 'needs_person' ? ['ask', 'Asks a person'] : ['ok', 'Allows']
}
export function ruleLevel(entry: RoutineGuardEntry, ctx: { project?: string; mine?: boolean } = {}): ['hard' | 'rec' | 'ws' | 'proj' | 'you', string] {
  if (entry.level === 'hard') return ['hard', 'Hard floor']
  if (entry.level === 'recommended') return ['rec', 'Recommended']
  if (entry.scope.kind === 'project') return ['proj', ctx.project || 'Project']
  if (entry.scope.kind === 'user') return ['you', ctx.mine ? 'Yours' : 'Its owner’s']
  return ['ws', 'Workspace']
}
// The editor's agent rows as typed, and the saved assignment they become.
export interface RoutineDraft { role: string; kind: string; sources: RoutineSource[]; actions: RoutineAction[]; mode: RoutineBudgetMode; tokens: string; dollars: string; browser: boolean }
export function routineDraft(a?: RoutineAssignment | null): RoutineDraft {
  const fields = budgetInputs(a?.budget)
  return { role: a?.role ?? 'build', kind: a?.work_kind_id ?? '', sources: (a?.sources ?? []).map(source => ({ ...source })), actions: [...(a ? a.allowed_actions ?? [] : ['work.create', 'work.update'] as RoutineAction[])], mode: a?.budget?.mode ?? 'off', tokens: fields.tokens, dollars: fields.dollars, browser: !!a?.runtime_requirements.needs_browser }
}
/** The assignment saved for a draft and its instructions (the ticket description), or null while the budget fields are invalid. */
export function assignmentFrom(draft: RoutineDraft, goal: string): RoutineAssignment | null {
  const budget = budgetFrom(draft.mode, draft.tokens, draft.dollars)
  if (!budget) return null
  return { goal: goal.trim(), sources: draft.sources.map(source => ({ ...source })), role: draft.role, work_kind_id: draft.kind, allowed_actions: ROUTINE_ACTIONS.map(item => item.action).filter(action => draft.actions.includes(action)), runtime_requirements: { needs_native_host: true, needs_browser: draft.browser, runtime_class: draft.browser ? 'native_browser' : 'native_coding' }, budget }
}
export function routineProblems(draft: RoutineDraft, goal: string): string[] {
  const problems: string[] = []
  if (!goal.trim()) problems.push('Describe in the description what the agent does each time.')
  else if (new TextEncoder().encode(goal.trim()).length > 16384) problems.push('Keep the instructions within 16 KB.')
  if (!draft.kind) problems.push('Pick a kind of work.')
  if (!budgetFrom(draft.mode, draft.tokens, draft.dollars)) problems.push(draft.mode === 'tokens' ? 'Use a number of thousand tokens above zero.' : draft.mode === 'money' ? 'Use a dollar amount above zero.' : 'Use a token and a dollar amount above zero.')
  return problems
}
