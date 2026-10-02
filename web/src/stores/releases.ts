// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref, shallowRef } from 'vue'
import { api } from '../lib/api'
import { useDeveloperSettings } from '../lib/developerSettings'
import { rememberCodename, rememberCodenames } from '../lib/codenames'
import { usePreference } from '../lib/preferences'
import { getReleases, isCalendarVersion, newSince, type ReleaseHistory } from '../lib/releases'
import { useVersion } from './version'

export const isCalendar = isCalendarVersion

// The release history, what is new since the person last looked, and whether the
// server now runs a newer version than this page.
export const useReleases = defineStore('releases', () => {
  const version = useVersion()
  const { showReservedVersions, loading: developerSettingsLoading, ready: developerSettingsReady } = useDeveloperSettings()
  const history = ref<ReleaseHistory | null>(null)
  const error = ref('')
  const loading = ref(false)
  let request: Promise<void> | undefined
  function load(force = false): Promise<void> {
    if (request) return request
    if (history.value && !force) return Promise.resolve()
    loading.value = true
    request = (async () => {
      try { history.value = await getReleases(); rememberCodenames(history.value.releases); error.value = '' }
      catch (e) { error.value = e instanceof Error ? e.message : 'The release history could not be loaded.' }
      finally { loading.value = false; request = undefined }
    })()
    return request
  }

  // The running version: the server's answer once the history is loaded.
  const current = computed(() => history.value?.current ?? version.value?.version ?? '')

  // ---------- New since the last visit (a per-person preference) ----------
  // Read only once signed in (start), so a signed-out page never caches an empty answer.
  const seen = shallowRef<ReturnType<typeof usePreference<{ last_seen?: string }>> | null>(null)
  const lastSeen = computed(() => { const v = seen.value?.value.value?.last_seen; return isCalendar(v) ? v : null })
  // A first visit remembers the running version quietly: nothing is "new" to someone who just arrived.
  let starting: Promise<void> | null = null
  function start() { return starting ??= begin() }
  async function begin() {
    const pref = seen.value ??= usePreference<{ last_seen?: string }>('releases')
    await Promise.all([pref.ready, version.load()])
    if (!lastSeen.value && isCalendar(version.value?.version)) pref.save({ last_seen: version.value.version }, 0)
    // Something is new: count it now rather than after the page settles.
    else if (unseen.value) void load()
  }
  const unseen = computed(() => isCalendar(current.value) && !!lastSeen.value && current.value > lastSeen.value)
  // How many releases are new; wait for visibility preferences and the history.
  const newCount = computed<number | null>(() => {
    if (developerSettingsLoading.value) return null
    if (!unseen.value) return 0
    if (!history.value) return null
    return newSince(history.value.releases.filter(r => r.version <= current.value), lastSeen.value, showReservedVersions.value).size
  })
  // Opening the history keeps what was new highlighted while it is open, then marks it seen.
  const highlight = ref(new Set<string>())
  // Waits for the stored last visit and visibility choice so a cold deep link highlights correctly.
  async function markSeen() {
    if (starting) await starting
    await developerSettingsReady.value
    highlight.value = history.value ? newSince(history.value.releases.filter(r => r.version <= current.value), lastSeen.value, showReservedVersions.value) : new Set()
    if (seen.value && isCalendar(current.value) && current.value !== lastSeen.value) seen.value.save({ last_seen: current.value }, 0)
  }

  // ---------- A newer version on the server ----------
  const available = ref<string | null>(null)
  let lastCheck = 0
  async function checkForUpdate() {
    const running = version.value?.version
    if (!isCalendar(running) || Date.now() - lastCheck < 10_000) return
    lastCheck = Date.now()
    try {
      const response = await api('/version', { cache: 'no-store' })
      if (!response.ok) return
      const body = await response.json()
      rememberCodename(body?.version, body?.codename)
      if (isCalendar(body?.version) && body.version > running && body.version !== available.value) available.value = body.version
    } catch { /* offline: try again later */ }
  }

  return { history, error, loading, load, current, lastSeen, newCount, highlight, start, markSeen, available, checkForUpdate }
})
