// SPDX-License-Identifier: AGPL-3.0-only
// Writes are immediate, serial and bound to the viewer who made the change.
// Polls may update counts, but never replace a newer edit with an older total.
import { computed, onScopeDispose, ref, watch } from 'vue'
import { api } from './api.ts'
import { basePlan, planValue, type PlanSnapshot, type WaitingWork, type WorkingPreference } from './agentsWorking.ts'
import { cloneDaily, sameDaily, type DailySettings } from './dailyLimits.ts'
import { queueRequest, type QueueWireSnapshot } from './workQueue.ts'
import { usePoller } from './usePolledData.ts'
/** After − or + is released, the final value is saved once this long passes without another press. */
export const SETTLE_MS = 300
export function useAgentPlan(viewer: () => string, harnessForProfile: (id: string) => string | undefined) {
  const snapshot = ref<PlanSnapshot | null>(null), plan = ref<WorkingPreference | null>(null)
  const readError = ref(''), saveError = ref(''), saving = ref(false), waiting = ref<WaitingWork[] | null>(null)
  const error = computed(() => saveError.value || readError.value)
  let generation = 0, version = 0, readVersion = 0
  let confirmed: WorkingPreference | null = null
  let confirmedAt: string | null = null
  let pending: { value: WorkingPreference; version: number; daily: boolean } | undefined
  let writer = false, reconciling = false
  // Steps made while − or + is held (and briefly after) only change the screen; the final value is saved once.
  let holds = 0, settle: ReturnType<typeof setTimeout> | undefined, leaving = false, disposed = false
  // A conflict ends the gesture it interrupts: its later steps are dropped until release, and a fresh press starts over.
  let interrupted = false
  /** Counts conflicts; − and + end an active hold whenever it changes. */
  const interrupts = ref(0)
  const current = (turn: number, who: string) => generation === turn && viewer() === who && !!who
  async function refresh(reconcile = false) {
    const turn = generation, who = viewer(), edit = version, read = ++readVersion
    if (!who) return
    try {
      const response = await api('/agents/plan')
      if (!response.ok) throw new Error('Couldn’t read the total. Please try again.')
      const answer = await response.json() as PlanSnapshot
      if (!current(turn, who) || read !== readVersion) return
      snapshot.value = answer
      readError.value = ''
      if ((reconcile || !writer && !pending) && edit === version) {
        confirmed = basePlan(answer); plan.value = basePlan(answer); confirmedAt = answer.updated_at
      }
    } catch (e) {
      if (current(turn, who) && read === readVersion) readError.value = e instanceof Error ? e.message : 'Couldn’t read the total.'
    }
  }
  async function refreshWaiting() {
    const turn = generation, who = viewer()
    if (!who) return
    try {
      const answer = await queueRequest<QueueWireSnapshot>('/queue')
      if (!current(turn, who)) return
      // This is the caller's visible queue, not a fabricated launcher queue.
      waiting.value = answer.items.map(item => ({ harness: harnessForProfile(item.queued.model_profile_id ?? '') || undefined, reason: item.queued.waiting ? item.queued.wait_reason : undefined }))
    } catch { if (current(turn, who)) waiting.value = null }
  }
  async function drain(turn: number, who: string) {
    if (writer || !pending || !current(turn, who)) return
    writer = true; saving.value = true
    // A hold that starts while a write is in flight waits for its own release.
    while (pending && !holds && !settle && current(turn, who)) {
      const job = pending; pending = undefined
      try {
        const response = await api('/preferences/agents.working', { method: 'PUT', keepalive: leaving, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value: job.value, expected_updated_at: confirmedAt }) })
        if (!current(turn, who)) return
        if (response.status === 409) {
          // A queued edit was made against the same stale plan. Drop it too;
          // the next deliberate click must start from the newly read revision.
          pending = undefined; version++
          if (holds) interrupted = true
          plan.value = confirmed && planValue(confirmed)
          saveError.value = 'The total or limits changed elsewhere. Review the latest values and try again.'
          if (disposed) break
          reconciling = true
          await refresh(true)
          if (!current(turn, who)) return
          reconciling = false
          interrupts.value++
          break
        }
        if (!response.ok) throw new Error(job.daily ? 'Couldn’t save the daily limit. Please try again.' : 'Couldn’t save the total and limits. Please try again.')
        const stored = await response.json() as { updated_at?: string }
        if (!current(turn, who)) return
        if (typeof stored.updated_at !== 'string' || !Number.isFinite(Date.parse(stored.updated_at))) throw new Error('Couldn’t confirm the save. Please try again.')
        confirmed = planValue(job.value)
        confirmedAt = stored.updated_at
        readVersion++ // Drop polls that started before this committed write.
        if (job.version === version) saveError.value = ''
      } catch (e) {
        if (!current(turn, who)) return
        if (job.version === version) {
          plan.value = confirmed && planValue(confirmed)
          saveError.value = e instanceof Error ? e.message : 'Couldn’t confirm the save. Please try again.'
        }
      }
    }
    if (!current(turn, who)) return
    writer = false; saving.value = false
    // Retain an unsuccessful write's feedback until the next deliberate change.
    if (!saveError.value && !disposed) void refresh()
  }
  function save(next: WorkingPreference, dailyEdit = false) {
    if (!plan.value || !snapshot.value || !viewer() || reconciling || interrupted) return
    if (next.total === plan.value.total && Object.keys(next.limits).length === Object.keys(plan.value.limits).length && Object.entries(next.limits).every(([key, limit]) => plan.value!.limits[key] === limit) && sameDaily(next.daily, plan.value.daily)) return
    plan.value = planValue(next); saveError.value = ''
    pending = { value: planValue(next), version: ++version, daily: dailyEdit }
    if (holds) return
    clearTimeout(settle); settle = undefined
    void drain(generation, viewer())
  }
  /** The daily settings now on screen: the unsaved or unconfirmed edit first, else what the server reads. */
  const daily = computed(() => plan.value?.daily ?? snapshot.value?.daily)
  /**
   * One harness's daily settings. The write carries every harness's current settings, because the
   * server replaces the map as a whole; the total and limits ride along unchanged.
   */
  function saveDaily(harness: string, settings: DailySettings) {
    if (!plan.value || !snapshot.value) return
    const next = cloneDaily(daily.value)
    next[harness] = { pace: { ...settings.pace }, boost_today: settings.boost_today && { ...settings.boost_today }, at_limit: settings.at_limit }
    if (sameDaily(next, daily.value)) return
    save({ ...planValue(plan.value), daily: next }, true)
  }
  function hold(active: boolean) {
    clearTimeout(settle); settle = undefined
    if (active) { holds++; return }
    holds = Math.max(0, holds - 1)
    if (holds) return
    interrupted = false
    const turn = generation, who = viewer()
    settle = setTimeout(() => { settle = undefined; void drain(turn, who) }, SETTLE_MS)
  }
  watch(viewer, who => {
    // A held or settling edit belongs to the viewer who made it and is dropped, never written for the next one.
    clearTimeout(settle); settle = undefined; holds = 0; interrupted = false
    generation++
    version = 0; readVersion++; writer = false; reconciling = false; pending = undefined; confirmed = null; confirmedAt = null
    snapshot.value = null; plan.value = null; waiting.value = null; readError.value = ''; saveError.value = ''; saving.value = false
    if (!who) return
    void refresh(); void refreshWaiting()
  }, { immediate: true, flush: 'sync' })
  const poller = usePoller(async () => { await Promise.all([refresh(), refreshWaiting()]) }, 15_000, { enabled: () => !!viewer(), invalidate: () => { readVersion++ } })
  poller.start(false)
  // Leaving the view or the page within the settle time still sends the released value.
  function flush() {
    if (!settle && !holds) return
    clearTimeout(settle); settle = undefined; holds = 0; interrupted = false
    void drain(generation, viewer())
  }
  function pageHide() { leaving = true; flush() }
  function pageShow() { leaving = false }
  if (typeof window !== 'undefined') { window.addEventListener('pagehide', pageHide); window.addEventListener('pageshow', pageShow) }
  onScopeDispose(() => {
    if (typeof window !== 'undefined') { window.removeEventListener('pagehide', pageHide); window.removeEventListener('pageshow', pageShow) }
    // The released value still follows a write in flight; only a changed viewer cancels it.
    disposed = true; poller.stop()
    flush()
  })
  return { snapshot, plan, daily, error, saving, waiting, interrupts, save, saveDaily, hold, refresh: () => refresh() }
}
