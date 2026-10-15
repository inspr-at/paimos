// SPDX-License-Identifier: AGPL-3.0-only
// Wire types and presentation for the sparse matrix in api/openapi.yaml.
export type PrefLevel = 'default' | 'person' | 'project'
export type Residency = 'any' | 'eu' | 'local'
export type ResidencyLockMode = 'warn' | 'freeze' | 'tighten_only'
export type ModelSelector = { mode: 'auto' } | { mode: 'pinned'; profile_id: string } | { mode: 'latest'; family: string; line: string; effort: string; harness?: string }
export interface PrefProfile { id: string; harness: string; family: string; model: string; effort: string; tier: string; enabled: boolean; display_name?: string; model_version?: string; provider?: string }
export interface WorkKind { id: string; slug: string; label: string; hint: string; position: number; project_id?: string; system?: 'review' | 'security' | 'other'; archived_at?: string }
export interface PrefRow { kind_id: string; locked: boolean; normal: ModelSelector; complex: ModelSelector }
export interface PrefScope { revision: number; residency: Residency | null; residency_locked: boolean; prefs_locked: boolean; rows: PrefRow[]; updated_by: string | null; updated_at: string | null }
export interface ResidencyView { value: Residency; set_by: PrefLevel; locked_by?: PrefLevel; lock_value?: Residency; loosened_lock: boolean; loosened_locks?: { level: PrefLevel; value: Residency }[]; qualifying_routes: number }
export interface EffectiveModel { selector: ModelSelector; profile: PrefProfile | null; label: string; brand: string; follows_latest: boolean; pinned: boolean; today_version: string; unavailable_reason?: string }
export interface PrefViewRow { kind_id: string; set_by: string; locked_by: string; changed_here: boolean; reset_to: string; warnings: string[]; normal: EffectiveModel; complex: EffectiveModel }
export interface PrefChoice { profile: PrefProfile; line: string; model_version: string; retired: boolean; review_ladder: boolean; review_reason: string; residency_routes: number }
export interface PrefView { choices_truncated?: boolean; choices?: PrefChoice[]; residency: ResidencyView; rows: PrefViewRow[]; changes: number }
export interface ModelPreferences { revision: number; person_id: string | null; kinds: WorkKind[]; levels: Record<PrefLevel, PrefScope | null>; views: Record<PrefLevel, PrefView | null>; can: Record<'edit_default' | 'edit_person' | 'edit_project' | 'add_default_kind' | 'add_project_kind', boolean>; residency_lock_mode: ResidencyLockMode }
export interface PrefWriteResult { level: PrefScope; revision: number; running_outside: string[]; residency: ResidencyView }
export const LEVELS: PrefLevel[] = ['default', 'person', 'project']
// Keep this in step with modelregistry.ProfileLine. Profile revision is never a model version.
export function profileLine(p: PrefProfile): { line: string; version: string } {
  let id = p.model
  if (p.harness === 'codex') { const m = /^gpt-([0-9]+(?:\.[0-9]+)*)-(.+)$/.exec(id); return m ? { line: m[2]!, version: m[1]! } : { line: id, version: '' } }
  if (p.harness === 'opencode') id = id.replace(/^(google|ollama)\//, '')
  if (p.harness === 'pi') id = id.replace(/^anthropic\//, '')
  if (p.harness === 'claude' || p.harness === 'pi') { id = id.replace(/^claude-/, ''); if (['opus', 'sonnet', 'haiku', 'fable'].includes(id)) return { line: id, version: 'alias' } }
  let suffix = ''
  if (p.harness === 'cursor') { const m = /^(.+?)(-(low|medium|high|xhigh|max|ultra))?(-fast)?$/.exec(id); if (m) { id = m[1]!; suffix = m[4] ?? '' } }
  const m = /^([a-z][a-z-]*?)-?([0-9]+(?:[.-][0-9]+)*)(.*)$/.exec(id)
  return m ? { line: m[1]! + m[3]! + suffix, version: m[2]!.replaceAll('-', '.') } : { line: p.model, version: '' }
}
// Match the server's segment ordering: aliases track the newest vendor version.
export function compareModelVersions(a: string, b: string): number {
  if (a === b) return 0
  if (a === 'alias') return 1
  if (b === 'alias') return -1
  const aa = a.split('.'), bb = b.split('.')
  for (let i = 0; i < Math.max(aa.length, bb.length); i++) {
    const difference = (Number(aa[i]) || 0) - (Number(bb[i]) || 0)
    if (difference) return Math.sign(difference)
  }
  return 0
}

export interface ModelPreferenceTrace {
  kind: string; kind_source: string; complexity: string; complexity_source: string; bucket: 'normal' | 'complex';
  set_by: PrefLevel; locked_by?: PrefLevel; mode: 'auto' | 'latest' | 'pinned'; role?: string;
  residency: Omit<ResidencyView, 'qualifying_routes'>; ticket_requirement?: Residency; fallback?: string; blocked?: string; prefs?: string; hard?: string[]
}
export interface TicketModelResolution { profile: PrefProfile | null; trace: ModelPreferenceTrace; owner_required: boolean; role: string; residency: Residency }
