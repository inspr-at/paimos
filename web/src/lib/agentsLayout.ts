// SPDX-License-Identifier: AGPL-3.0-only
// The Agents page layout, per person and across devices (AEON-1210, design AEON-1196 §3.3).
// The `agents:layout` preference holds the folded project groups (`folded_projects`); the
// split handle joins it later (`handle`, package P5). A save sends the whole object it read,
// so one part never drops the other.
import { computed } from 'vue'
import { usePreference } from './preferences'

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
  const folded = computed(() => new Set(foldedProjects(pref.value.value)))
  function setFolded(fold: Iterable<string>, unfold: Iterable<string>, known?: ReadonlySet<string>) {
    pref.save({ ...(pref.value.value ?? {}), folded_projects: nextFolded([...folded.value], fold, unfold, known) }, 0)
  }
  return { folded, setFolded, get ready() { return pref.ready } }
}
