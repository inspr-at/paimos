// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useSession } from './session'
import { can } from '../lib/authz'
import * as themes from '../lib/themes'
import type { ActiveTheme, ThemeRecord } from '../lib/themes'

type ThemeConflict = { themeID: string; revision: number; operation: 'selection' | 'save' | 'delete' }
type ThemeRecovery = ThemeConflict & { feedback: string }

export function useThemeEditor() {
  const session = useSession()
  const items = ref<ThemeRecord[]>([]), active = ref<ActiveTheme | null>(null), draft = ref<ThemeRecord | null>(null)
  const cursor = ref<string | null>(null), busy = ref(false), failure = ref(''), message = ref('')
  // A list update or another record's operation cannot resolve this record's
  // recovery. Only installing fresh active state or deleting that record can.
  const recoveries = ref(new Map<string, ThemeRecovery>())
  const recovery = computed(() => active.value ? recoveries.value.get(active.value.theme.id) : undefined)
  const conflict = computed(() => !!recovery.value)
  const error = computed(() => failure.value || recovery.value?.feedback || '')
  let epoch = 0
  const identity = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
  const selfWrite = computed(() => session.identity?.principal.kind === 'person' && (can('profile.write') || can('profile.portal_write')))
  // The API's RLS returns only this canonical person's personal themes; linked
  // aliases may have a different owner UUID. Both shared scopes need manage.
  const editable = (theme: ThemeRecord) => theme.scope === 'personal' ? selfWrite.value : session.identity?.principal.kind === 'person' && can('settings.manage')
  const dirty = computed(() => !!draft.value && !!active.value && JSON.stringify({ name: draft.value.name, values: draft.value.values }) !== JSON.stringify({ name: active.value.theme.name, values: active.value.theme.values }))
  const valid = computed(() => !!draft.value?.name.trim() && [...draft.value.name.trim()].length <= 80 && !/[\u0000-\u001f\u007f]/.test(draft.value.name))
  const clone = (theme: ThemeRecord) => JSON.parse(JSON.stringify(theme)) as ThemeRecord
  function install(current: ActiveTheme) {
    recoveries.value.delete(current.theme.id)
    failure.value = ''
    active.value = current; draft.value = clone(current.theme)
    merge(current.theme)
    if (current.fallback_notice) message.value = `${current.fallback_notice.deleted_theme_name} was deleted. You are using the workspace default.`
  }
  function merge(theme: ThemeRecord) {
    const index = items.value.findIndex(item => item.id === theme.id)
    if (index < 0) items.value.push(theme)
    else items.value[index] = theme
  }
  async function perform(fn: (current: () => boolean) => Promise<void>, operation?: ThemeConflict) {
    if (busy.value || !identity.value) return
    const started = epoch, person = identity.value
    const current = () => started === epoch && identity.value === person
    busy.value = true; message.value = ''
    // Listing more themes does not refresh the conflicted active record.
    // Keep its feedback and recovery link until fresh state is installed.
    if (!conflict.value) failure.value = ''
    try { await fn(current) }
    catch (caught) {
      if (current()) {
        failure.value = caught instanceof Error ? caught.message : 'The theme operation failed.'
        if ((caught as { status?: number }).status === 409 && operation) recoveries.value.set(operation.themeID, { ...operation, feedback: failure.value })
      }
    } finally { if (current()) busy.value = false }
  }
  async function load() {
    await perform(async current => {
      const [page, chosen] = await Promise.all([themes.listThemes(), themes.getActiveTheme()])
      if (!current()) return
      items.value = page.items; cursor.value = page.next_cursor; install(chosen)
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
    await perform(async current => { const chosen = await themes.selectTheme(id, revision); if (current()) install(chosen) }, { themeID: active.value.theme.id, revision, operation: 'selection' })
  }
  async function save() {
    if (!draft.value || !dirty.value || !valid.value || !editable(draft.value) || conflict.value) return
    const captured = clone(draft.value)
    await perform(async current => {
      const saved = await themes.updateTheme(captured)
      if (!current() || draft.value?.id !== captured.id) return
      recoveries.value.delete(saved.id)
      merge(saved); active.value!.theme = saved; draft.value = clone(saved); message.value = 'Saved.'
    }, { themeID: captured.id, revision: captured.revision, operation: 'save' })
  }
  function discard() {
    if (busy.value || !active.value) return
    draft.value = clone(active.value.theme); failure.value = ''; message.value = 'Discarded.'
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
      try {
        const chosen = await themes.selectTheme(created.id, selectionRevision)
        if (current()) install(chosen)
      } catch (caught) {
        if (current()) failure.value = `The copy was created, but could not be selected. ${caught instanceof Error ? caught.message : 'Try again.'}`
      }
    })
  }
  async function newTheme() {
    const id = active.value?.default_theme_id
    if (!id || dirty.value || !selfWrite.value) return
    const started = epoch
    let fetched = false
    // Read the actual default even when it lives on a later list page.
    await perform(async current => { const source = await themes.getTheme(id); if (current()) { merge(source); fetched = true } })
    const source = items.value.find(item => item.id === id)
    if (started === epoch && fetched && source) await duplicate(source)
  }
  async function remove(theme: ThemeRecord) {
    if (dirty.value || !editable(theme) || theme.id === active.value?.default_theme_id) return
    const captured = clone(theme)
    await perform(async current => {
      await themes.deleteTheme(captured)
      if (!current()) return
      recoveries.value.delete(captured.id)
      items.value = items.value.filter(item => item.id !== captured.id); message.value = 'Deleted.'
      if (active.value?.theme.id === captured.id) {
        // Deletion succeeded even if the subsequent fallback read fails.
        active.value = null; draft.value = null
        try { const chosen = await themes.getActiveTheme(); if (current()) { install(chosen); message.value = 'Deleted. You are using the workspace default.' } }
        catch { if (current()) failure.value = 'Deleted, but the workspace default could not be loaded. Try again.' }
      }
    }, { themeID: captured.id, revision: captured.revision, operation: 'delete' })
  }
  watch(identity, () => {
    epoch++; items.value = []; active.value = null; draft.value = null; cursor.value = null
    busy.value = false; failure.value = ''; message.value = ''; recoveries.value.clear()
    if (identity.value) void load()
  }, { immediate: true })
  onScopeDispose(() => { epoch++ })
  return { items, active, draft, cursor, busy, error, message, conflict, dirty, valid, selfWrite, editable, load, more, choose, save, discard, duplicate, newTheme, remove }
}
