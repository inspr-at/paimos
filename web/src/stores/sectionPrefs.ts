// SPDX-License-Identifier: AGPL-3.0-only
// Open or folded Agents page sections, per person and across devices (AEON-781).
// A toggle shows at once; writes are serial and always send the whole current
// object, so the last toggle wins and no section is reset by another one's write.
import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { api } from '../lib/api'
import { cacheKey, DEFAULT_SECTIONS, LEGACY_DIAL_KEY, readLegacyDial, readSections, SECTIONS_KEY, withDefaults, type SectionKey, type SectionsOpen } from '../lib/sectionPrefs'
import { useSession } from './session'

function cached(viewer: string): Partial<SectionsOpen> {
  try { return readSections(JSON.parse(localStorage.getItem(cacheKey(viewer)) ?? 'null')) } catch { return {} }
}
function remember(viewer: string, value: SectionsOpen) {
  try { localStorage.setItem(cacheKey(viewer), JSON.stringify(value)) } catch { /* storage may be disabled */ }
}
async function readPreference(key: string): Promise<unknown> {
  const response = await api(`/preferences/${key}`)
  if (!response.ok) throw new Error(`preference ${response.status}`)
  return ((await response.json()) as { value?: unknown } | null)?.value ?? null
}

export const useSectionPrefs = defineStore('sectionPrefs', () => {
  const session = useSession()
  const viewer = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const open = ref<SectionsOpen>({ ...DEFAULT_SECTIONS }), error = ref('')
  let generation = 0, loaded = false, touched = new Set<SectionKey>(), tail: Promise<void> = Promise.resolve()

  async function load(turn: number, who: string) {
    const value = await readPreference(SECTIONS_KEY)
    const server = readSections(value)
    const legacy = 'dial' in server ? {} : readLegacyDial(await readPreference(LEGACY_DIAL_KEY).catch(() => null))
    if (turn !== generation) return
    const next = withDefaults(legacy, server)
    // A section toggled while the read was in flight keeps the person's choice.
    for (const key of touched) next[key] = open.value[key]
    open.value = next; loaded = true
    remember(who, next)
  }
  watch(viewer, who => {
    const turn = ++generation
    loaded = false; touched = new Set(); error.value = ''
    open.value = withDefaults(who ? cached(who) : {})
    tail = who ? load(turn, who).catch(() => { /* the cache or the defaults stay; a toggle reads again */ }) : Promise.resolve()
  }, { immediate: true, flush: 'sync' })

  function toggle(key: SectionKey) {
    const who = viewer.value, turn = generation
    open.value = { ...open.value, [key]: !open.value[key] }
    if (!who) return
    touched.add(key); error.value = ''
    remember(who, open.value)
    tail = tail.catch(() => {}).then(async () => {
      if (turn !== generation) return
      try {
        // Without a successful read the other sections' stored state is unknown; never overwrite it with defaults.
        if (!loaded) await load(turn, who)
        if (turn !== generation) return
        const response = await api(`/preferences/${SECTIONS_KEY}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value: open.value }) })
        if (!response.ok) throw new Error(`preference ${response.status}`)
      } catch {
        if (turn === generation) error.value = 'Couldn’t remember which sections are open. Please try again.'
      }
    })
  }
  return { open, error, toggle }
})
