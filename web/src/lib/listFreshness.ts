// SPDX-License-Identifier: AGPL-3.0-only
import { STREAM_SILENCE_MS } from './streamHealth.ts'

export const STALE_LIST_TIP = 'Showing the last successful update. Reconnecting and refreshing; workers and ETAs may have changed. Past estimates are not current overdue work.'

export function listFreshness(updatedAt: number | null, untrusted: boolean, now: number) {
  const age = updatedAt === null ? 0 : Math.max(0, now - updatedAt)
  const state = !untrusted && updatedAt !== null ? 'live' : updatedAt !== null && age >= STREAM_SILENCE_MS ? 'stale' : 'reconnecting'
  return {
    state,
    text: state === 'live' ? 'Live' : state === 'stale' ? `Updated ${Math.floor(age / 60_000)} min ago` : 'Reconnecting…',
    tip: state === 'live' ? 'Connected to live updates; workers and ETAs are current.' : STALE_LIST_TIP,
  }
}
