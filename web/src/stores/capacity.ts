// SPDX-License-Identifier: AGPL-3.0-only
// Capacity for the Agents desk: observed windows with the server's pacing, the
// person's one schedule, and which computers are online. Sprint and Hold ride on
// pool schedule entries (the AEON-297 contract); those entries copy the person's
// schedule, so a schedule change is carried into them and a finished override is
// removed instead of leaving a stale copy behind.
import { defineStore } from 'pinia'
import { computed } from 'vue'
import { listPairingComputers, type PairingView } from '../lib/agentPairing'
import {
  activeOverride, buildPools, buildRows, clone, defaultSchedule, listCapacity, listSchedules, putSchedule, sameShape, stripOverride,
  type AccountCapacity, type AccountInput, type CapacitySchedule, type GaugePreference, type Override, type Pool, type ScheduleOverride,
} from '../lib/capacity'
import { usePreference } from '../lib/preferences'
import { usePolledData } from '../lib/usePolledData'
import { useAgents } from './agents'

const browserZone = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' } catch { return 'UTC' } }

export const useCapacity = defineStore('capacity', () => {
  const agents = useAgents()
  const capacityRead = usePolledData(listCapacity, [] as AccountCapacity[])
  const schedulesRead = usePolledData(listSchedules, [] as ScheduleOverride[])
  // Connectivity is optional detail: people without pairing access still see capacity.
  const computersRead = usePolledData(() => listPairingComputers().catch(() => [] as PairingView[]), [] as PairingView[])
  const gaugePref = usePreference<GaugePreference>('agents.capacity.gauge')
  const state = computed(() => capacityRead.status.value.state)
  const loaded = computed(() => capacityRead.status.value.updatedAt !== null)
  const stale = computed(() => capacityRead.stale.value)

  async function load() {
    await Promise.all([capacityRead.refresh(), schedulesRead.refresh(), computersRead.refresh()])
  }

  const computerOf = computed(() => {
    const out = new Map<string, PairingView>()
    for (const computer of computersRead.data.value) for (const e of computer.enrollments ?? []) out.set(e.account_id, computer)
    return out
  })
  const inputs = computed<AccountInput[]>(() => agents.accounts.map(a => {
    const computer = computerOf.value.get(a.id)
    return {
      id: a.id, label: a.label, harness: a.harness, host: a.host_label || computer?.computer_name || a.daemon_id, state: a.state, last_probe_ok: a.last_probe_ok, plan: a.plan,
      connectivity: computer?.connectivity, loginRequired: computer?.setup_state === 'login_required',
    }
  }))
  const rows = computed(() => buildRows(inputs.value, capacityRead.data.value))
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

  function local(next: ScheduleOverride[]) { schedulesRead.invalidate(); schedulesRead.data.value = next }
  async function saveSchedule(next: CapacitySchedule) {
    const previous = schedule.value
    const body = stripOverride(clone(next))
    local([...entries.value.filter(e => e.scope !== 'user'), { scope: 'user', schedule: body }])
    try {
      await putSchedule({ scope: 'user', schedule: body })
      // Pool and account entries that only carry Sprint/Hold follow the new schedule.
      for (const e of entries.value) {
        if (e.scope === 'user' || !e.schedule || !sameShape(e.schedule, previous)) continue
        const keep = activeOverride(e.schedule, Date.now())
        const target = e.scope === 'pool' ? { scope: e.scope, pool: e.pool } : { scope: e.scope, account_id: e.account_id }
        await putSchedule({ ...target, schedule: keep ? { ...body, override: keep } : null }).catch(() => undefined)
      }
    } finally {
      await Promise.all([schedulesRead.refresh(), capacityRead.refresh()])
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
  async function setPoolOverride(pool: string, value: Override) {
    const entry = poolEntry(pool)
    const carrier = !entry?.schedule || sameShape(entry.schedule, schedule.value)
    const shape = carrier ? schedule.value : stripOverride(entry!.schedule!)
    try {
      if (!value && carrier) await putSchedule({ scope: 'pool', pool: pool as Pool, schedule: null })
      else await putSchedule({ scope: 'pool', pool: pool as Pool, schedule: { ...clone(shape), override: value } })
    } finally {
      await Promise.all([schedulesRead.refresh(), capacityRead.refresh()])
    }
  }

  const gauge = computed(() => gaugePref.value.value)
  const setGauge = (next: GaugePreference) => gaugePref.save(next, 0)

  return {
    state, loaded, stale, load, inputs, rows, pools, ready, signins, schedule, timezone, saveSchedule, setPreset, setNights, setPoolOverride, gauge, setGauge,
    invalidate: () => { capacityRead.invalidate(); schedulesRead.invalidate(); computersRead.invalidate() },
  }
})
