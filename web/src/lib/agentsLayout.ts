// SPDX-License-Identifier: AGPL-3.0-only
// The Agents page layout, per person and across devices (AEON-1210, design AEON-1196 §3.3).
// The `agents:layout` preference holds the folded project groups (`folded_projects`); the
// split handle shares it (`handle`, package P5). Saves merge into a successfully read
// object, so one part never drops the other, including after a failed read.
import { computed, shallowRef, watch } from 'vue'
import { preferenceOwner, usePreference } from './preferences'

export const AGENTS_LAYOUT_KEY = 'agents:layout'
export interface AgentsLayout { folded_projects?: string[]; [key: string]: unknown }
/** At most this many folded projects, so the object stays far under the server's 16 KiB preference limit. */
export const MAX_FOLDED = 300
const READ_LIMIT = 2000
const PROJECT_ID = /^[A-Za-z0-9_.:-]{1,64}$/

/** The folded project ids a stored value holds: well-formed ids only, each once, bounded. */
export function foldedProjects(value: unknown): string[] {
  const raw = value && typeof value === 'object' ? (value as AgentsLayout).folded_projects : undefined
  if (!Array.isArray(raw)) return []
  const out = new Set<string>()
  for (const id of raw.slice(0, READ_LIMIT)) {
    if (typeof id === 'string' && PROJECT_ID.test(id)) out.add(id)
    if (out.size === MAX_FOLDED) break
  }
  return [...out]
}

/** The folded list after folding and unfolding some projects; with `known`, projects that are gone leave it. */
export function nextFolded(current: readonly string[], fold: Iterable<string>, unfold: Iterable<string>, known?: ReadonlySet<string>): string[] {
  const out = new Set(current)
  for (const id of unfold) out.delete(id)
  for (const id of fold) if (PROJECT_ID.test(id)) { out.delete(id); out.add(id) }
  // The latest folds win when the list is over its bound.
  return [...out].filter(id => !known?.size || known.has(id)).slice(-MAX_FOLDED)
}

export function useAgentsLayout() {
  const pref = usePreference<AgentsLayout>(AGENTS_LAYOUT_KEY)
  const pending = shallowRef<{ fold: Set<string>; unfold: Set<string>; known?: ReadonlySet<string> } | null>(null)
  const folded = computed(() => {
    const current = foldedProjects(pref.value.value), intent = pending.value
    return new Set(intent ? nextFolded(current, intent.fold, intent.unfold, intent.known) : current)
  })
  // A pending read or fold cannot outlive its person, even on A → B → A.
  watch(preferenceOwner, () => { pending.value = null }, { flush: 'sync' })
  async function savePending() {
    const intent = pending.value
    if (!intent || !await pref.ensureLoaded() || pending.value !== intent) return
    pref.save({ ...(pref.value.value ?? {}), folded_projects: nextFolded(foldedProjects(pref.value.value), intent.fold, intent.unfold, intent.known) }, 0)
    pending.value = null
  }
  // Another layout consumer may recover the shared read first.
  watch(pref.value, () => { if (pending.value) void savePending() })
  function setFolded(fold: Iterable<string>, unfold: Iterable<string>, known?: ReadonlySet<string>) {
    const folds = new Set(pending.value?.fold), unfolds = new Set(pending.value?.unfold)
    for (const id of unfold) { folds.delete(id); unfolds.add(id) }
    for (const id of fold) if (PROJECT_ID.test(id)) { unfolds.delete(id); folds.delete(id); folds.add(id) }
    pending.value = { fold: folds, unfold: unfolds, known: known && new Set(known) }
    void savePending()
  }
  return { folded, setFolded, get ready() { return pref.ready } }
}
