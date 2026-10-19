// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import type { IconName } from '../components/AppIcon.vue'
import { watchStreamHealth } from './streamHealth'

export const DEFINITION_BLOCKS = [
  { id: 'in_progress', name: 'Ready', to: 'In progress' },
  { id: 'qa', name: 'QA', to: 'QA' },
  { id: 'done', name: 'Done', to: 'Done' },
] as const
export type DefinitionStatus = typeof DEFINITION_BLOCKS[number]['id']
export type DefinitionCondition = 'all' | 'ui' | 'code' | 'blocked' | 'release_notes' | 'releases'
export interface DefinitionRule { id: string; label?: string; label_de?: string; condition?: DefinitionCondition; kinds?: string[]; labels?: string[]; owner_id?: string | null; fallback_owner_id?: string | null }
export type DefinitionChecks = Partial<Record<DefinitionStatus, DefinitionRule[]>>
export interface Definition { id: string; revision: string; created_at?: string; source: 'project' | 'workspace'; copied_from_revision: string | null; checks: DefinitionChecks }
export interface DefinitionChange { check_id: string; block: DefinitionStatus; kind: 'new' | 'changed' | 'removed'; before: DefinitionRule | null; current: DefinitionRule | null }
export interface DefinitionAdmin { id: string; name: string }
export interface DefinitionContext { project_id: string; definition: Definition | null; workspace_default: Definition | null; copied_from_checks: DefinitionChecks | null; can_manage: boolean; pending_changes?: DefinitionChange[]; admins?: DefinitionAdmin[] }
export interface DefinitionProject { project_id: string; key: string; difference_count: number; pending_count: number }
export interface WorkspaceDefinitionContext { definition: Definition | null; can_manage: boolean; admins: DefinitionAdmin[]; projects: DefinitionProject[]; differ_count: number; uses_default: string[]; next_cursor: string | null }
export const DEFINITION_CATALOG: { id: string; en: string; de: string; condition: DefinitionCondition; block: DefinitionStatus; icon: IconName }[] = [
  { id: 'estimate_set', en: 'Estimate set', de: 'Schätzung gesetzt', condition: 'all', block: 'in_progress', icon: 'clock' },
  { id: 'acceptance_present', en: 'Acceptance criteria written', de: 'Abnahmekriterien beschrieben', condition: 'all', block: 'in_progress', icon: 'list' },
  { id: 'design_attached', en: 'Approved design attached', de: 'Freigegebenes Design angehängt', condition: 'ui', block: 'in_progress', icon: 'image' },
  { id: 'blocker_named', en: 'Blocker named', de: 'Blocker benannt', condition: 'blocked', block: 'in_progress', icon: 'not' },
  { id: 'no_active_build', en: 'No other build in flight', de: 'Kein anderer Build aktiv', condition: 'all', block: 'in_progress', icon: 'workflow' },
  { id: 'ci_green_latest_main', en: 'Full CI green on latest main', de: 'Volle CI grün auf aktuellem main', condition: 'code', block: 'qa', icon: 'workflow' },
  { id: 'review_ok_cross_family', en: 'Cross-family review ok', de: 'Review durch andere Modellfamilie ok', condition: 'code', block: 'qa', icon: 'shield' },
  { id: 'visual_ok', en: 'Visual check ok', de: 'Visuelle Prüfung ok', condition: 'ui', block: 'qa', icon: 'eye' },
  { id: 'pills_benefits_set', en: 'Pills and benefits set', de: 'Pills und Nutzen beschrieben', condition: 'release_notes', block: 'done', icon: 'sentence' },
  { id: 'release_copy_set', en: 'Release text set (EN + DE)', de: 'Release-Text gesetzt (EN + DE)', condition: 'release_notes', block: 'done', icon: 'sentence' },
  { id: 'merged', en: 'Merged to main', de: 'In main gemergt', condition: 'code', block: 'done', icon: 'merge' },
  { id: 'released', en: 'Shipped in a release', de: 'In einem Release ausgeliefert', condition: 'releases', block: 'done', icon: 'layers' },
]
export const DEFINITION_CONDITIONS: Record<DefinitionCondition, string> = { all: 'All tickets', ui: 'UI tickets', code: 'Code tickets', blocked: 'Blocked tickets', release_notes: 'Unless hidden from release notes', releases: 'If the project uses releases' }
export const DEFINITION_CONDITIONS_DE: Record<DefinitionCondition, string> = { all: 'Alle Tickets', ui: 'UI-Tickets', code: 'Code-Tickets', blocked: 'Blockierte Tickets', release_notes: 'Sofern in Release-Notizen sichtbar', releases: 'Wenn das Projekt Releases nutzt' }
export const definitionConditionWords = (rule: DefinitionRule, language = 'en') => (language === 'de' ? DEFINITION_CONDITIONS_DE : DEFINITION_CONDITIONS)[ruleCondition(rule)]
export function definitionDate(value: string, language = 'en') {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : new Intl.DateTimeFormat(language === 'de' ? 'de-AT' : 'en-GB', {day:'numeric', month:'short', timeZone:'UTC'}).format(date).replace(/\s/g, '\u00a0')
}
export function definitionNotice(key: string, count: number, date: string, language = 'en') {
  if (!count) return language === 'de' ? 'Alle Änderungen entschieden.' : 'All changes decided.'
  return language === 'de' ? `Der Workspace-Standard wurde am ${date} geändert, nachdem ${key} ihn kopiert hat. ${count}\u00a0${count === 1 ? 'Änderung wartet' : 'Änderungen warten'} unten auf eine Entscheidung; bis dahin behält ${key} seine Kopie unverändert.`
    : `The workspace default changed on ${date}, after ${key} copied it. ${count}\u00a0${count === 1 ? 'change waits' : 'changes wait'} for a decision below; until then ${key} keeps its copy as it is.`
}
export function definitionChangeWords(change: DefinitionChange, date: string, language = 'en', currentWords?: string) {
  const de = language === 'de', condition = currentWords ?? (change.current ? definitionConditionWords(change.current, language) : '')
  return change.kind === 'new' ? de ? `Neu im Workspace-Standard seit ${date}` : `New in the workspace default since ${date}`
    : change.kind === 'removed' ? de ? `Seit ${date} nicht mehr im Workspace-Standard` : `Gone from the workspace default since ${date}`
    : de ? `Der Workspace-Standard sagt jetzt ${condition} (seit ${date})` : `The workspace default now says ${condition} (since ${date})`
}
export function definitionDecisionWords(change: DefinitionChange, own: DefinitionRule | undefined, language = 'en'): [string, string] {
  const de = language === 'de', condition = definitionConditionWords(own ?? change.before ?? change.current!, language)
  return change.kind === 'new' ? de ? ['Hier übernehmen','Weglassen'] : ['Add here','Leave out']
    : change.kind === 'removed' ? de ? ['Auch hier entfernen','Behalten'] : ['Remove here too','Keep it']
    : de ? ['Auch hier übernehmen',`${condition} behalten`] : ['Use it here too',`Keep ${condition}`]
}
export function definitionDeskWords(key: string, count: number, language = 'en') {
  return language === 'de' ? `${key}: ${count}\u00a0Änderungen am Workspace-Standard warten auf eine Entscheidung` : `${key}: ${count}\u00a0changes to the workspace default wait for a decision`
}
export const DEFINITION_CONDITION_OPTIONS = [
  { value: 'all', detail: 'Every ticket' },
  { value: 'code', detail: 'Tickets that change code' },
  { value: 'ui', detail: 'Tickets labelled ui' },
] as const
export const manualRule = (rule: DefinitionRule) => rule.id.startsWith('manual:')
export const ruleIcon = (rule: DefinitionRule): IconName => manualRule(rule) ? 'person-check' : DEFINITION_CATALOG.find(c => c.id === rule.id)?.icon ?? 'list-checks'
export function fixedRuleCondition(rule: DefinitionRule): DefinitionCondition | undefined {
  const condition = DEFINITION_CATALOG.find(c => c.id === rule.id)?.condition
  return condition && !DEFINITION_CONDITION_OPTIONS.some(option => option.value === condition) ? condition : undefined
}
export const ruleLabel = (rule: DefinitionRule, language = 'en') => (language === 'de' ? rule.label_de : rule.label) || (language === 'de' ? DEFINITION_CATALOG.find(c => c.id === rule.id)?.de : DEFINITION_CATALOG.find(c => c.id === rule.id)?.en) || rule.label || rule.id
export const ruleCondition = (rule: DefinitionRule): DefinitionCondition => rule.condition || DEFINITION_CATALOG.find(c => c.id === rule.id)?.condition || 'all'
export function ruleOwnerWords(rule: DefinitionRule, people: { id: string; name: string }[] = []): string {
  const name = (id: string) => people.find(person => person.id === id)?.name ?? 'a named person'
  return rule.owner_id ? `Ticked by ${name(rule.owner_id)}` : `Ticked by the ticket’s assignee, else ${rule.fallback_owner_id ? name(rule.fallback_owner_id) : 'a project admin'}`
}
export function ruleWords(rule: DefinitionRule, people: { id: string; name: string }[] = []): string {
  return [DEFINITION_CONDITIONS[ruleCondition(rule)], rule.kinds?.length ? `Kinds: ${rule.kinds.join(', ')}` : '', rule.labels?.length ? `Labels: ${rule.labels.join(', ')}` : '', manualRule(rule) ? ruleOwnerWords(rule, people) : ''].filter(Boolean).join(' · ')
}
export const cloneChecks = (checks: DefinitionChecks = {}): DefinitionChecks => JSON.parse(JSON.stringify(checks))
export const checkCount = (checks: DefinitionChecks = {}) => DEFINITION_BLOCKS.reduce((n, b) => n + (checks[b.id]?.length ?? 0), 0)
const set = (values: string[] = []) => [...new Set(values.map(s => s.toLowerCase()))].sort()
export function sameRule(a: DefinitionRule, b: DefinitionRule) {
  return ruleLabel(a) === ruleLabel(b) && ruleLabel(a, 'de') === ruleLabel(b, 'de') && ruleCondition(a) === ruleCondition(b) && (a.owner_id ?? null) === (b.owner_id ?? null) && (a.fallback_owner_id ?? null) === (b.fallback_owner_id ?? null) && JSON.stringify([...(a.kinds ?? [])].sort()) === JSON.stringify([...(b.kinds ?? [])].sort()) && JSON.stringify(set(a.labels)) === JSON.stringify(set(b.labels))
}
export interface DefinitionDifference { item: DefinitionRule; kind: 'same' | 'added' | 'changed' | 'moved' | 'left' | 'waiting_new' | 'waiting_changed' | 'waiting_removed'; default?: DefinitionRule; block: DefinitionStatus }
export function definitionDiff(own: DefinitionChecks, workspace: DefinitionChecks = {}, base: DefinitionChecks | null = null): DefinitionDifference[] {
  return DEFINITION_BLOCKS.flatMap(({ id: block }) => {
    const rows = own[block] ?? [], defaults = workspace[block] ?? [], before = base?.[block] ?? defaults
    const shared = rows.filter(r => defaults.some(d => d.id === r.id)).map(r => r.id), ordered = defaults.filter(d => shared.includes(d.id)).map(r => r.id)
    return [
      ...rows.map(item => {
        const d = defaults.find(r => r.id === item.id), b = before.find(r => r.id === item.id)
        let kind: DefinitionDifference['kind'] = 'added'
        if (d && sameRule(item, d)) kind = shared.indexOf(item.id) === ordered.indexOf(item.id) ? 'same' : 'moved'
        else if (d) kind = base && b && sameRule(item, b) ? 'waiting_changed' : 'changed'
        else if (b && base) kind = 'waiting_removed'
        return { item, kind, default: d, block }
      }),
      ...defaults.filter(d => !rows.some(r => r.id === d.id)).map(item => ({ item, kind: (base && !before.some(r => r.id === item.id) ? 'waiting_new' : 'left') as DefinitionDifference['kind'], block })),
    ]
  })
}
export function resetChanges(own: DefinitionChecks, workspace: DefinitionChecks = {}): string[] {
  const diff = definitionDiff(own, workspace), lines: string[] = []
  for (const [kind, verb] of [['added', 'Dropped'], ['left', 'Comes back'], ['changed', 'Uses the workspace version']] as const) {
    const names = diff.filter(r => r.kind === kind).map(r => ruleLabel(r.item))
    if (names.length) lines.push(`${verb}: ${names.join(', ')}.`)
  }
  for (const block of DEFINITION_BLOCKS) {
    const shared = (own[block.id] ?? []).filter(r => workspace[block.id]?.some(d => d.id === r.id)).map(r => r.id)
    const order = (workspace[block.id] ?? []).filter(r => shared.includes(r.id)).map(r => r.id)
    if (JSON.stringify(shared) !== JSON.stringify(order)) lines.push(`${block.name} returns to the workspace check order.`)
  }
  if (!lines.length) lines.push('The checks stay the same; future workspace changes apply here at once.')
  return lines
}
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await api(path, init)
  if (!response.ok) { const body = await response.json().catch(() => ({})); throw new APIError(response.status, body.error ?? 'Definition could not be saved.', body) }
  return response.json()
}
export const readDefinitionContext = (id: string, signal?: AbortSignal) => request<DefinitionContext>(`/projects/${encodeURIComponent(id)}/definitions/context`, { signal })
export function saveDefinition(id: string, checks: DefinitionChecks, context: DefinitionContext, signal?: AbortSignal) {
  const current = context.definition, own = current?.source === 'project', workspace = context.workspace_default
  return request<Definition>(`/projects/${encodeURIComponent(id)}/definitions`, { method: 'PUT', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ checks, expected_revision: own ? current.revision : null, copied_from_revision: own ? current.copied_from_revision : workspace?.revision ?? null, ...(!own ? { expected_workspace_revision: workspace?.revision ?? null } : {}) }) })
}
export const resetDefinition = (id: string, context: DefinitionContext, signal?: AbortSignal) => request<Definition | null>(`/projects/${encodeURIComponent(id)}/definitions`, { method: 'DELETE', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: context.definition?.revision, expected_workspace_revision: context.workspace_default?.revision ?? null }) })
export const decideDefinitionChange = (id: string, change: DefinitionChange, decision: 'accept' | 'keep', context: DefinitionContext, signal?: AbortSignal) => request<DefinitionContext>(`/projects/${encodeURIComponent(id)}/definitions/decisions`, {method:'POST', signal, headers:{'Content-Type':'application/json'}, body:JSON.stringify({expected_revision:context.definition?.revision, expected_workspace_revision:context.workspace_default?.revision, check_id:change.check_id, decision})})
export const readWorkspaceDefinitionContext = (after?: string, signal?: AbortSignal) => request<WorkspaceDefinitionContext>(`/settings/definitions/context${after ? `?after=${encodeURIComponent(after)}` : ''}`, {signal})
export const saveWorkspaceDefinition = (checks: DefinitionChecks, current: Definition | null, signal?: AbortSignal) => request<Definition>('/settings/definitions', {method:'PUT',signal,headers:{'Content-Type':'application/json'},body:JSON.stringify({checks,expected_revision:current?.id === '00000000-0000-0000-0000-000000000000' ? null : current?.revision ?? null})})

// Hints only: each consumer re-reads its authorized context. The connection is
// owned and closed by the same person/tenant scope as that context.
export const DEFINITION_EVENTS = ['definition.saved','definition.reset','definition.change_decided'] as const
export function subscribeDefinitionChanges(changed: () => void) {
  if (typeof EventSource === 'undefined') return () => {}
  let stream: EventSource, stopped = false
  const health = watchStreamHealth(() => { stream.close(); changed(); connect() })
  function connect() {
    const source = stream = new EventSource('/api/events/stream?after=latest')
    const current = (run: () => void) => () => { if (!stopped && stream === source) { health.heard(); run() } }
    source.onopen = current(changed)
    for (const event of ['stream.ping','stream.ready']) source.addEventListener(event,current(() => {}))
    for (const event of DEFINITION_EVENTS) source.addEventListener(event,current(changed))
  }
  connect()
  return () => { stopped=true;health.stop();stream.close() }
}
