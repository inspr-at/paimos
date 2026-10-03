// SPDX-License-Identifier: AGPL-3.0-only
// Capacity for the Agents desk: observed windows with the server's pacing, the
// person's one schedule, and which computers are online. Sprint and Hold ride on
// pool schedule entries (the AEON-297 contract); those entries copy the person's
// schedule, so a schedule change is carried into them and a finished override is
// removed instead of leaving a stale copy behind. Keep for you (AEON-375) is the
// person's reserve on their own schedule, a pool may carry its own, and Away
// rides on the person's schedule until its date.
import { defineStore } from 'pinia'
import { computed, onScopeDispose } from 'vue'
import { listPairingComputers, PairingError, type PairingView } from '../lib/agentPairing'
import {
  activeOverride, buildPools, buildRows, clone, confirmsSave, uncertainFailure, defaultSchedule, listCapacity, listSchedules, putSchedule, sameShape, stripOverride, stripReserve, withReserve,
  type AccountCapacity, type AccountInput, type CapacitySchedule, type GaugePreference, type Override, type Pool, type ReserveMode, type ScheduleOverride,
} from '../lib/capacity'
import { usePreference } from '../lib/preferences'
import { buildComputerCards } from '../lib/computerAccounts'
import { initialRefreshStatus, usePolledData } from '../lib/usePolledData'
import { onReset } from '../lib/position'
import { onAccessChange } from '../lib/authz'
import { useAgents } from './agents'

const browserZone = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' } catch { return 'UTC' } }

