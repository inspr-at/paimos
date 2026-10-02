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
export interface PrefView { choices?: PrefChoice[]; residency: ResidencyView; rows: PrefViewRow[]; changes: number }
export interface ModelPreferences { revision: number; person_id: string | null; kinds: WorkKind[]; levels: Record<PrefLevel, PrefScope | null>; views: Record<PrefLevel, PrefView | null>; can: Record<'edit_default' | 'edit_person' | 'edit_project' | 'add_default_kind' | 'add_project_kind', boolean>; residency_lock_mode: ResidencyLockMode }
export interface PrefWriteResult { level: PrefScope; revision: number; running_outside: string[]; residency: ResidencyView }
export const LEVELS: PrefLevel[] = ['default', 'person', 'project']
export const LEVEL_LABEL: Record<PrefLevel, string> = { default: 'Default', person: 'You', project: 'This project' }
export const RESIDENCY_LABEL: Record<Residency, string> = { any: 'Any provider', eu: 'EU-hosted only', local: 'Local only' }
export const lowerLevelLabel = (level: PrefLevel) => level === 'default' ? 'People and projects may change this' : 'Projects may change this'
export const resetRowLabel = (level: PrefLevel) => level === 'project' ? 'Reset to your setting' : 'Reset to default'
export const resetLevelLabel = (level: PrefLevel) => level === 'default' ? 'Reset to product defaults' : level === 'person' ? 'Reset all your changes' : 'Reset all project changes'
export const summaryLabel = (level: PrefLevel, changes: number) => level === 'default' ? 'You are editing the default for everyone.' : `${changes} ${changes === 1 ? 'change' : 'changes'} ${level === 'person' ? 'on your level' : 'for this project'}.`
export const setByLabel = (row: Pick<PrefViewRow, 'locked_by' | 'changed_here' | 'set_by'>) => row.locked_by ? `Locked by ${row.locked_by === 'person' ? 'you' : row.locked_by === 'project' ? 'this project' : 'default'}` : row.changed_here ? 'Changed here' : LEVEL_LABEL[row.set_by as PrefLevel] ?? 'Default'
export const canAddKind = (doc: ModelPreferences, level: PrefLevel) => level === 'default' ? doc.can.add_default_kind : level === 'project' && doc.can.add_project_kind
export const canRemoveKind = (kind: WorkKind, level: PrefLevel, editable: boolean) => editable && !kind.system && (level === 'default' ? !kind.project_id : level === 'project' && !!kind.project_id)
export const visibleKinds = (kinds: WorkKind[], level: PrefLevel) => kinds.filter(k => !k.archived_at && (level === 'project' || !k.project_id)).sort((a, b) => a.position - b.position || a.label.localeCompare(b.label))
const STRICTNESS: Record<Residency, number> = { any: 0, eu: 1, local: 2 }
export function providerDisabled(value: Residency, view: ResidencyView, level: PrefLevel, mode: ResidencyLockMode): boolean {
  if (!view.locked_by || view.locked_by === level) return false
  const locked = view.lock_value ?? view.value
  return mode === 'freeze' ? value !== locked : mode === 'tighten_only' ? STRICTNESS[value] < STRICTNESS[locked] : false
}
export function providerWarning(view: ResidencyView): string {
  if (!view.loosened_lock) return ''
  const locks = view.loosened_locks?.length ? view.loosened_locks : [{ level: view.locked_by ?? 'default', value: view.lock_value ?? 'eu' }]
  return `Looser than the lock. ${locks.map(l => `${LEVEL_LABEL[l.level]} locks Allowed providers to ${RESIDENCY_LABEL[l.value]}`).join('; ')}. ${RESIDENCY_LABEL[view.value]} applies here. Runs may use providers outside that lock; their trace records the choice.`
}
export function modelCopy(model: EffectiveModel, review = false, choices: PrefChoice[] = []) {
  const selector = model.selector, auto = selector.mode === 'auto'
  const requested = selector.mode === 'pinned' ? choices.find(c => c.profile.id === selector.profile_id)?.profile : null
  const name = auto ? review ? 'Another family' : 'Automatic' : selector.mode === 'latest' ? model.unavailable_reason ? selector.line : model.profile?.display_name || selector.line : requested?.display_name || (model.profile?.id === (selector.mode === 'pinned' ? selector.profile_id : '') ? model.profile.display_name || model.label : 'Pinned model')
  const detail = model.unavailable_reason ? `Unavailable · ${model.profile ? 'Automatic' : 'work waits'}` : auto ? review ? 'strongest qualified reviewer' : "the ticket’s role" : [model.today_version, model.profile?.effort].filter(Boolean).join(' · ') || 'Version not reported'
  const tip = model.unavailable_reason || (auto ? `Follows the ticket's own role.${model.profile ? ` Typical pick today: ${model.label} ${model.today_version} · ${model.profile.effort}.` : ''}` : `${model.follows_latest ? 'Follows new versions' : 'Pinned'}: ${name} · ${detail}`)
  return { name, detail, tip }
}
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
export interface PickerCandidate { profile_id: string; selected: boolean; skip_reasons: string[] }
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
export function pickerReason(p: PrefProfile, review: boolean, candidates: PickerCandidate[], residency: ResidencyView): string {
  if (!p.enabled) return 'Disabled in the model registry'
  if (review) {
    if (!['strong', 'frontier'].includes(p.tier) || p.effort !== 'xhigh') return 'Reviews require strong or frontier models at extra high effort'
    if (!candidates.some(c => c.profile_id === p.id)) return 'Outside the review ladder'
    if (!['claude', 'grok'].includes(p.harness)) return `${p.harness === 'codex' ? 'Codex' : p.harness} is not qualified for reviews`
  }
  if (residency.value !== 'any' && residency.qualifying_routes === 0) return `No ${residency.value === 'eu' ? 'EU-hosted' : 'local'} model route qualifies today`
  return ''
}
