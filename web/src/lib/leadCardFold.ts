// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1027: which projects' "No lead" card this person folded. One small
// per-person preference holds the project ids; a project that gets a lead drops
// out again, so a folded card never outlives the state it folds.
import { computed, ref, watch, type Ref } from 'vue'
import { usePreference } from './preferences'

export const LEAD_CARD_KEY = 'ui.project.lead-card'
/** 200 ids stay far below the server's 16 KiB preference limit; the oldest fold goes first. */
export const MAX_FOLDED = 200
interface LeadCardPrefs { collapsed?: string[] }

/** Only ids count; anything else in a stored value is ignored. */
export function readCollapsed(value: unknown): string[] {
  const list = value && typeof value === 'object' ? (value as Record<string, unknown>).collapsed : undefined
  if (!Array.isArray(list)) return []
  const ids = list.filter((id): id is string => typeof id === 'string' && id.length > 0 && id.length <= 64)
  return [...new Set(ids)].slice(-MAX_FOLDED)
}

/** The list with one project folded or unfolded; a fold is the newest entry. */
export function withCollapsed(current: string[], project: string, collapsed: boolean): string[] {
  const rest = current.filter(id => id !== project)
  return (collapsed ? [...rest, project] : rest).slice(-MAX_FOLDED)
}

const reducedMotion = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

/**
 * The fold of one project's "No lead" card, per person (the preference follows the signed-in person).
 * `ready` is true once that person's preference has been read, so the card opens in its final shape
 * and never jumps; `moving` is true only while a person's own toggle animates.
 */
export function useLeadCardFold(project: Ref<string>, viewer: Ref<string>, hasLead: Ref<boolean>) {
  const pref = usePreference<LeadCardPrefs>(LEAD_CARD_KEY)
  const ready = ref(false), moving = ref(false)
  let turn = 0
  // A new person has their own preference: wait for its read before the card shows.
  watch(viewer, () => {
    const mine = ++turn
    ready.value = false; moving.value = false
    void pref.ready.then(() => { if (mine === turn) ready.value = true })
  }, { immediate: true, flush: 'sync' })
  // Another project applies at once, with no motion.
  watch(project, () => { moving.value = false }, { flush: 'sync' })

  const stored = () => readCollapsed(pref.value.value)
  const collapsed = computed(() => ready.value && stored().includes(project.value))
  // No debounce: a fold is a single deliberate act, and the write is serial behind earlier ones.
  const write = (id: string, fold: boolean) => pref.save({ collapsed: withCollapsed(stored(), id, fold) }, 0)

  function toggle() {
    if (!ready.value) return
    // The project on screen when the person acts, not whichever shows when the write runs.
    const id = project.value, fold = !collapsed.value
    write(id, fold)
    // Without a signed-in owner nothing was saved and nothing moves.
    moving.value = collapsed.value === fold && !reducedMotion()
  }
  function settle(event: TransitionEvent) {
    if (event.target === event.currentTarget && event.propertyName === 'grid-template-rows') moving.value = false
  }
  // Once the project has a lead the card is gone: the next time it has none it opens.
  watch([ready, hasLead, project, () => pref.value.value], ([isReady, has, id]) => {
    if (isReady && has && stored().includes(id)) write(id, false)
  }, { flush: 'sync' })
  return { ready, collapsed, moving, toggle, settle }
}