export const useCapacity = defineStore('capacity', () => {
  const agents = useAgents()
  const capacityRead = usePolledData(listCapacity, [] as AccountCapacity[])
  const schedulesRead = usePolledData(listSchedules, [] as ScheduleOverride[])
  // Pairing access is optional. A failed read is not an empty inventory:
  // preserve the previous snapshot and report the failure to its consumers.
  const computersRead = usePolledData(() => listPairingComputers().catch(error => {
    if (error instanceof PairingError && error.status === 403) return [] as PairingView[]
    throw error
  }), [] as PairingView[])
  const computersState = computed(() => computersRead.status.value.state)
  const computersStale = computed(() => computersRead.stale.value)
  const gaugePref = usePreference<GaugePreference>('agents.capacity.gauge')
  const state = computed(() => capacityRead.status.value.state)
  const loaded = computed(() => capacityRead.status.value.updatedAt !== null)
  const schedulesLoaded = computed(() => schedulesRead.status.value.updatedAt !== null)
  const stale = computed(() => capacityRead.stale.value)

  async function load() {
    await Promise.all([capacityRead.refresh(), schedulesRead.refresh(), computersRead.refresh()])
  }
  const invalidate = () => { capacityRead.invalidate(); schedulesRead.invalidate(); computersRead.invalidate() }
  // Snapshots belong to one person/workspace and access epoch. Retaining them
  // across a transient failure is safe only until that ownership is reset.
  function reset() {
    invalidate()
    capacityRead.data.value = []
    schedulesRead.data.value = []
    computersRead.data.value = []
    for (const read of [capacityRead, schedulesRead, computersRead]) read.status.value = initialRefreshStatus()
  }
  onScopeDispose(onReset(reset))
  onScopeDispose(onAccessChange(change => { if (change === 'reset') reset() }))
  // Every write on /agents drops these reads too and reads them again (AEON-402).
  onScopeDispose(agents.onWrite({ invalidate, refresh: load }))

  const computerOf = computed(() => {
    const out = new Map<string, PairingView>()
    for (const computer of computersRead.data.value) {
      if (computer.computer_state === 'revoked') continue
      for (const e of computer.enrollments ?? []) if (e.state !== 'revoked') out.set(e.account_id, computer)
    }
    return out
  })
  const inputs = computed<AccountInput[]>(() => agents.accounts.map(a => {
    const computer = computerOf.value.get(a.id)
    return {
      id: a.id, label: a.label, harness: a.harness, host: a.host_label || computer?.computer_name || a.daemon_id, state: a.state, last_probe_ok: a.last_probe_ok, plan: a.plan,
      fingerprint: a.quota_pool_fingerprint, groupId: a.group_id, groupName: a.group_name,
      // The computer's setup flag is computer-wide; sign-ins are judged per account (probe_failure).
      connectivity: computer?.connectivity,
      disconnecting: computer?.enrollments.some(e => e.account_id === a.id && e.state === 'draining') ?? false,
    }
  }))
  const rows = computed(() => buildRows(inputs.value, capacityRead.data.value))
  const accountLines = computed(() => new Map(buildComputerCards({ computers: computersRead.data.value, rows: rows.value, now: agents.now }).flatMap(c => c.accounts.map(a => [a.id, a] as const))))
  /** The raw projection per account: windows, the Advanced limit, API-key spend. */
  const byAccount = computed(() => new Map(capacityRead.data.value.map(c => [c.account_id, c])))
  const pools = computed(() => buildPools(rows.value, agents.now))
  const ready = computed(() => ({ live: rows.value.filter(r => r.state === 'live').length, total: rows.value.length }))
  const signins = computed(() => rows.value.filter(r => r.state === 'signin'))

  // ---------- The person's schedule ----------
  const entries = computed(() => schedulesRead.data.value)
  const userEntry = computed(() => entries.value.find(e => e.scope === 'user' && e.schedule)?.schedule ?? null)
  const timezone = computed(() => {
    const zone = userEntry.value?.timezone ?? capacityRead.data.value[0]?.schedule.timezone
    return zone && zone !== 'UTC' ? zone : browserZone()
  })
  const schedule = computed<CapacitySchedule>(() => {
    const own = userEntry.value
    return own ? stripOverride(own) : defaultSchedule(timezone.value)
  })
  const poolEntry = (pool: string) => entries.value.find(e => e.scope === 'pool' && e.pool === pool)
  /** Away until this instant, while it is in force; empty otherwise. */
  const away = computed(() => (activeOverride(userEntry.value, agents.now) === 'away' ? userEntry.value?.override_until ?? '' : ''))
  /** The one-time plan card stays until the person has chosen a reserve once. */
  const reserveConfirmed = computed(() => !!userEntry.value?.reserve)
  const hasUserSchedule = computed(() => !!userEntry.value)
  /** Each pool's own Keep for you; '' follows the person's. */
  const poolReserves = computed(() => {
    const out: Record<string, { reserve: ReserveMode; percent?: number }> = {}
    for (const e of entries.value) if (e.scope === 'pool' && e.pool && e.schedule?.reserve) out[e.pool] = { reserve: e.schedule.reserve, percent: e.schedule.reserve_percent }
    return out
  })
  const keepAway = (body: CapacitySchedule): CapacitySchedule => (away.value ? { ...body, override: 'away', override_until: away.value } : body)

  function local(next: ScheduleOverride[]) { schedulesRead.invalidate(); schedulesRead.data.value = next }
  // Work days, nights and the editors change the shape; Keep for you and Away ride along.
  async function saveSchedule(next: CapacitySchedule) {
    await saveUser(keepAway(stripOverride(clone(next))))
  }
  // One request: the server saves the schedule and carries every entry that only
  // holds Sprint/Hold or its own reserve in the same transaction, so a save is all
  // or nothing. A failure propagates to the caller (the editor stays open, no
  // "Saved"), and the refresh puts the screen back to what the server has.
  async function saveUser(body: CapacitySchedule) {
    const before = entries.value
    local([...before.filter(e => e.scope !== 'user'), { scope: 'user', schedule: body }])
    try {
      await putSchedule({ scope: 'user', schedule: body, carry_overrides: true })
    } catch (e) {
      if (!uncertainFailure(e)) {
        local(before)
        throw new Error(`Nothing was saved: ${e instanceof Error ? e.message : 'the server did not accept the schedule'}.`)
      }
      // The answer was lost, not necessarily the save: ask the server what it has.
      let fresh: ScheduleOverride[]
      try { fresh = await listSchedules() } catch {
        local(before)
        throw new Error("Couldn't confirm the save: the connection dropped. Check the schedule, then try again.")
      }
      local(fresh)
      if (confirmsSave(fresh, body)) return
      throw new Error('Not saved: the connection dropped before the server took it. Try again.')
    } finally {
      await agents.afterWrite()
    }
  }
  async function setPreset(days: 5 | 6 | 7) {
    const next = clone(schedule.value)
    next.week = next.week.map((_, i) => ({ on: i < days, start: 8, end: 22 }))
    await saveSchedule(next)
  }
  async function setNights(on: boolean) {
    await saveSchedule({ ...clone(schedule.value), nights: on })
  }
  /** Sprint, Hold (optionally until a time) or back to the plan; a pool's own reserve stays. */
  async function setPoolOverride(pool: string, value: Override, until?: string) {
    const group = pool.startsWith('group:') ? pool.slice('group:'.length) : ''
    const entry = group
      ? entries.value.find(e => e.scope === 'group' && e.group_id === group)?.schedule ?? null
      : poolEntry(pool)?.schedule ?? null
    const carrier = !entry || sameShape(entry, schedule.value)
    const own = entry?.reserve ?? ''
    const shape = withReserve(stripReserve(carrier ? schedule.value : stripOverride(entry!)), own, entry?.reserve_percent)
    const write = (body: CapacitySchedule | null) => group
      ? putSchedule({ scope: 'group', group_id: group, schedule: body })
      : putSchedule({ scope: 'pool', pool: pool as Pool, schedule: body })
    try {
      if (!value && carrier && !own) await write(null)
      else await write({ ...clone(shape), override: value, ...(value === 'hold' && until ? { override_until: until } : {}) })
    } finally {
      await agents.afterWrite()
    }
  }
  /**
   * Keep for you: the person's reserve and Away in one save, then each pool
   * whose own reserve changed. A pool that follows the person again and carries
   * nothing else is removed.
   */
  async function saveKeep(draft: { reserve: ReserveMode; percent?: number; away: string; pools: Record<string, { reserve: ReserveMode; percent?: number }> }) {
    const body = withReserve(stripOverride(clone(schedule.value)), draft.reserve, draft.percent)
    await saveUser(draft.away ? { ...body, override: 'away', override_until: draft.away } : body)
    try {
      for (const [pool, want] of Object.entries(draft.pools)) {
        const entry = poolEntry(pool)?.schedule ?? null
        const percent = want.reserve === 'fixed' ? want.percent : undefined
        if ((entry?.reserve ?? '') === want.reserve && entry?.reserve_percent === percent) continue
        if (!entry) {
          if (want.reserve) await putSchedule({ scope: 'pool', pool: pool as Pool, schedule: withReserve(stripReserve(stripOverride(schedule.value)), want.reserve, percent) })
          continue
        }
        const override = activeOverride(entry, agents.now)
        if (!want.reserve && !override && sameShape(entry, schedule.value)) { await putSchedule({ scope: 'pool', pool: pool as Pool, schedule: null }); continue }
        const next = withReserve(override ? entry : stripOverride(entry), want.reserve, percent)
        await putSchedule({ scope: 'pool', pool: pool as Pool, schedule: next })
      }
    } finally {
      await agents.afterWrite()
    }
  }
  /** The plan card's answer: keep Auto, or turn the reserve off. */
  const confirmReserve = (mode: 'auto' | 'off') => saveUser(keepAway(withReserve(stripOverride(clone(schedule.value)), mode)))
  /** End Away now (the header chip's close). */
  const endAway = () => saveUser(stripOverride(clone(schedule.value)))

  const gauge = computed(() => gaugePref.value.value)
  const setGauge = (next: GaugePreference) => gaugePref.save(next, 0)

  return {
    state, loaded, stale, load, inputs, rows, accountLines, byAccount, computers: computersRead.data, computersLoaded: computed(() => computersRead.status.value.updatedAt !== null), computersState, computersStale, refreshCapacity: capacityRead.refresh, pools, ready, signins, schedule, timezone, saveSchedule, setPreset, setNights, setPoolOverride, gauge, setGauge,
    schedulesLoaded, away, reserveConfirmed, hasUserSchedule, poolReserves, saveKeep, confirmReserve, endAway,
    invalidate,
  }
})
