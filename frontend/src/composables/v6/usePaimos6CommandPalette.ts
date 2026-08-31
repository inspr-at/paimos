/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-866 — v6-shell-only command coordinator.
 */

import { computed, onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

import {
  commandShortcutMatches,
  loadCommandPaletteSearch,
  loadCommandPaletteSettings,
  type CommandPaletteSearchWire,
  type CommandPaletteSettingsWire,
} from '@/v6/commandPalette'

export interface UsePaimos6CommandPaletteOptions {
  principalId: Readonly<Ref<number | null>>
  authorityKey: Readonly<Ref<string>>
  projectId: Readonly<Ref<number | null>>
  routeKey: Readonly<Ref<string>>
  loadSettings?: typeof loadCommandPaletteSettings
  loadSearch?: typeof loadCommandPaletteSearch
}

export type CommandPaletteLoadState = 'idle' | 'loading' | 'ready' | 'empty' | 'unavailable'

function editableTarget(event: KeyboardEvent): boolean {
  const target = event.composedPath()[0] instanceof HTMLElement
    ? event.composedPath()[0] as HTMLElement
    : event.target instanceof HTMLElement ? event.target : null
  return !!target && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}

export function usePaimos6CommandPalette(options: UsePaimos6CommandPaletteOptions) {
  const open = ref(false)
  const query = ref('')
  const settings = ref<CommandPaletteSettingsWire | null>(null)
  const settingsState = ref<CommandPaletteLoadState>('idle')
  const search = ref<CommandPaletteSearchWire | null>(null)
  const searchState = ref<CommandPaletteLoadState>('idle')
  const announcement = ref('')
  const effectiveShortcut = computed(() => settings.value?.effective_shortcut ?? null)
  const settingsLoader = options.loadSettings ?? loadCommandPaletteSettings
  const searchLoader = options.loadSearch ?? loadCommandPaletteSearch
  let settingsVersion = 0
  let searchVersion = 0
  let settingsController: AbortController | null = null
  let searchController: AbortController | null = null

  function clearSearch() {
    searchVersion += 1
    searchController?.abort()
    searchController = null
    search.value = null
    searchState.value = 'idle'
  }

  function close() {
    open.value = false
    query.value = ''
    announcement.value = ''
    clearSearch()
  }

  function show() {
    if (options.principalId.value === null) return
    close()
    open.value = true
  }

  async function reloadSettings() {
    const principalId = options.principalId.value
    const authorityKey = options.authorityKey.value
    const version = ++settingsVersion
    settingsController?.abort()
    settings.value = null
    settingsState.value = principalId === null ? 'idle' : 'loading'
    if (principalId === null) return
    const controller = new AbortController()
    settingsController = controller
    try {
      const result = await settingsLoader(controller.signal)
      if (version !== settingsVersion || controller.signal.aborted
        || principalId !== options.principalId.value || authorityKey !== options.authorityKey.value) return
      settings.value = result
      settingsState.value = 'ready'
    } catch {
      if (version !== settingsVersion || controller.signal.aborted
        || principalId !== options.principalId.value || authorityKey !== options.authorityKey.value) return
      settingsState.value = 'unavailable'
    } finally {
      if (settingsController === controller) settingsController = null
    }
  }

  async function runSearch(raw: string) {
    const trimmed = raw.trim()
    const principalId = options.principalId.value
    const authorityKey = options.authorityKey.value
    const projectId = options.projectId.value
    const version = ++searchVersion
    searchController?.abort()
    searchController = null
    search.value = null
    announcement.value = ''
    if (!open.value || trimmed === '') {
      searchState.value = 'idle'
      return
    }
    if (principalId === null || projectId === null || new TextEncoder().encode(trimmed).byteLength > 128) {
      searchState.value = 'unavailable'
      return
    }
    const controller = new AbortController()
    searchController = controller
    searchState.value = 'loading'
    try {
      const result = await searchLoader(projectId, trimmed, 8, controller.signal)
      if (version !== searchVersion || controller.signal.aborted || !open.value
        || raw !== query.value || principalId !== options.principalId.value
        || authorityKey !== options.authorityKey.value || projectId !== options.projectId.value) return
      search.value = result
      searchState.value = result.sessions.length + result.nodes.length + result.knowledge.length === 0 ? 'empty' : 'ready'
    } catch {
      if (version !== searchVersion || controller.signal.aborted || !open.value
        || raw !== query.value || principalId !== options.principalId.value
        || authorityKey !== options.authorityKey.value || projectId !== options.projectId.value) return
      searchState.value = 'unavailable'
    } finally {
      if (searchController === controller) searchController = null
    }
  }

  function onGlobalKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape' && open.value) {
      event.preventDefault()
      close()
      return
    }
    if (editableTarget(event) || effectiveShortcut.value === null
      || !commandShortcutMatches(event, effectiveShortcut.value)) return
    event.preventDefault()
    show()
  }

  watch(query, (value) => void runSearch(value), { flush: 'sync' })
  watch(options.authorityKey, () => {
    close()
    settingsVersion += 1
    settingsController?.abort()
    settingsController = null
    settings.value = null
    settingsState.value = options.principalId.value === null ? 'idle' : 'loading'
    void reloadSettings()
  }, { immediate: true, flush: 'sync' })
  watch([options.projectId, options.routeKey], close, { flush: 'sync' })

  onMounted(() => window.addEventListener('keydown', onGlobalKeydown, true))
  onBeforeUnmount(() => {
    window.removeEventListener('keydown', onGlobalKeydown, true)
    settingsVersion += 1
    settingsController?.abort()
    close()
  })

  return {
    open,
    query,
    settings,
    settingsState,
    search,
    searchState,
    announcement,
    effectiveShortcut,
    show,
    close,
    reloadSettings,
  }
}
