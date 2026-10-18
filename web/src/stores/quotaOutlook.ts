// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497: the Agents dial's quota outlook (GET /agent-accounts/quota-outlook). One read for
// every harness: the folded dial's mini lines, the rows' summaries and the open detail share it.
// Readings arrive about hourly, so a slow poll is enough; the snapshot belongs to one person
// and access epoch and is dropped when either changes.
import { defineStore } from 'pinia'
import { computed, onScopeDispose } from 'vue'
import { onAccessRevalidate } from '../lib/authz'
import { onReset } from '../lib/position'
import { getQuotaOutlook, type QuotaOutlook } from '../lib/quotaOutlook'
import { initialRefreshStatus, usePolledData, usePoller } from '../lib/usePolledData'

export const OUTLOOK_POLL_MS = 120_000

export const useQuotaOutlook = defineStore('quotaOutlook', () => {
  const read = usePolledData<QuotaOutlook | null>(getQuotaOutlook, null)
  const poller = usePoller(() => read.refresh(), OUTLOOK_POLL_MS, { invalidate: read.invalidate })
  let users = 0
  /** A shown dial reads; the last one to go stops the poll. */
  function use() {
    if (users++ === 0) poller.start(true)
    return () => { if (--users === 0) poller.stop() }
  }
  // Loading until the first answer; a failed first read is an error. A later failure keeps the
  // last answer (its sampled time says how old the readings are).
  const state = computed<'loading' | 'ready' | 'error'>(() => read.data.value ? 'ready' : read.status.value.state === 'error' || read.status.value.state === 'forbidden' ? 'error' : 'loading')
  const groups = computed(() => new Map((read.data.value?.groups ?? []).map(g => [g.harness, g])))
  function reset() {
    read.invalidate()
    read.data.value = null
    read.status.value = initialRefreshStatus()
  }
  onScopeDispose(onReset(reset))
  onScopeDispose(onAccessRevalidate({ reset, refresh: () => { void read.refresh() } }))
  return { data: read.data, groups, state, refresh: read.refresh, use, reset }
})
