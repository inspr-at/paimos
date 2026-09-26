// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError } from '../lib/api'
import { getLiveAgents } from '../lib/agents'
import { LIVE_FRESH_MS, LIVE_POLL_MS, advanceActivity, agentKey, groupLive, sameLive, skewOf, type ActivityEvidence, type LiveAgent } from '../lib/liveAgents'

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
  const skew = ref(0)
  const fresh = ref(LIVE_FRESH_MS)
  const now = ref(Date.now())
  const state = ref<'idle' | 'ready' | 'unavailable'>('idle')
  const evidence = ref(new Map<string, ActivityEvidence>())
  const evidenceKey = (agent: LiveAgent) => `${agent.project_id}:${agentKey(agent)}`
  const eventPulseFor = (agent: LiveAgent) => evidence.value.get(evidenceKey(agent))?.pulse ?? 0
  let watchers = 0
  let poll: ReturnType<typeof setTimeout> | undefined
  let ticker: ReturnType<typeof setInterval> | undefined
  let fetchedAt = 0
  let inflight: Promise<void> | null = null

  const serverNow = computed(() => now.value - skew.value)
  // The same Map while nothing visible changed: cards and labels stay put between polls and ticks.
  const byProject = computed<Map<string, LiveAgent[]>>(previous => {
    const next = groupLive(items.value, serverNow.value, fresh.value)
    return previous && sameLive(previous, next) ? previous : next
  })
  const forProject = (projectId: string) => byProject.value.get(projectId) ?? NONE

  function refresh(): Promise<void> {
    inflight ??= (async () => {
      let wait = LIVE_POLL_MS
      try {
        const page = await getLiveAgents()
        const at = Date.now()
        items.value = Array.isArray(page.items) ? page.items : []
        evidence.value = new Map(items.value.map(agent => [evidenceKey(agent), advanceActivity(evidence.value.get(evidenceKey(agent)), agent)]))
        skew.value = skewOf(page, at)
        if (Number.isFinite(page.fresh_seconds) && page.fresh_seconds > 0) fresh.value = page.fresh_seconds * 1000
        state.value = 'ready'
        now.value = at
      } catch (e) {
        if (e instanceof APIError && (e.status === 401 || e.status === 403 || e.status === 404)) { state.value = 'unavailable'; items.value = []; evidence.value.clear(); wait = 0 }
        else wait = 60_000
      } finally {
        fetchedAt = Date.now()
        inflight = null
        schedule(wait)
      }
    })()
    return inflight
  }
  function schedule(wait = LIVE_POLL_MS) {
    clearTimeout(poll)
    poll = undefined
    if (!watchers || !wait || document.visibilityState === 'hidden') return
    poll = setTimeout(() => void refresh(), wait)
  }
  function tick() {
    clearInterval(ticker)
    ticker = undefined
    if (!watchers || document.visibilityState === 'hidden') return
    ticker = setInterval(() => { if (items.value.length) now.value = Date.now() }, TICK_MS)
  }
  function visibility() {
    if (document.visibilityState === 'hidden') { clearTimeout(poll); poll = undefined; clearInterval(ticker); ticker = undefined; return }
    now.value = Date.now()
    tick()
    if (state.value === 'unavailable') return
    if (Date.now() - fetchedAt >= LIVE_POLL_MS / 2) void refresh(); else schedule(LIVE_POLL_MS - (Date.now() - fetchedAt))
  }
  function watch() {
    watchers++
    if (watchers === 1) {
      document.addEventListener('visibilitychange', visibility)
      tick()
      if (state.value !== 'unavailable' && document.visibilityState !== 'hidden') void refresh()
    }
    let stopped = false
    return () => {
      if (stopped) return
      stopped = true
      if (--watchers > 0) return
      document.removeEventListener('visibilitychange', visibility)
      clearTimeout(poll); poll = undefined
      clearInterval(ticker); ticker = undefined
    }
  }

  return { items, state, now, serverNow, byProject, forProject, eventPulseFor, refresh, watch }
})
