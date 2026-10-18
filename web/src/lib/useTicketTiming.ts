// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Tickets' reads (AEON-1359): one bounded answer per project, window and filters, refreshed every minute
// while shown. A changed window or filter keeps the previous answer on screen, marked busy, until the new one lands
// in one render; an answer seen before lands at once. A failed read shows no figures: an old answer never stands in.
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { Final } from './deliveryTickets'
import type { WindowDays } from './deliveryNumbers'
import { onAccessRevalidate } from './authz'
import { keepTypicals, readTicketTiming, type TicketStatusTiming, type TimingFilters } from './statusTiming'
import { usePoller } from './usePolledData'
import { useIdentityScope } from './useIdentityScope'

// The project's last step does not depend on the window or filters; remembered so a revisit lays out its tiles at once.
const finals = new Map<string, Final>()
const CACHE_TTL = 60_000

export function useTicketTiming(options: { projectId: () => string; window: () => WindowDays; filters: () => TimingFilters; active: () => boolean }) {
  const scope = useIdentityScope(), reads = scope.lane()
  const data = shallowRef<TicketStatusTiming | null>(null)
  const status = ref<'loading' | 'ready' | 'error'>('loading')
  const busy = ref(false)
  const final = ref<Final>(finals.get(options.projectId()) ?? 'delivered')
  const cache = new Map<string, { at: number; answer: TicketStatusTiming }>()
  // The key of the answer on screen: a refresh of it is silent, only another window or filter shows busy.
  let shown = ''
  const keyOf = () => { const f = options.filters(); return `${scope.owner.value}|${options.projectId()}|${options.window()}|${f.type}|${f.priority}|${f.worked_by}` }

  function load(): Promise<unknown> {
    if (!options.active()) return Promise.resolve()
    const key = keyOf(), project = options.projectId(), window = options.window(), filters = { ...options.filters() }
    busy.value = status.value === 'ready' && shown !== key
    if (status.value === 'error') status.value = 'loading'
    return reads.run(({ after, signal }) => after(readTicketTiming(project, window, filters, signal), answer => {
      if (key !== keyOf()) return
      cache.set(key, { at: Date.now(), answer })
      keepTypicals(scope.owner.value, answer, filters)
      finals.set(project, answer.final_status)
      final.value = answer.final_status
      data.value = answer
      shown = key
      status.value = 'ready'
      busy.value = false
    }), { failed: () => {
      if (key !== keyOf()) return
      data.value = null
      shown = ''
      status.value = 'error'
      busy.value = false
    } })
  }
  function show() {
    const kept = cache.get(keyOf())
    if (kept && Date.now() - kept.at < CACHE_TTL) { reads.cancel(); data.value = kept.answer; shown = keyOf(); status.value = 'ready'; busy.value = false; return }
    void load()
  }
  const poller = usePoller(load, 60_000, { enabled: options.active, invalidate: () => reads.cancel() })
  onMounted(() => poller.start(false))
  onBeforeUnmount(() => { poller.stop(); reads.cancel() })
  // A permission reset drops the read in flight with the scope; ask again in the new one.
  onBeforeUnmount(onAccessRevalidate({ reset: () => { if (status.value === 'loading' || busy.value) void load() } }))
  watch(() => [options.projectId(), scope.owner.value], () => {
    data.value = null; shown = ''; status.value = 'loading'; busy.value = false; cache.clear()
    final.value = finals.get(options.projectId()) ?? 'delivered'
  }, { flush: 'sync' })
  watch(() => [options.projectId(), options.window(), keyOf(), options.active()], () => { if (options.active()) show() }, { immediate: true })
  function retry() { status.value = 'loading'; data.value = null; shown = ''; void load() }
  return { data, status, busy, final, retry }
}
