// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError } from '../lib/api'
import { getLiveAgents } from '../lib/agentRows'
import { LIVE_POLL_MS, advanceActivity, agentKey, groupLive, sameLive, skewOf, type ActivityEvidence, type LiveAgent } from '../lib/liveAgents'
import { useAgentAppearance } from '../lib/agentAppearance'
import { usePolledData, usePoller } from '../lib/usePolledData'
import { subscribeAgents } from '../lib/agents'
import { createReadOrder } from '../lib/position'

const NONE: LiveAgent[] = []
// Elapsed times move on at this pace while someone is at work.
const TICK_MS = 10_000

// The agents working right now, for every visible project (AEON-184). A page
// that shows them calls watch() and the returned stop(): while anyone watches
// and the tab is visible it asks every 20 seconds; a hidden tab asks nothing
// and catches up the moment it is shown again. A server that forbids or lacks
// the read ends the polling quietly; a failed read retries a minute later.
export const useLiveAgents = defineStore('liveAgents', () => {
  const items = ref<LiveAgent[]>([])
  const { choice: statePreferences, ready: preferencesReady } = useAgentAppearance()
  const skew = ref(0)
  const now = ref(Date.now())
  const unavailable = ref(false)
  const truncated = ref(false)
  const evidence = ref(new Map<string, ActivityEvidence>())
  const evidenceKey = (agent: LiveAgent) => `${agent.project_id}:${agentKey(agent)}`
  const eventPulseFor = (agent: LiveAgent) => evidence.value.get(evidenceKey(agent))?.pulse ?? 0
  let watchers = 0
  let ticker: ReturnType<typeof setInterval> | undefined
  let stopStream: (() => void) | undefined
  const reading = usePolledData(async () => {
    try { await preferencesReady; return await getLiveAgents() }
    catch (e) {
      if (e instanceof APIError && [401, 403, 404].includes(e.status)) { unavailable.value = true; items.value = []; evidence.value = new Map(); truncated.value = false }
      throw e
    }
  }, { items: [], at: '', fresh_seconds: 120 }, page => {
    const at = Date.now()
    items.value = Array.isArray(page.items) ? page.items : []
    truncated.value = page.truncated === true
    evidence.value = new Map(items.value.map(agent => [evidenceKey(agent), advanceActivity(evidence.value.get(evidenceKey(agent)), agent)]))
    skew.value = skewOf(page, at)
    now.value = at
  }, { order: createReadOrder() })
  const state = computed<'idle' | 'ready' | 'unavailable'>(() => unavailable.value ? 'unavailable' : reading.status.value.updatedAt === null ? 'idle' : 'ready')

  const serverNow = computed(() => now.value - skew.value)
  // The same Map while nothing visible changed: cards and labels stay put between polls and ticks.
  const byProject = computed<Map<string, LiveAgent[]>>(previous => {
    const next = groupLive(items.value, serverNow.value, statePreferences.value)
    return previous && sameLive(previous, next) ? previous : next
  })
  const forProject = (projectId: string) => byProject.value.get(projectId) ?? NONE

  const refresh = reading.refresh
  const poller = usePoller(refresh, LIVE_POLL_MS, { enabled: () => !unavailable.value, invalidate: reading.invalidate })
  function tick() {
    clearInterval(ticker)
    ticker = undefined
    if (!watchers || document.visibilityState === 'hidden') return
    ticker = setInterval(() => { if (items.value.length) now.value = Date.now() }, TICK_MS)
  }
  function visibility() { now.value = Date.now(); tick() }
  // Hints invalidate an older read and ask for one catch-up, even when a poll
  // is already running. The shared poller coalesces bursts and respects hidden tabs.
  function changed() { reading.invalidate(); poller.tick(true) }
  function watch() {
    watchers++
    if (watchers === 1) {
      document.addEventListener('visibilitychange', visibility)
      tick()
      poller.start(true)
      stopStream = subscribeAgents(changed, connected => { if (!connected) reading.invalidate() }, undefined, true)
    }
    let stopped = false
    return () => {
      if (stopped) return
      stopped = true
      if (--watchers > 0) return
      document.removeEventListener('visibilitychange', visibility)
      poller.stop()
      stopStream?.(); stopStream = undefined
      clearInterval(ticker); ticker = undefined
    }
  }

  return { items, state, now, serverNow, byProject, forProject, eventPulseFor, refresh, watch, truncated, pollStale: reading.stale }
})
