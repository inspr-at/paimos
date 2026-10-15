// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api'
import { APIError } from './work'

export const DEFINITION_BLOCKS = [
  { id: 'in_progress', name: 'Ready', to: 'In progress' },
  { id: 'qa', name: 'QA', to: 'QA' },
  { id: 'done', name: 'Done', to: 'Done' },
] as const
export type DefinitionStatus = typeof DEFINITION_BLOCKS[number]['id']
export type DefinitionCondition = 'all' | 'ui' | 'code' | 'blocked' | 'release_notes' | 'releases'
export interface DefinitionRule { id: string; label?: string; label_de?: string; condition?: DefinitionCondition; kinds?: string[]; labels?: string[]; owner_id?: string | null }
export type DefinitionChecks = Partial<Record<DefinitionStatus, DefinitionRule[]>>
export interface Definition { id: string; revision: string; source: 'project' | 'workspace'; copied_from_revision: string | null; checks: DefinitionChecks }
export interface DefinitionContext { project_id: string; definition: Definition | null; workspace_default: Definition | null; copied_from_checks: DefinitionChecks | null; can_manage: boolean }
export const DEFINITION_CATALOG: { id: string; en: string; de: string; condition: DefinitionCondition; block: DefinitionStatus }[] = [
  { id: 'estimate_set', en: 'Estimate set', de: 'Schätzung gesetzt', condition: 'all', block: 'in_progress' },
  { id: 'acceptance_present', en: 'Acceptance criteria written', de: 'Abnahmekriterien beschrieben', condition: 'all', block: 'in_progress' },
  { id: 'design_attached', en: 'Approved design attached', de: 'Freigegebenes Design angehängt', condition: 'ui', block: 'in_progress' },
  { id: 'blocker_named', en: 'Blocker named', de: 'Blocker benannt', condition: 'blocked', block: 'in_progress' },
  { id: 'no_active_build', en: 'No other build in flight', de: 'Kein anderer Build aktiv', condition: 'all', block: 'in_progress' },
  { id: 'ci_green_latest_main', en: 'Full CI green on latest main', de: 'Volle CI grün auf aktuellem main', condition: 'code', block: 'qa' },
  { id: 'review_ok_cross_family', en: 'Cross-family review ok', de: 'Review durch andere Modellfamilie ok', condition: 'code', block: 'qa' },
  { id: 'visual_ok', en: 'Visual check ok', de: 'Visuelle Prüfung ok', condition: 'ui', block: 'qa' },
  { id: 'pills_benefits_set', en: 'Pills and benefits set', de: 'Pills und Nutzen beschrieben', condition: 'release_notes', block: 'done' },
  { id: 'release_copy_set', en: 'Release text set (EN + DE)', de: 'Release-Text gesetzt (EN + DE)', condition: 'release_notes', block: 'done' },
  { id: 'merged', en: 'Merged to main', de: 'In main gemergt', condition: 'code', block: 'done' },
  { id: 'released', en: 'Shipped in a release', de: 'In einem Release ausgeliefert', condition: 'releases', block: 'done' },
]
export const DEFINITION_CONDITIONS: Record<DefinitionCondition, string> = { all: 'All tickets', ui: 'UI tickets', code: 'Code tickets', blocked: 'Blocked tickets', release_notes: 'Tickets shown in release notes', releases: 'Projects that use releases' }
export const manualRule = (rule: DefinitionRule) => rule.id.startsWith('manual:')
export const ruleLabel = (rule: DefinitionRule, language = 'en') => (language === 'de' ? rule.label_de : rule.label) || (language === 'de' ? DEFINITION_CATALOG.find(c => c.id === rule.id)?.de : DEFINITION_CATALOG.find(c => c.id === rule.id)?.en) || rule.label || rule.id
export const ruleCondition = (rule: DefinitionRule): DefinitionCondition => rule.condition || DEFINITION_CATALOG.find(c => c.id === rule.id)?.condition || 'all'
export function ruleWords(rule: DefinitionRule): string {
  return [DEFINITION_CONDITIONS[ruleCondition(rule)], rule.kinds?.length ? `Kinds: ${rule.kinds.join(', ')}` : '', rule.labels?.length ? `Labels: ${rule.labels.join(', ')}` : '', manualRule(rule) ? 'A person ticks this' : ''].filter(Boolean).join(' · ')
}
export const cloneChecks = (checks: DefinitionChecks = {}): DefinitionChecks => JSON.parse(JSON.stringify(checks))
export const checkCount = (checks: DefinitionChecks = {}) => DEFINITION_BLOCKS.reduce((n, b) => n + (checks[b.id]?.length ?? 0), 0)
const set = (values: string[] = []) => [...new Set(values.map(s => s.toLowerCase()))].sort()
export function sameRule(a: DefinitionRule, b: DefinitionRule) {
  return ruleLabel(a) === ruleLabel(b) && ruleLabel(a, 'de') === ruleLabel(b, 'de') && ruleCondition(a) === ruleCondition(b) && (a.owner_id ?? null) === (b.owner_id ?? null) && JSON.stringify([...(a.kinds ?? [])].sort()) === JSON.stringify([...(b.kinds ?? [])].sort()) && JSON.stringify(set(a.labels)) === JSON.stringify(set(b.labels))
}
export interface DefinitionDifference { item: DefinitionRule; kind: 'same' | 'added' | 'changed' | 'moved' | 'left' | 'waiting_new' | 'waiting_changed' | 'waiting_removed'; default?: DefinitionRule; block: DefinitionStatus }
export function definitionDiff(own: DefinitionChecks, workspace: DefinitionChecks = {}, base: DefinitionChecks | null = null): DefinitionDifference[] {
  return DEFINITION_BLOCKS.flatMap(({ id: block }) => {
    const rows = own[block] ?? [], defaults = workspace[block] ?? [], before = base?.[block] ?? defaults
    const shared = rows.filter(r => defaults.some(d => d.id === r.id)).map(r => r.id), ordered = defaults.filter(d => shared.includes(d.id)).map(r => r.id)
    return [
      ...rows.map(item => {
        const d = defaults.find(r => r.id === item.id), b = before.find(r => r.id === item.id)
        const kind: DefinitionDifference['kind'] = d ? sameRule(item, d) ? shared.indexOf(item.id) === ordered.indexOf(item.id) ? 'same' : 'moved' : b && sameRule(item, b) && base ? 'waiting_changed' : 'changed' : b && base ? 'waiting_removed' : 'added'
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
