// SPDX-License-Identifier: AGPL-3.0-only
// The dial's remembered state per person (AEON-1036). A change shows at once; writes
// are serial and always send the whole current object, so the last change wins and
// one part is never reset by another one's write. Without a successful read the
// stored state is unknown and is never overwritten with defaults.
import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { api } from '../lib/api'
import { DIAL_KEY, dialCacheKey, foldKey, readDialPrefs, withDialDefaults, type DialFold, type DialPrefs } from '../lib/dialPrefs'
import { useSession } from './session'

function cached(viewer: string): Partial<DialPrefs> {
  try { return readDialPrefs(JSON.parse(localStorage.getItem(dialCacheKey(viewer)) ?? 'null')) } catch { return {} }
}
function remember(viewer: string, value: DialPrefs) {
  try { localStorage.setItem(dialCacheKey(viewer), JSON.stringify(value)) } catch { /* storage may be disabled */ }
}
async function readPreference(): Promise<unknown> {
  const response = await api(`/preferences/${DIAL_KEY}`)
  if (!response.ok) throw new Error(`preference ${response.status}`)
  return ((await response.json()) as { value?: unknown } | null)?.value ?? null
}

export const useDialPrefs = defineStore('dialPrefs', () => {
  const session = useSession()
  const viewer = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const prefs = ref<DialPrefs>(withDialDefaults()), error = ref('')
  let generation = 0, loaded = false, touched = new Set<keyof DialPrefs>(), tail: Promise<void> = Promise.resolve()

  async function load(turn: number, who: string) {
    const server = readDialPrefs(await readPreference())
    if (turn !== generation) return
    const next = withDialDefaults(server)
    // A part changed while the read was in flight keeps the person's choice.
    for (const key of touched) (next as unknown as Record<string, unknown>)[key] = prefs.value[key]
    prefs.value = next; loaded = true
    remember(who, next)
  }
  watch(viewer, who => {
    const turn = ++generation
    loaded = false; touched = new Set(); error.value = ''
    prefs.value = withDialDefaults(who ? cached(who) : {})
    tail = who ? load(turn, who).catch(() => { /* the cache or the defaults stay; a change reads again */ }) : Promise.resolve()
  }, { immediate: true, flush: 'sync' })

  function change(part: keyof DialPrefs, next: DialPrefs) {
    const who = viewer.value, turn = generation
    prefs.value = next
    if (!who) return
    touched.add(part); error.value = ''
    remember(who, next)
    tail = tail.catch(() => {}).then(async () => {
      if (turn !== generation) return
      try {
        if (!loaded) await load(turn, who)
        if (turn !== generation) return
        const response = await api(`/preferences/${DIAL_KEY}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value: prefs.value }) })
        if (!response.ok) throw new Error(`preference ${response.status}`)
      } catch {
        if (turn === generation) error.value = 'Couldn’t remember the dial’s view. Please try again.'
      }
    })
  }
  const setInfo = (open: boolean) => { if (prefs.value.info_open !== open) change('info_open', { ...prefs.value, info_open: open }) }
  const select = (harness: string) => { if (prefs.value.selected !== harness) change('selected', { ...prefs.value, selected: harness }) }
  const foldOpen = (harness: string, fold: DialFold) => prefs.value.folds[foldKey(harness, fold)] === true
  const toggleFold = (harness: string, fold: DialFold) => change('folds', { ...prefs.value, folds: { ...prefs.value.folds, [foldKey(harness, fold)]: !foldOpen(harness, fold) } })
  return { prefs, error, setInfo, select, foldOpen, toggleFold }
})
