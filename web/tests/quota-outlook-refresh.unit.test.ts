// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497: a refresh that fails after a good read keeps the snapshot. The age moves with the
// client clock from sampled_at and read_at; the server's counters stay frozen. Try again stays
// on the muted line, so the chart and its steppers do not move.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import { APIError } from '../src/lib/api'
import { accountRows, getQuotaOutlook, usageView, type QuotaOutlook } from '../src/lib/quotaOutlook'
import { useQuotaOutlook } from '../src/stores/quotaOutlook'
import QuotaUsage from '../src/components/agents/QuotaUsage.vue'

vi.mock('../src/lib/quotaOutlook', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/quotaOutlook')>(),
  getQuotaOutlook: vi.fn(),
}))

const T = Date.parse('2026-10-18T11:10:00Z')
const SAMPLED = '2026-10-18T10:10:00.000Z'

function payload(): QuotaOutlook {
  return {
    generated_at: SAMPLED, anchor_hour: '2026-10-18T00:00:00.000Z', history_hours: 169, horizon_hours: 168, truncated: false,
    groups: [{
      harness: 'claude', state: 'measured',
      accounts: [{
        account_id: 'a', label: 'account-a@', state: 'ready', pooled: true, used_percent: 40, limit_percent: 95,
        resets_at: '2026-10-20T10:10:00.000Z', read_at: SAMPLED, reading_age_seconds: 3600, freshness: 'fresh', forecast: null,
      }],
      pool: { accounts: 1, used_percent: 40, left_percent: 60, sampled_at: SAMPLED, sampled_age_seconds: 3600 },
      pace: { per_hour_24h: null, per_hour_6h: null, safe_per_hour: null, measured_hours: 0 },
      verdict: 'unknown',
      headline: { kind: 'unknown', at: null, until: null, account_id: '', unused_percent: null, focus_at: null },
      history: [], past_line: [], past_refills: [], current: null, safe: null, projected: [],
    }],
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.useFakeTimers()
  vi.setSystemTime(T)
  vi.mocked(getQuotaOutlook).mockReset()
})
afterEach(() => { vi.useRealTimers() })

it('a failed refresh keeps the snapshot and ages it from the reading', async () => {
  const store = useQuotaOutlook()
  vi.mocked(getQuotaOutlook).mockResolvedValue(payload())
  await store.refresh()
  const fresh = usageView('claude', store.groups.get('claude'), store.data!.anchor_hour, 'UTC', [], 'ready', false, store.now)
  expect(fresh.muted).toContain('sampled 10:10')
  expect(fresh.muted).not.toContain('ago')
  expect(store.failed).toBe(false)

  vi.mocked(getQuotaOutlook).mockRejectedValue(new APIError(503, 'unavailable', {}))
  vi.setSystemTime(T + 2 * 3_600_000)
  await store.refresh()
  expect(store.failed).toBe(true)
  expect(store.data).not.toBeNull()
  expect(store.data!.groups[0]!.pool!.sampled_age_seconds).toBe(3600)
  const aged = usageView('claude', store.groups.get('claude'), store.data!.anchor_hour, 'UTC', [], 'ready', false, store.now)
  expect(aged.muted).toContain('3 h ago')
  expect(aged.muted).not.toContain('10:10')
  expect(aged.stale).toBe(true)
  expect(accountRows(store.groups.get('claude')!, 'UTC', [], store.now)[0]!.stateWord).toBe('3 h ago')

  const app = Vue.createSSRApp({
    render: () => Vue.h(QuotaUsage, { view: aged, state: 'ready', failed: true, now: store.now, tz: 'UTC' }),
  })
  app.component('RouterLink', { props: ['to'], setup: (_, { slots }) => () => slots.default?.() })
  expect(await renderToString(app)).toContain('Try again')
})
