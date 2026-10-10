// SPDX-License-Identifier: AGPL-3.0-only
import { policyJSON, policyRequest } from './policyEditor.ts'
import type { PolicyLadder, PolicyRole, PolicyStep } from './policies.ts'
export type ModelRoute = Omit<PolicyStep, 'profile'>
export interface LadderMutation { routes: ModelRoute[]; order_mode?: 'legacy' | 'saved' }
export interface EditableLadder extends PolicyLadder { routes: ModelRoute[]; edit_token: string | null; can_edit: boolean }
export type Selector = { mode: 'auto' } | { mode: 'pinned'; profile_id: string } | { mode: 'latest'; family: string; line: string; effort: string; harness?: string }
export type PreferenceLevelName = 'default' | 'person' | 'project'
export interface PreferenceRow { kind_id: string; normal: Selector; complex: Selector; locked: boolean }
export interface PreferenceScalars { residency: 'any' | 'eu' | 'local' | null; residency_locked: boolean; prefs_locked: boolean }
export interface PreferenceLevel extends PreferenceScalars { revision: number; rows: PreferenceRow[] }
export interface PreferenceChoice { profile: PolicyStep['profile']; line: string; model_version: string; retired: boolean; review_ladder: boolean; review_reason: string; residency_routes: number }
export interface EffectiveCell { selector: Selector; label: string; today_version: string; unavailable_reason?: string }
export interface PreferenceViewRow { kind_id: string; set_by: string; locked_by: string; changed_here: boolean; reset_to: string; warnings: string[]; normal: EffectiveCell; complex: EffectiveCell }
export interface PreferenceView {
  choices: PreferenceChoice[]; choices_truncated: boolean; resolution_truncated: boolean; changes: number
  residency: { value: string; set_by: string; locked_by?: string; loosened_lock: boolean; qualifying_routes: number }
  rows: PreferenceViewRow[]
}
export interface PreferenceDocument {
  person_id: string | null; levels: Record<PreferenceLevelName, PreferenceLevel | null>; views: Record<PreferenceLevelName, PreferenceView | null>
  kinds: { id: string; slug: string; label: string; hint: string; system?: string; project_id?: string }[]
  can: Record<string, boolean>; residency_lock_mode: 'warn'
}
export type PreferenceMutation = { unit: 'row'; kind_id: string; value: PreferenceRow | null } | { unit: 'scalars'; value: PreferenceScalars }
export interface PreferenceSnapshot { document: PreferenceDocument; level: PreferenceLevelName; project: string; running_outside?: string[] }
const encode = encodeURIComponent
const strongToken = (token: unknown): token is string => typeof token === 'string' && /^"[a-f0-9]{64}"$/.test(token)
function validRoute(row: ModelRoute, role: PolicyRole) {
  return row && row.role === role && typeof row.profile_id === 'string' && row.profile_id.length > 0 && Number.isInteger(row.priority) && row.priority > 0 && row.priority <= 2147483647 && ['available','unavailable','conserved','budget_limited'].includes(row.state) && typeof row.reason === 'string' && (row.valid_until === null || typeof row.valid_until === 'string' && Number.isFinite(Date.parse(row.valid_until)))
}
function validSelector(value: Selector) {
  if (!value || typeof value !== 'object') return false
  if (value.mode === 'auto') return Object.keys(value).length === 1
  if (value.mode === 'pinned') return typeof value.profile_id === 'string' && !!value.profile_id
  return value.mode === 'latest' && !!value.family && !!value.line && !!value.effort
}
function validLevel(level: PreferenceLevel) {
  return level && Number.isSafeInteger(level.revision) && level.revision >= 0 && [null,'any','eu','local'].includes(level.residency) && typeof level.residency_locked === 'boolean' && typeof level.prefs_locked === 'boolean' && Array.isArray(level.rows) && level.rows.length <= 256 && level.rows.every(row => row && typeof row.kind_id === 'string' && typeof row.locked === 'boolean' && validSelector(row.normal) && validSelector(row.complex))
}
export function validateLadder(data: EditableLadder, role: PolicyRole) {
  if (data.role !== role || !Array.isArray(data.routes) || data.routes.length > 50 || !Array.isArray(data.steps) || data.steps.length !== data.routes.length || typeof data.can_edit !== 'boolean' || typeof data.truncated !== 'boolean' || typeof data.setup !== 'boolean' || (!data.truncated && !strongToken(data.edit_token))) throw new Error('Invalid ladder snapshot')
  if (role === 'review-gate' && (!['legacy','saved'].includes(data.order_mode ?? '') || !Array.isArray(data.managed_fallback_order) || data.managed_fallback_order.length !== data.routes.length || new Set(data.managed_fallback_order).size !== data.routes.length || data.managed_fallback_order.some(id => !data.routes.some(row => row.profile_id === id)))) throw new Error('Invalid managed order snapshot')
  for (const route of data.routes) if (!validRoute(route, role)) throw new Error('Invalid ladder route')
  return data
}
export async function readLadder(role: PolicyRole, signal: AbortSignal): Promise<EditableLadder> {
  const response = await policyRequest(`/models/routes?role=${encode(role)}`, { signal })
  return validateLadder(await policyJSON<EditableLadder>(response), role)
}
export async function writeLadder(before: EditableLadder, desired: ModelRoute[] | LadderMutation, undo: boolean, signal: AbortSignal): Promise<EditableLadder> {
  const routes = Array.isArray(desired) ? desired : desired.routes
  const orderMode = before.role === 'review-gate' ? (Array.isArray(desired) ? undo ? before.order_mode : 'saved' : desired.order_mode) : undefined
  if (before.role === 'review-gate' && (!['legacy','saved'].includes(orderMode ?? '') || !['legacy','saved'].includes(before.order_mode ?? ''))) throw new Error('A complete managed order mode is required')
  if (!before.setup || before.truncated || !before.can_edit || !strongToken(before.edit_token) || routes.length > 50 || routes.some(row => row.role !== before.role)) throw new Error('A complete editable order is required')
  const response = await policyRequest(`/models/routes?role=${encode(before.role)}${undo ? '&expiry_policy=clear' : ''}${orderMode ? `&order_mode=${orderMode}` : ''}`, {
    method: 'PUT', signal, headers: { 'Content-Type': 'application/json', 'If-Match': before.edit_token }, body: JSON.stringify(routes),
  })
  const actual = await policyJSON<ModelRoute[]>(response), token = response.headers.get('ETag')
  if (!strongToken(token) || !Array.isArray(actual) || actual.length !== routes.length || actual.some((row,index) => !validRoute(row, before.role) || row.profile_id !== routes[index]?.profile_id || row.priority !== routes[index]?.priority)) throw new Error('Invalid save confirmation')
  const actualMode = response.headers.get('Model-Order-Mode')
  if (orderMode && actualMode !== orderMode) throw new Error('Invalid ordering mode confirmation')
  // Profile display metadata is refreshed afterwards; stored routes/token are
  // adopted immediately from the confirmed response, including expiry clearing.
  // Legacy fallback is refreshed from its source GET rather than inferred from
  // possibly missing display metadata (for example a step re-added by Undo).
  return { ...before, ...(orderMode ? { order_mode: orderMode, managed_fallback_order: orderMode === 'saved' ? actual.map(row => row.profile_id) : undefined } : {}), routes: actual, edit_token: token, steps: actual.map(route => ({ ...route, profile: before.steps.find(row => row.profile_id === route.profile_id)?.profile ?? { id: route.profile_id, slug: route.profile_id, model: route.profile_id, family: '', harness: '', effort: '', tier: '', enabled: true } })) }
}
/** Legacy draft starts from the displayed dispatch order, retaining every hold. */
export function ladderDraft(before: EditableLadder): LadderMutation {
  if (before.role !== 'review-gate') return { routes: structuredClone(before.routes) }
  validateLadder(before, before.role)
  const ids = before.managed_fallback_order!
  return { routes: ids.map((id, index) => ({ ...before.routes.find(row => row.profile_id === id)!, priority: index + 1 })), order_mode: 'saved' }
}
export function compensateLadder(before: EditableLadder): LadderMutation {
  return { routes: structuredClone(before.routes), ...(before.role === 'review-gate' ? { order_mode: before.order_mode } : {}) }
}
export function validatePreferences(data: PreferenceDocument) {
  if (typeof data.person_id !== 'string' || !Array.isArray(data.kinds) || data.kinds.length > 256 || data.residency_lock_mode !== 'warn' || !data.can || !data.views || !data.levels) throw new Error('Invalid preference snapshot')
  for (const name of ['default', 'person', 'project'] as const) {
    const level = data.levels[name], view = data.views[name]
    if (name === 'project' && level === null && view === null) continue
    if (!validLevel(level!) || !view || !Array.isArray(view.rows) || view.rows.length > 256 || !Array.isArray(view.choices) || view.choices.length > 256) throw new Error('Invalid preference level')
  }
  return data
}
export async function readPreferences(level: PreferenceLevelName, project: string, signal: AbortSignal): Promise<PreferenceSnapshot> {
  const context = level === 'project' ? project : ''
  if (level === 'project' && !context) throw new Error('Choose a visible project first')
  const response = await policyRequest(`/model-preferences${context ? `?project_id=${encode(context)}` : ''}`, { signal })
  const document = validatePreferences(await policyJSON<PreferenceDocument>(response))
  if (!document.levels[level] || !document.views[level]) throw new Error('The selected level was not returned')
  return { document, level, project: context }
}
/** Deliberately closed request set: no level DELETE and no rows on level PUT. */
export function preferenceRequest(before: PreferenceSnapshot, mutation: PreferenceMutation) {
  const level = before.document.levels[before.level]
  if (!level || !before.document.person_id || !before.document.can[`edit_${before.level}`]) throw new Error('This preference level is not editable')
  const base = `/model-preferences/levels/${encode(before.level)}`
  const suffix = before.project ? `?project_id=${encode(before.project)}` : ''
  const headers = { 'Content-Type': 'application/json', 'If-Prefs-Person': before.document.person_id }
  if (mutation.unit === 'scalars') {
    if ('rows' in mutation.value || !Object.hasOwn(mutation.value, 'residency') || !Object.hasOwn(mutation.value, 'residency_locked') || !Object.hasOwn(mutation.value, 'prefs_locked')) throw new Error('Only the three scalar fields may be saved at level scope')
    return { path: base + suffix, init: { method: 'PUT', headers, body: JSON.stringify({ revision: level.revision, residency: mutation.value.residency, residency_locked: mutation.value.residency_locked, prefs_locked: mutation.value.prefs_locked }) } }
  }
  if (mutation.unit !== 'row' || !mutation.kind_id || (mutation.value && mutation.value.kind_id !== mutation.kind_id)) throw new Error('A captured work-kind row is required')
  if (mutation.value === null) return { path: `${base}/rows/${encode(mutation.kind_id)}${suffix}${suffix ? '&' : '?'}revision=${level.revision}`, init: { method: 'DELETE', headers } }
  return { path: `${base}/rows/${encode(mutation.kind_id)}${suffix}`, init: {
    method: 'PUT', headers,
    body: JSON.stringify({ revision: level.revision, normal: mutation.value.normal, complex: mutation.value.complex, locked: mutation.value.locked }),
  } }
}
export async function writePreferences(before: PreferenceSnapshot, mutation: PreferenceMutation, _undo: boolean, signal: AbortSignal): Promise<PreferenceSnapshot> {
  const request = preferenceRequest(before, mutation)
  const response = await policyRequest(request.path, { ...request.init, signal })
  const result = await policyJSON<{ person_id: string; level: PreferenceLevel; revision: number; running_outside: string[] }>(response)
  if (result.person_id !== before.document.person_id || !validLevel(result.level) || !Number.isSafeInteger(result.revision) || result.level.revision !== result.revision || result.revision <= before.document.levels[before.level]!.revision || !Array.isArray(result.level.rows) || result.level.rows.length > 256 || !Array.isArray(result.running_outside)) throw new Error('Invalid preference confirmation')
  return { ...before, document: { ...before.document, levels: { ...before.document.levels, [before.level]: result.level } }, running_outside: result.running_outside }
}
export function compensatePreferences(before: PreferenceSnapshot, desired: PreferenceMutation): PreferenceMutation {
  const level = before.document.levels[before.level]!
  if (desired.unit === 'row') return { unit: 'row', kind_id: desired.kind_id, value: structuredClone(level.rows.find(row => row.kind_id === desired.kind_id) ?? null) }
  return { unit: 'scalars', value: preferenceScalars(level) }
}
export function preferenceScalars(level: PreferenceScalars): PreferenceScalars { return { residency: level.residency, residency_locked: level.residency_locked, prefs_locked: level.prefs_locked } }
export function selectorFor(choice: PreferenceChoice, mode: 'pinned' | 'latest'): Selector {
  return mode === 'pinned' ? { mode, profile_id: choice.profile.id } : { mode, family: choice.profile.family, line: choice.line, effort: choice.profile.effort, harness: choice.profile.harness }
}
export function choiceDisabled(choice: PreferenceChoice, kind: string) {
  return choice.retired || !choice.profile.enabled || choice.residency_routes === 0 || kind === 'security' || (kind === 'review' && !!choice.review_reason)
}
