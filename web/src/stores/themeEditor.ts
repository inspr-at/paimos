// SPDX-License-Identifier: AGPL-3.0-only
import { captureAgentThemeSave, captureAgentThemeSelection, installAgentTheme } from '../lib/agentTheme'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useSession } from './session'
import { can } from '../lib/authz'
import * as themes from '../lib/themes'
import type { ActiveTheme, ThemeRecord } from '../lib/themes'

export function useThemeEditor() {
  const session = useSession()
  const items = ref<ThemeRecord[]>([]), active = ref<ActiveTheme | null>(null), draft = ref<ThemeRecord | null>(null)
  const cursor = ref<string | null>(null), busy = ref(false), error = ref(''), message = ref('')
  const conflict = ref(false)
  let epoch = 0
  const identity = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
  const selfWrite = computed(() => session.identity?.principal.kind === 'person' && (can('profile.write') || can('profile.portal_write')))
  // The API's RLS returns only this canonical person's personal themes; linked
  // aliases may have a different owner UUID. Workspace writes need manage.
  const editable = (theme: ThemeRecord) => theme.scope !== 'personal' ? session.identity?.principal.kind === 'person' && can('settings.manage') : selfWrite.value
  const dirty = computed(() => !!draft.value && !!active.value && JSON.stringify({ name: draft.value.name, values: draft.value.values }) !== JSON.stringify({ name: active.value.theme.name, values: active.value.theme.values }))
  const valid = computed(() => !!draft.value?.name.trim() && [...draft.value.name.trim()].length <= 80 && !/[\u0000-\u001f\u007f]/.test(draft.value.name))
  const clone = (theme: ThemeRecord) => JSON.parse(JSON.stringify(theme)) as ThemeRecord
  function install(current: ActiveTheme) {
    active.value = current; draft.value = clone(current.theme)
    installAgentTheme(current.theme.values.agents, current)
    merge(current.theme)
    if (current.fallback_notice) message.value = `${current.fallback_notice.deleted_theme_name} was deleted. You are using the workspace default.`
  }
  function merge(theme: ThemeRecord) {
    const index = items.value.findIndex(item => item.id === theme.id)
    if (index < 0) items.value.push(theme)
    else items.value[index] = theme
  }
  async function perform(fn: (current: () => boolean) => Promise<void>) {
    if (busy.value || !identity.value) return
    const started = epoch, person = identity.value
    const current = () => started === epoch && identity.value === person
    busy.value = true; error.value = ''; message.value = ''
    try { await fn(current) }
    catch (failure) {
      if (current()) { error.value = failure instanceof Error ? failure.message : 'The theme operation failed.'; conflict.value = (failure as { status?: number }).status === 409 }
    } finally { if (current()) busy.value = false }
  }
  async function load() {
    await perform(async current => {
      const [page, chosen] = await Promise.all([themes.listThemes(), themes.getActiveTheme()])
      if (!current()) return
      items.value = page.items; cursor.value = page.next_cursor; conflict.value = false; install(chosen)
    })
  }
  async function more() {
    const after = cursor.value
    if (!after) return
    await perform(async current => {
      const page = await themes.listThemes(after)
      if (!current()) return
      page.items.forEach(merge); cursor.value = page.next_cursor
    })
  }
  async function choose(theme: ThemeRecord) {
    if (!active.value || dirty.value || !selfWrite.value) return
    const revision = active.value.revision, id = theme.id
    const reconcile = captureAgentThemeSelection(id, revision)
    await perform(async current => {
      const chosen = await themes.selectTheme(id, revision)
      if (reconcile(chosen) && current()) install(chosen)
    })
  }
  async function save() {
    if (!draft.value || !dirty.value || !valid.value || !editable(draft.value) || conflict.value) return
    const captured = clone(draft.value)
    const reconcile = captureAgentThemeSave(captured.id, active.value!.revision)
    await perform(async current => {
      const saved = await themes.updateTheme(captured)
      reconcile(saved)
      if (!current() || draft.value?.id !== captured.id) return
      merge(saved); active.value!.theme = saved; draft.value = clone(saved); message.value = 'Saved.'
    })
  }
  function discard() {
    if (busy.value || !active.value) return
    draft.value = clone(active.value.theme); error.value = ''; message.value = 'Discarded.'
    if (conflict.value) void load()
  }
  async function duplicate(source: ThemeRecord) {
    if (dirty.value || !selfWrite.value) return
    const captured = clone(source), selectionRevision = active.value?.revision
    const name = `${[...captured.name].slice(0, 73).join('')} copy`
    await perform(async current => {
      const created = await themes.duplicateTheme(captured, name)
      if (!current()) return
      merge(created); message.value = 'Duplicated.'
      if (selectionRevision === undefined) return
      const reconcile = captureAgentThemeSelection(created.id, selectionRevision)
      try {
        const chosen = await themes.selectTheme(created.id, selectionRevision)
        if (reconcile(chosen) && current()) install(chosen)
      } catch (failure) {
        if (current()) error.value = `The copy was created, but could not be selected. ${failure instanceof Error ? failure.message : 'Try again.'}`
      }
    })
  }
  async function newTheme() {
    const id = active.value?.default_theme_id
    if (!id || dirty.value || !selfWrite.value) return
    // Read the actual default even when it lives on a later list page.
    await perform(async current => { const source = await themes.getTheme(id); if (current()) merge(source) })
    const source = items.value.find(item => item.id === id)
    if (source && !error.value) await duplicate(source)
  }
  async function remove(theme: ThemeRecord) {
    if (dirty.value || !editable(theme) || theme.id === active.value?.default_theme_id) return
    const captured = clone(theme)
    await perform(async current => {
      await themes.deleteTheme(captured)
      if (!current()) return
      items.value = items.value.filter(item => item.id !== captured.id); message.value = 'Deleted.'
      if (active.value?.theme.id === captured.id) {
        // Deletion succeeded even if the subsequent fallback read fails.
        active.value = null; draft.value = null
        try { const chosen = await themes.getActiveTheme(); if (current()) { install(chosen); message.value = 'Deleted. You are using the workspace default.' } }
        catch { if (current()) error.value = 'Deleted, but the workspace default could not be loaded. Try again.' }
      }
    })
  }
  watch(identity, () => {
    epoch++; items.value = []; active.value = null; draft.value = null; cursor.value = null
    busy.value = false; error.value = ''; message.value = ''; conflict.value = false
    if (identity.value) void load()
  }, { immediate: true })
  onScopeDispose(() => { epoch++ })
  return { items, active, draft, cursor, busy, error, message, conflict, dirty, valid, selfWrite, editable, load, more, choose, save, discard, duplicate, newTheme, remove }
}
