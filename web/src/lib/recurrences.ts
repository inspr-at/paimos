// SPDX-License-Identifier: AGPL-3.0-only
import type { ListItem } from './api'
import { parseEstimate } from './estimates'

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
}
export interface Recurrence extends RecurrenceInput {
  id: string; paused: boolean; revision: number; occurrence_count: number; next_at: string | null
  created_at: string; updated_at: string; last_result?: RecurrenceResult; open_previous?: RecurrenceResult
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
export interface RecurrencePreview { times: string[]; trigger_kind: 'time' | 'event' }
export const copyRecurrenceInput = (value: RecurrenceInput): RecurrenceInput => ({ ...value, template: { ...value.template, tags: [...value.template.tags], acceptance_criteria: [...value.template.acceptance_criteria] }, trigger: { ...value.trigger, ...(value.trigger.filter ? { filter: { ...value.trigger.filter, ...(value.trigger.filter.project_ids ? { project_ids: [...value.trigger.filter.project_ids] } : {}) } } : {}), ...(value.trigger.external ? { external: { ...value.trigger.external } } : {}) } })
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
