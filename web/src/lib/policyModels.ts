// SPDX-License-Identifier: AGPL-3.0-only
import { policyJSON, policyRequest } from './policyEditor.ts'
export interface ModelProfile { id: string; slug: string; display_name?: string; model_version?: string; model: string; family: string; harness: string; effort: string; tier: string; enabled: boolean }
export type Selector = { mode: 'auto' } | { mode: 'pinned'; profile_id: string } | { mode: 'latest'; family: string; line: string; effort: string; harness?: string }
export type PreferenceLevelName = 'default' | 'person' | 'project'
export interface PreferenceRow { kind_id: string; normal: Selector; complex: Selector; locked: boolean }
export interface PreferenceScalars { residency: 'any' | 'eu' | 'local' | null; residency_locked: boolean; prefs_locked: boolean }
export interface PreferenceLevel extends PreferenceScalars { revision: number; rows: PreferenceRow[] }
export interface PreferenceChoice { profile: ModelProfile; line: string; model_version: string; retired: boolean; review_ladder: boolean; review_reason: string; residency_routes: number }
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
function validSelector(value: Selector) {
  if (!value || typeof value !== 'object') return false
  if (value.mode === 'auto') return Object.keys(value).length === 1
  if (value.mode === 'pinned') return typeof value.profile_id === 'string' && !!value.profile_id
  return value.mode === 'latest' && !!value.family && !!value.line && !!value.effort
}
function validLevel(level: PreferenceLevel) {
  return level && Number.isSafeInteger(level.revision) && level.revision >= 0 && [null,'any','eu','local'].includes(level.residency) && typeof level.residency_locked === 'boolean' && typeof level.prefs_locked === 'boolean' && Array.isArray(level.rows) && level.rows.length <= 256 && level.rows.every(row => row && typeof row.kind_id === 'string' && typeof row.locked === 'boolean' && validSelector(row.normal) && validSelector(row.complex))
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
