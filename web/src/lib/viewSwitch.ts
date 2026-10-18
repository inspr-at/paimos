// SPDX-License-Identifier: AGPL-3.0-only
// One way to remember a view (AEON-1453). Every view switch keeps, per person and
// workspace on the server, the view used last and, where the person chose one with
// right-click or long-press, an explicit default. The default wins over the view used
// last; a link (?view=, ?tab=, ?mode=) wins once and changes neither. One key per
// switch, per project where the switch belongs to a project. Never browser storage.
import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { preferenceGeneration, preferenceOwner, preferenceSaves, usePreference } from './preferences'

export interface ViewMemory { last?: string; default?: string | null }
// What an older store held before the switch had its own key: read, never written.
export interface LegacyView { last?: unknown; default?: unknown }

export const viewKey = (name: string, scope?: string | null) => scope ? `view:${name}:${scope}` : `view:${name}`
export const defaultTip = (label: string) => `${label} · default`
export const SAVED_VIEW = 'saved:'

function valid(value: unknown, options: readonly string[]): string | null {
  return typeof value === 'string' && options.includes(value) ? value : null
}
// The explicit default; a default that was set and cleared stays cleared (null), so an
// older store's value only counts while the person never chose here.
export function memoryDefault(memory: ViewMemory | null, options: readonly string[], legacy?: LegacyView | null): string | null {
  if (memory && memory.default !== undefined) return valid(memory.default, options)
  return valid(legacy?.default, options)
}
export function memoryLast(memory: ViewMemory | null, options: readonly string[], legacy?: LegacyView | null): string | null {
  return valid(memory?.last, options) ?? valid(legacy?.last, options)
}
// The view a switch opens with when no link asks for one.
export function openingView(memory: ViewMemory | null, options: readonly string[], fallback: string, legacy?: LegacyView | null): string {
  return memoryDefault(memory, options, legacy) ?? memoryLast(memory, options, legacy) ?? fallback
}
export function readMemory(value: unknown): ViewMemory | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const raw = value as Record<string, unknown>
  const memory: ViewMemory = {}
  if (typeof raw.last === 'string') memory.last = raw.last
  if (typeof raw.default === 'string' || raw.default === null) memory.default = raw.default as string | null
  return memory
}

export interface ViewSwitchOptions {
  // The preference key; null while the person or the project is not known yet.
  key: MaybeRefOrGetter<string | null>
  options: MaybeRefOrGetter<readonly string[]>
  fallback: MaybeRefOrGetter<string>
  // The view a link asks for (?view=, ?tab=, ?mode=). It wins until the person chooses.
  link?: MaybeRefOrGetter<unknown>
  // An older store of this switch: its key and how to read last/default from it.
  legacy?: { key: MaybeRefOrGetter<string | null>; read: (value: Record<string, unknown> | null) => LegacyView | null }
  // Views that never become the one used last (a settings page is visited, not chosen to read).
  remembers?: (id: string) => boolean
}

export function useViewSwitch(config: ViewSwitchOptions) {
  const key = computed(() => toValue(config.key) || null)
  const options = computed(() => toValue(config.options))
  const fallback = computed(() => toValue(config.fallback))
  const pref = computed(() => key.value ? usePreference<ViewMemory>(key.value) : null)
  const legacyKey = computed(() => config.legacy ? toValue(config.legacy.key) || null : null)
  const legacyPref = computed(() => legacyKey.value ? usePreference<Record<string, unknown>>(legacyKey.value) : null)
  const memory = computed(() => readMemory(pref.value?.value.value))
  const legacy = computed(() => config.legacy && legacyPref.value ? config.legacy.read(legacyPref.value.value.value ?? null) : null)

  // The memory for this key (and its older store) was read, or the read failed: the
  // switch may open now without showing one view first and then another.
  const loaded = ref(false)
  let turn = 0
  async function settle(): Promise<void> {
    const mine = ++turn
    loaded.value = false
    const reads = [pref.value?.ready, legacyPref.value?.ready].filter(Boolean)
    if (!key.value) return
    await Promise.all(reads)
    if (mine === turn) loaded.value = true
  }
  watch([key, legacyKey, preferenceOwner], () => { void settle() }, { immediate: true })
  function ready(): Promise<void> {
    return Promise.all([pref.value?.ready, legacyPref.value?.ready]).then(() => undefined)
  }

  const defaultId = computed(() => memoryDefault(memory.value, options.value, legacy.value))
  const lastId = computed(() => memoryLast(memory.value, options.value, legacy.value))
  const preferred = computed(() => openingView(memory.value, options.value, fallback.value, legacy.value))
  const linked = computed(() => valid(config.link === undefined ? null : toValue(config.link), options.value))
  // The person's choice on this visit. A new link, another key, or another person starts from the link or the memory again.
  const chosen = ref<string | null>(null)
  // Sync: the choice drops with the preference cache, before a write queued on the old epoch resumes.
  watch([key, preferenceOwner], () => { chosen.value = null }, { flush: 'sync' })
  watch(linked, value => { if (value) chosen.value = null })
  const current = computed(() => valid(chosen.value, options.value) ?? linked.value ?? preferred.value)

  // Writes merge into what the server holds; a value that could not be read is never overwritten.
  // The promise is true only after the server has accepted the value (or already held it).
  async function write(change: (now: ViewMemory) => ViewMemory | null): Promise<boolean> {
    const at = key.value, target = pref.value
    const owner = preferenceOwner.value, generation = preferenceGeneration()
    const same = () => key.value === at && preferenceOwner.value === owner && preferenceGeneration() === generation
    if (!at || !target || !(await target.ensureLoaded()) || !same()) return false
    const now = readMemory(target.value.value) ?? {}
    const next = change(now)
    if (!next || !same()) return target.done()
    return target.save(next as ViewMemory & object, 0)
  }
  function remember(id: string): Promise<boolean> {
    if (!options.value.includes(id) || config.remembers?.(id) === false) return Promise.resolve(false)
    const outstanding = `${preferenceOwner.value}/${key.value}`
    return write(now => now.last === id && !preferenceSaves.failed.has(outstanding) ? null : { ...now, last: id })
  }
  // The memory on the server, including a write this visit that has not answered yet.
  function confirmed(): Promise<boolean> {
    const target = pref.value
    if (!key.value || !target) return Promise.resolve(false)
    return target.done()
  }
  function choose(id: string) {
    if (!options.value.includes(id)) return
    chosen.value = id
    remember(id)
  }
  function setDefault(id: string | null) {
    if (id !== null && !options.value.includes(id)) return
    // A new default is for the next visit: the view on screen stays as it is.
    chosen.value = current.value
    void write(now => now.default === id ? null : { ...now, default: id })
  }
  // The last save of this switch's memory failed (the Settings footer offers Retry).
  const failed = computed(() => !!key.value && preferenceSaves.failed.has(`${preferenceOwner.value}/${key.value}`))

  return { key, memory, loaded, ready, defaultId, lastId, preferred, linked, chosen, current, choose, remember, confirmed, setDefault, failed }
}
export type ViewSwitchState = ReturnType<typeof useViewSwitch>
