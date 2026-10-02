// SPDX-License-Identifier: AGPL-3.0-only
// Writes are immediate, serial and bound to the viewer who made the change.
// Polls may update counts, but never replace a newer edit with an older total.
import { onScopeDispose, ref, watch } from 'vue'
import { api } from './api.ts'
import { planValue, type PlanSnapshot, type WaitingWork, type WorkingPreference } from './agentsWorking.ts'
import { queueRequest, type QueueWireSnapshot } from './workQueue.ts'
import { usePoller } from './usePolledData.ts'
export const FOLD_KEY = 'agents.working.display'
export function useAgentPlan(viewer: () => string, harnessForProfile: (id: string) => string | undefined) {
  const snapshot = ref<PlanSnapshot | null>(null), plan = ref<WorkingPreference | null>(null)
  const folded = ref(false), error = ref(''), saving = ref(false), waiting = ref<WaitingWork[] | null>(null)
  let generation = 0, version = 0, readVersion = 0, foldVersion = 0
  let confirmed: WorkingPreference | null = null
  let pending: { value: WorkingPreference; version: number } | undefined
  let writer = false, foldTail = Promise.resolve()
  const current = (turn: number, who: string) => generation === turn && viewer() === who && !!who
  async function refresh() {
    const turn = generation, who = viewer(), edit = version, read = ++readVersion
    if (!who) return
    try {
      const response = await api('/agents/plan')
      if (!response.ok) throw new Error('Couldn’t read the total. Please try again.')
      const answer = await response.json() as PlanSnapshot
      if (!current(turn, who) || read !== readVersion) return
      snapshot.value = answer
      if (!writer && !pending && edit === version) {
        confirmed = planValue(answer); plan.value = planValue(answer); error.value = ''
      }
    } catch (e) {
      if (current(turn, who) && read === readVersion) error.value = e instanceof Error ? e.message : 'Couldn’t read the total.'
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
    if (writer || !current(turn, who)) return
    writer = true; saving.value = true
    while (pending && current(turn, who)) {
      const job = pending; pending = undefined
      try {
        const response = await api('/preferences/agents.working', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value: job.value }) })
        if (!current(turn, who)) return
        if (!response.ok) throw new Error('Couldn’t save the total and limits. Please try again.')
        confirmed = planValue(job.value)
        if (job.version === version) error.value = ''
      } catch (e) {
        if (!current(turn, who)) return
        if (job.version === version) {
          plan.value = confirmed && planValue(confirmed)
          error.value = e instanceof Error ? e.message : 'Couldn’t confirm the save. Please try again.'
        }
      }
    }
    if (!current(turn, who)) return
    writer = false; saving.value = false
    // Retain an unsuccessful write's feedback until the next deliberate change.
    if (!error.value) void refresh()
  }
  function save(next: WorkingPreference) {
    if (!plan.value || !snapshot.value || !viewer()) return
    plan.value = planValue(next); error.value = ''
    pending = { value: planValue(next), version: ++version }
    void drain(generation, viewer())
  }
  function toggleFold() {
    const turn = generation, who = viewer(), next = !folded.value
    folded.value = next; foldVersion++
    foldTail = foldTail.catch(() => {}).then(async () => {
      if (!current(turn, who)) return
      try {
        const response = await api(`/preferences/${FOLD_KEY}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value: { folded: next } }) })
        if (!response.ok && current(turn, who)) error.value = 'Couldn’t remember the detail view. Please try again.'
      } catch { if (current(turn, who)) error.value = 'Couldn’t remember the detail view. Please try again.' }
    })
  }
  watch(viewer, async who => {
    const turn = ++generation
    version = 0; foldVersion = 0; readVersion++; writer = false; pending = undefined; confirmed = null
    snapshot.value = null; plan.value = null; waiting.value = null; folded.value = false; error.value = ''; saving.value = false
    if (!who) return
    void refresh(); void refreshWaiting()
    try {
      const response = await api(`/preferences/${FOLD_KEY}`)
      const answer = response.ok ? await response.json() : null
      if (current(turn, who) && foldVersion === 0) folded.value = answer?.value?.folded === true
    } catch { /* Detail-view memory is optional; the total is never inferred. */ }
  }, { immediate: true, flush: 'sync' })
  const poller = usePoller(async () => { await Promise.all([refresh(), refreshWaiting()]) }, 15_000, { enabled: () => !!viewer(), invalidate: () => { readVersion++ } })
  poller.start(false)
  onScopeDispose(() => { generation++; pending = undefined; poller.stop() })
  return { snapshot, plan, folded, error, saving, waiting, save, refresh, toggleFold }
}
