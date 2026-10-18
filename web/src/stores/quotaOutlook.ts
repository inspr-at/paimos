// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497: the Agents dial's quota outlook (GET /agent-accounts/quota-outlook). One read for
// every harness: the folded dial's mini lines, the rows' summaries and the open detail share it.
// Readings arrive about hourly, so a slow poll is enough; the snapshot belongs to one person
// and access epoch and is dropped when either changes.
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref } from 'vue'
import { onAccessRevalidate } from '../lib/authz'
import { onReset } from '../lib/position'
import { getQuotaOutlook, type QuotaOutlook } from '../lib/quotaOutlook'
import { initialRefreshStatus, usePolledData, usePoller } from '../lib/usePolledData'

export const OUTLOOK_POLL_MS = 120_000
/** How often a shown dial advances the age of the snapshot it already holds. */
const OUTLOOK_CLOCK_MS = 30_000

export const useQuotaOutlook = defineStore('quotaOutlook', () => {
  const read = usePolledData<QuotaOutlook | null>(getQuotaOutlook, null)
  // The server's age counters freeze at the response. This clock ages them from sampled_at and read_at.
  const now = ref(Date.now())
  // A click passes its event; the poller passes nothing. Both only mean "read again".
  async function refresh(..._extra: unknown[]) {
    const outcome = await read.refresh()
    now.value = Date.now()
    return outcome
  }
  const poller = usePoller(() => refresh(), OUTLOOK_POLL_MS, { invalidate: read.invalidate })
  let users = 0
  let clock: ReturnType<typeof setInterval> | undefined
  /** A shown dial reads; the last one to go stops the poll and the age clock. */
  function use() {
    if (users++ === 0) {
      now.value = Date.now()
      poller.start(true)
      clock = setInterval(() => { now.value = Date.now() }, OUTLOOK_CLOCK_MS)
    }
    return () => {
      if (--users === 0) {
        poller.stop()
        if (clock) { clearInterval(clock); clock = undefined }
      }
    }
  }
  // Loading until the first answer; a failed first read is an error. A later failure keeps the
  // last answer and says so, so the chart stays and the muted line can offer another try.
  const state = computed<'loading' | 'ready' | 'error'>(() => read.data.value ? 'ready' : read.status.value.state === 'error' || read.status.value.state === 'forbidden' ? 'error' : 'loading')
  const failed = computed(() => read.status.value.failures > 0 && read.data.value != null)
  const groups = computed(() => new Map((read.data.value?.groups ?? []).map(g => [g.harness, g])))
  function reset() {
    read.invalidate()
    read.data.value = null
    read.status.value = initialRefreshStatus()
  }
  onScopeDispose(onReset(reset))
  onScopeDispose(onAccessRevalidate({ reset, refresh: () => { void refresh() } }))
  onScopeDispose(() => { if (clock) clearInterval(clock) })
  return { data: read.data, groups, state, failed, now, refresh, use, reset }
})
