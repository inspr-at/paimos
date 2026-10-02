// SPDX-License-Identifier: AGPL-3.0-only
import type { EffectiveModel, ModelPreferences, ModelSelector, PrefChoice, PrefLevel, PrefScope, PrefView, Residency, WorkKind } from '../src/lib/modelPrefs'
export const PREF_MODELS = [
  { id: 'model-codex-high', harness: 'codex', family: 'openai', model: 'gpt-6.1-sol', effort: 'high', tier: 'standard', enabled: true, display_name: 'Codex Sol', model_version: '6.1', provider: 'openai' },
  { id: 'model-codex-xhigh', harness: 'codex', family: 'openai', model: 'gpt-6.1-sol', effort: 'xhigh', tier: 'frontier', enabled: true, display_name: 'Codex Sol', model_version: '6.1', provider: 'openai' },
  { id: 'model-claude-high', harness: 'claude', family: 'anthropic', model: 'claude-fable-5-1', effort: 'high', tier: 'frontier', enabled: true, display_name: 'Claude Fable', model_version: '5.1', provider: 'anthropic' },
  { id: 'model-claude-xhigh', harness: 'claude', family: 'anthropic', model: 'claude-fable-5-1', effort: 'xhigh', tier: 'frontier', enabled: true, display_name: 'Claude Fable', model_version: '5.1', provider: 'anthropic' },
  { id: 'model-retired', harness: 'claude', family: 'anthropic', model: 'claude-fable-5', effort: 'high', tier: 'frontier', enabled: true, display_name: 'Claude Fable', model_version: '5', provider: 'anthropic' },
]
export const prefScope = (): PrefScope => ({ revision: 0, residency: null, residency_locked: false, prefs_locked: false, rows: [], updated_by: null, updated_at: null })
export function prefsFixture(): ModelPreferences {
  return { revision: 0, person_id: 'person', kinds: [
    { id: 'backend', slug: 'backend', label: 'Backend', hint: 'APIs and data', position: 10 },
    { id: 'review', slug: 'review', label: 'Reviews', hint: 'A second family checks the work', position: 20, system: 'review' },
    { id: 'security', slug: 'security', label: 'Security and permissions', hint: 'Trust boundaries', position: 30, system: 'security' },
    { id: 'other', slug: 'other', label: 'Everything else', hint: 'Any unassigned area', position: 40, system: 'other' },
    { id: 'firmware', slug: 'firmware', label: 'Firmware', hint: 'For this project only', position: 50, project_id: 'p-aeon' },
  ], levels: { default: prefScope(), person: prefScope(), project: prefScope() }, views: { default: null, person: null, project: null },
    can: { edit_default: true, edit_person: true, edit_project: true, add_default_kind: true, add_project_kind: true }, residency_lock_mode: 'warn' }
}
const effective = (selector: ModelSelector, complex: boolean, review: boolean): EffectiveModel => {
  const profile = selector.mode === 'pinned' ? PREF_MODELS.find(p => p.id === selector.profile_id) ?? null : selector.mode === 'latest' ? PREF_MODELS.find(p => p.family === selector.family && p.effort === selector.effort) ?? null : PREF_MODELS.find(p => p.family === (review ? 'anthropic' : 'openai') && p.effort === (review || complex ? 'xhigh' : 'high'))!
  return { selector, profile, label: profile?.display_name ?? 'Automatic', brand: profile?.family ?? '', today_version: profile?.model_version ?? '', follows_latest: selector.mode === 'latest', pinned: selector.mode === 'pinned' }
}
export function prefsDocument(source: ModelPreferences, project = true): ModelPreferences {
  const doc = structuredClone(source), levels: PrefLevel[] = ['default', 'person', 'project']
  for (let i = 0; i < (project ? 3 : 2); i++) {
    const level = levels[i]!, own = doc.levels[level]!
    const view: PrefView = { changes: own.rows.length + Number(own.residency !== null) + Number(own.prefs_locked) + Number(own.residency_locked), rows: [], residency: { value: 'any', set_by: 'default', qualifying_routes: 4, loosened_lock: false }, choices: [] }
    const strict: Record<Residency, number> = { any: 0, eu: 1, local: 2 }
    for (const current of levels.slice(0, i + 1)) {
      const scope = doc.levels[current]!
      if (scope.residency) { view.residency.value = scope.residency; view.residency.set_by = current }
      if (scope.residency_locked) { view.residency.locked_by = current; view.residency.lock_value = view.residency.value }
    }
    view.residency.qualifying_routes = view.residency.value === 'any' ? 4 : 0
    view.residency.loosened_lock = !!view.residency.lock_value && strict[view.residency.value] < strict[view.residency.lock_value]
    view.choices = PREF_MODELS.map(p => ({ profile: p, line: p.harness === 'codex' ? 'sol' : 'fable', model_version: p.model_version, retired: p.id === 'model-retired', review_ladder: p.effort === 'xhigh', review_reason: p.harness === 'codex' ? 'Codex is not qualified for reviews: inherited MCP tools are not isolated.' : '', residency_routes: view.residency.value === 'any' ? 1 : 0 } satisfies PrefChoice))
    for (const kind of doc.kinds.filter(k => !k.archived_at && (level === 'project' || !k.project_id))) {
      let normal: ModelSelector = { mode: 'auto' }, complex: ModelSelector = { mode: 'auto' }, setBy: PrefLevel = 'default', lockedBy = ''
      for (const current of levels.slice(0, i + 1)) {
        if (lockedBy) break
        const scope = doc.levels[current]!, row = scope.rows.find(r => r.kind_id === kind.id)
        if (row) { normal = row.normal; complex = row.complex; setBy = current }
        if (scope.prefs_locked || row?.locked) lockedBy = current
      }
      const changed = level !== 'default' && own.rows.some(r => r.kind_id === kind.id) && (!lockedBy || lockedBy === level)
      view.rows.push({ kind_id: kind.id, set_by: setBy, locked_by: lockedBy, changed_here: changed, reset_to: changed ? level === 'project' ? 'person' : 'default' : '', warnings: [], normal: effective(normal, false, kind.system === 'review'), complex: effective(complex, true, kind.system === 'review') })
    }
    doc.views[level] = view
  }
  if (!project) { doc.levels.project = null; doc.views.project = null; doc.can.edit_project = false; doc.can.add_project_kind = false; doc.kinds = doc.kinds.filter(k => !k.project_id) }
  return doc
}
export const customKind = (id: string, label: string, project?: string): WorkKind => ({ id, slug: label.toLowerCase().replaceAll(' ', '-'), label, hint: '', position: 60, ...(project ? { project_id: project } : {}) })
