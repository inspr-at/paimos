// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, ref } from 'vue'
import { APIError } from './api'
import { archiveWorkKind, createWorkKind, getModelPreferences, getPickerProfiles, getReviewCandidates, putPreferenceRow, putPreferenceScope, resetPreference } from './modelPrefsApi'
import type { ModelPreferences, ModelSelector, PickerCandidate, PrefLevel, PrefProfile, PrefScope, PrefWriteResult } from './modelPrefs'

export function useModelPrefsEditor(project: string | undefined, initial: PrefLevel) {
  const doc = ref<ModelPreferences | null>(null), level = ref<PrefLevel>(initial)
  const profiles = ref<PrefProfile[]>([]), candidates = ref<PickerCandidate[]>([])
  const loading = ref(true), busy = ref(false), notice = ref(''), catalogError = ref('')
  const outside = ref<string[]>([])
  const abort = new AbortController()
  let generation = 0, active = true
  onBeforeUnmount(() => { active = false; generation++; abort.abort() })
  async function load() {
    const token = ++generation
    loading.value = true
    const results = await Promise.allSettled([getModelPreferences(project, abort.signal), getPickerProfiles(abort.signal), getReviewCandidates(abort.signal)])
    if (!active || token !== generation) return
    loading.value = false
    const [prefs, models, reviews] = results
    if (prefs.status === 'fulfilled') doc.value = prefs.value
    else notice.value = prefs.reason instanceof Error ? prefs.reason.message : 'Preferences could not be loaded'
    if (models.status === 'fulfilled') profiles.value = models.value
    if (reviews.status === 'fulfilled') candidates.value = reviews.value.ladder
    catalogError.value = models.status === 'rejected' || reviews.status === 'rejected' ? 'Some model choices could not be loaded' : ''
  }
  async function mutate(write: () => Promise<unknown>, optimistic?: () => void) {
    if (busy.value || !doc.value || !doc.value.can[`edit_${level.value}`]) return
    const token = ++generation, before = structuredClone(doc.value ? JSON.parse(JSON.stringify(doc.value)) : null) as ModelPreferences
    busy.value = true; notice.value = ''; optimistic?.()
    let accepted = false
    try {
      const result = await write()
      accepted = true
      if (!active || token !== generation) return
      if (result && typeof result === 'object' && 'running_outside' in result) outside.value = (result as PrefWriteResult).running_outside
      const refreshed = await getModelPreferences(project, abort.signal)
      if (active && token === generation) doc.value = refreshed
    } catch (error) {
      if (!active || token !== generation) return
      if (!accepted) doc.value = before
      if (error instanceof APIError && error.status === 409 && error.body.code !== 'slug_taken') {
        try { const refreshed = await getModelPreferences(project, abort.signal); if (active && token === generation) { doc.value = refreshed; notice.value = 'Changed elsewhere, refreshed' } }
        catch { if (active && token === generation) { notice.value = 'Changed elsewhere; refresh before editing again'; doc.value = null } }
      } else notice.value = accepted ? 'Saved, but the refreshed view could not be loaded. Refresh before editing again.' : error instanceof APIError && error.body.code === 'slug_taken' ? 'That kind of work already exists. Choose another name.' : error instanceof Error ? error.message : 'Could not save this change'
      if (accepted) doc.value = null
    } finally { if (active && token === generation) busy.value = false }
  }
  function scope(body: Partial<Pick<PrefScope, 'residency' | 'residency_locked' | 'prefs_locked'>>) {
    const target = level.value, stored = doc.value?.levels[target]
    if (!stored) return
    // Level PUT replaces its section flags; omitted flags would clear a lock.
    const update = { revision: stored.revision, residency: stored.residency, residency_locked: stored.residency_locked, prefs_locked: stored.prefs_locked, ...body }
    if (update.residency_locked && update.residency === null) update.residency = doc.value!.views[target]!.residency.value
    return mutate(() => putPreferenceScope(target, update, project))
  }
  function row(kind: string, selector?: { bucket: 'normal' | 'complex'; value: ModelSelector }, toggleLock = false) {
    const target = level.value, stored = doc.value?.levels[target], effective = doc.value?.views[target]?.rows.find(r => r.kind_id === kind)
    if (!stored || !effective || (effective.locked_by && effective.locked_by !== target)) return
    const own = stored.rows.find(r => r.kind_id === kind)
    if (toggleLock && target === 'project') return
    const normal = selector?.bucket === 'normal' ? selector.value : own?.normal ?? effective.normal.selector
    const complex = selector?.bucket === 'complex' ? selector.value : own?.complex ?? effective.complex.selector
    const body = toggleLock && !own ? { revision: stored.revision, locked: true } : { revision: stored.revision, normal, complex, locked: toggleLock ? !own?.locked : own?.locked ?? false }
    return mutate(() => putPreferenceRow(target, kind, body, project), selector ? () => {
      const model = effective[selector.bucket]
      model.selector = selector.value
      const selection = selector.value
      model.profile = selection.mode === 'pinned' ? profiles.value.find(p => p.id === selection.profile_id) ?? null : model.profile
      model.follows_latest = selector.value.mode === 'latest'; model.pinned = selector.value.mode === 'pinned'
      effective.changed_here = target !== 'default'; effective.set_by = target
    } : undefined)
  }
  function reset(kind?: string) {
    const target = level.value, revision = doc.value?.levels[target]?.revision
    if (revision === undefined) return
    return mutate(() => resetPreference(target, revision, project, kind))
  }
  function addKind(label: string) {
    const target = level.value
    if (!doc.value || !['default', 'project'].includes(target) || !doc.value.can[target === 'default' ? 'add_default_kind' : 'add_project_kind']) return
    return mutate(() => createWorkKind(label.trim(), target === 'project' ? project : undefined))
  }
  function archive(kind: string) { return mutate(() => archiveWorkKind(kind)) }
  return { doc, level, profiles, candidates, loading, busy, notice, catalogError, outside, load, scope, row, reset, addKind, archive }
}
