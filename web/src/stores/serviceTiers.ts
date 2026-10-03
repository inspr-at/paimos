// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { defineStore } from 'pinia'
import { useSession } from './session'
import { useAgents } from './agents'
import { can } from '../lib/authz'
import type { HarnessSession, ProcessOwnership } from '../lib/agents'
import { askServiceTier, changeServiceTier, decideServiceTier, readServiceTier } from '../lib/agentRows'
import { TIER_NAME, sameOwnership, tierCancelled, tierPrice, tierReport, tierUnavailable, undoTierAllowed, type ServiceTier, type TierChange, type TierRequest, type TierState } from '../lib/serviceTier'
import { toast } from '../lib/toast'

export const useServiceTiers = defineStore('service-tiers', () => {
  const auth = useSession(), agents = useAgents()
  const states = ref<Record<string, TierState>>({})
  const busy = ref<Record<string, boolean>>({})
  const errors = ref<Record<string, string>>({})
  const dismissed = ref<Record<string, string>>({})
  const uncertain = ref<Record<string, boolean>>({})
  const dialog = shallowRef<{ instance: string; session: HarnessSession; name: string; anchor: HTMLElement; ask: boolean } | null>(null)
  const undo = shallowRef<{ session: string; project: string; name: string; from: ServiceTier; to: ServiceTier; revision: number; control: string; price: string; ownership: ProcessOwnership; actor: string } | null>(null)
  const viewer = computed(() => auth.identity ? `${auth.identity.tenant.id}.${auth.identity.principal.id}` : '')
  const grant = computed(() => ({ person: auth.identity?.principal.kind === 'person', can }))
  let epoch = 0
  const reads = new Map<string, Promise<TierState | undefined>>(), timers = new Map<string, ReturnType<typeof setTimeout>>()
  const following = new Map<string, { session: HarnessSession; control: string; delay: number; deadline: number }>()
  let watchers = 0
  const visible = () => watchers > 0 && (typeof document === 'undefined' || document.visibilityState !== 'hidden')
  function stopTimers() { for (const timer of timers.values()) clearTimeout(timer); timers.clear() }
  watch(viewer, () => {
    epoch++; reads.clear(); states.value = {}; busy.value = {}; errors.value = {}; dismissed.value = {}; uncertain.value = {}; dialog.value = null; undo.value = null
    stopTimers(); following.clear()
  }, { flush: 'sync' })
  onScopeDispose(() => { stopTimers(); following.clear(); epoch++; if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', visibility) })
  function state(s: HarnessSession): TierState {
    const saved = states.value[s.id]
    if (saved && saved.revision >= (s.service_tier_revision ?? 0)) return saved
    return { session_id: s.id, revision: s.service_tier_revision ?? 0, active_tier: s.service_tier ?? null, pending: null, read_only: false, reports: s.service_tier_reports ?? [], requests: [] }
  }
  const report = (s: HarnessSession) => tierReport(s, state(s).reports)
  function rejection(s: HarnessSession) {
    const current = state(s), last = current.last_change
    return !busy.value[s.id] && !current.pending && last?.outcome === 'rejected' && !tierCancelled(last) && dismissed.value[s.id] !== last.id ? last : undefined
  }
  function dismissRejection(s: HarnessSession) {
    const last = rejection(s)
    if (!last || uncertain.value[s.id]) return
    dismissed.value[s.id] = last.id
    errors.value[s.id] = ''
  }
  const canDismissRejection = (s: HarnessSession) => !!rejection(s) && !uncertain.value[s.id]
  const unavailable = (s: HarnessSession) => uncertain.value[s.id] ? 'The previous tier outcome is unknown. Check its result before changing again.' : tierUnavailable(s, grant.value, Date.now(), state(s))
  const canAsk = (s: HarnessSession) => auth.identity?.principal.kind === 'agent' && auth.identity.principal.id === s.agent_principal_id &&
    s.management_mode === 'managed' && !s.stopped_at && !s.archived_at && !s.watch && s.phase !== 'stopped' && s.phase !== 'stopping' && s.advertised_capabilities.includes('service_tier_v1') && !state(s).read_only
  function adopt(s: HarnessSession, answer: TierState, cancelling = false) {
    if (answer.session_id !== s.id) throw new Error('The tier response belongs to another session.')
    const previous = states.value[s.id]
    if (previous && answer.revision < previous.revision) return
    states.value[s.id] = answer
    if (previous?.pending && previous.pending.id !== answer.pending?.id) {
      following.delete(s.id); clearTimeout(timers.get(s.id)); timers.delete(s.id)
      const last = answer.last_change
      const cancelled = tierCancelled(last) && !answer.pending && (
        last?.id === previous.pending.id ||
        (last?.reason === 'tier_cancelled_pending' && answer.revision === previous.revision + 1 && answer.active_tier === previous.active_tier)
      )
      if (answer.active_tier !== previous.pending.value && undo.value?.control === previous.pending.id) undo.value = null
      if (cancelled || cancelling) {
        errors.value[s.id] = ''
        if (!cancelling) toast(`Change to ${TIER_NAME[previous.pending.value]} was cancelled.`, { tone: 'info' })
      } else if (answer.active_tier !== previous.pending.value) {
        const reason = answer.last_change?.id === previous.pending.id ? answer.last_change.reason?.replaceAll('_', ' ') : null
        const message = `Change to ${TIER_NAME[previous.pending.value]} was not applied. ${reason || 'It was rejected, expired, or superseded.'}`
        errors.value[s.id] = message
        if (undo.value?.control === previous.pending.id) undo.value = null
        toast(message, { tone: 'error' })
      } else errors.value[s.id] = ''
    }
  }
  async function load(s: HarnessSession) {
    const current = reads.get(s.id)
    if (current) return current
    const generation = epoch
    const flight: Promise<TierState | undefined> = readServiceTier(s.project_id, s.id).then(answer => {
      if (generation !== epoch) return
      // Writes invalidate an older read. Its callers receive the newer flight
      // or accepted state instead of a spurious "session changed" error.
      if (reads.get(s.id) !== flight) return reads.get(s.id) ?? states.value[s.id]
      adopt(s, answer)
      uncertain.value[s.id] = false
      return states.value[s.id]
    }).finally(() => { if (reads.get(s.id) === flight) reads.delete(s.id) })
    reads.set(s.id, flight)
    return flight
  }
  function follow(s: HarnessSession) {
    const pending = states.value[s.id]?.pending
    if (!pending || !viewer.value) { following.delete(s.id); return }
    let progress = following.get(s.id)
    if (progress?.control !== pending.id) {
      progress = { session: s, control: pending.id, delay: 1200, deadline: Date.now() + 120_000 }
      following.set(s.id, progress)
    }
    if (!visible() || timers.has(s.id)) return
    if (Date.now() + progress.delay > progress.deadline) {
      errors.value[s.id] = 'Confirmation is still pending. Live updates or Check result will confirm it.'
      return
    }
    const delay = progress.delay
    progress.delay = Math.min(delay * 2, 30_000)
    const generation = epoch
    timers.set(s.id, setTimeout(async () => {
      timers.delete(s.id)
      if (generation !== epoch || !visible()) return
      try {
        await load(agents.sessionById(s.id) ?? s)
        if (generation !== epoch || !visible()) return
        if (states.value[s.id]?.pending) follow(s)
        else void agents.refreshSessions()
      } catch (error) {
        if (generation === epoch) errors.value[s.id] = `Confirmation unavailable. ${error instanceof Error ? error.message : 'Refresh and check the result.'}`
      }
    }, delay))
  }
  function visibility() {
    stopTimers()
    if (!visible()) return
    reconcile()
  }
  function reconcile() {
    if (!visible()) return
    const generation = epoch
    for (const progress of following.values()) {
      const s = agents.sessionById(progress.session.id) ?? progress.session
      void load(s).then(() => { if (generation === epoch && visible()) follow(s) }).catch(error => {
        if (generation === epoch) errors.value[s.id] = error instanceof Error ? error.message : 'Tier information unavailable.'
      })
    }
  }
  // The /agents view owns this lease. A global Pinia store must not keep
  // polling after navigation or in a hidden tab.
  function watchPage() {
    if (++watchers === 1) {
      if (typeof document !== 'undefined') document.addEventListener('visibilitychange', visibility)
      visibility()
    }
    let stopped = false
    return () => {
      if (stopped) return
      stopped = true
      if (--watchers) return
      stopTimers()
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', visibility)
      close(); undo.value = null
    }
  }
  // Session revisions arrive via the existing live stream/list refresh. Only
  // tier changes trigger reads; ordinary heartbeat ticks do not.
  watch(() => agents.sessions.map(s => [s.id, s.service_tier_revision] as const), rows => {
    if (!visible()) return
    const generation = epoch
    for (const [id, revision] of rows) {
      const saved = states.value[id], s = agents.sessionById(id)
      if (saved && s && (revision ?? 0) > saved.revision) {
        void load(s).then(() => { if (generation === epoch && visible()) follow(s) }).catch(error => { if (generation === epoch) errors.value[id] = error instanceof Error ? error.message : 'Tier information unavailable.' })
      }
    }
  })
  function open(s: HarnessSession, name: string, anchor: HTMLElement) {
    if (dialog.value?.anchor === anchor) { close(true); return }
    if (busy.value[s.id] || state(s).pending || (unavailable(s) && !canAsk(s))) return
    dialog.value = { instance: crypto.randomUUID(), session: s, name, anchor, ask: canAsk(s) }
  }
  function close(restore = false) { const anchor = dialog.value?.anchor; dialog.value = null; if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true }) }
  function changeBody(s: HarnessSession, tier: ServiceTier, snapshot: TierState): TierChange {
    if (!s.process_ownership) throw new Error('Process ownership is not confirmed.')
    return { request_id: crypto.randomUUID(), tier, expected_revision: snapshot.revision, expected_ownership: { ...s.process_ownership } }
  }
  async function change(s: HarnessSession, name: string, to: ServiceTier, options: { request?: TierRequest; decision?: 'approve' | 'decline'; withUndo?: boolean; undoOf?: string; snapshot?: TierState } = {}) {
    if (busy.value[s.id] || uncertain.value[s.id]) return false
    const reason = unavailable(s)
    if (reason && options.decision !== 'decline') { toast(reason, { tone: 'error' }); return false }
    if (!grant.value.person || !can('harness.control', s.project_id)) return false
    const before = options.snapshot ?? state(s), generation = epoch, actor = viewer.value
    const body = { ...changeBody(s, to, before), ...(options.undoOf ? { undo_of_control_id: options.undoOf } : {}) }
    // Starting the next change retires the previous rejection for this viewer.
    if (before.last_change?.outcome === 'rejected') dismissed.value[s.id] = before.last_change.id
    busy.value[s.id] = true; errors.value[s.id] = ''
    reads.delete(s.id)
    try {
      const answer = options.request
        ? await decideServiceTier(s.project_id, s.id, options.request.id, { ...body, decision: options.decision ?? 'approve' })
        : await changeServiceTier(s.project_id, s.id, body)
      if (generation !== epoch) return false
      adopt(s, answer, !!before.pending && to === before.active_tier && !answer.pending)
      if (options.withUndo && before.active_tier && answer.pending?.id === body.request_id) {
        undo.value = { session: s.id, project: s.project_id, name, from: before.active_tier, to, revision: answer.revision, control: answer.pending.id, price: tierPrice(before.reports.find(r => r.harness === s.harness && r.model === s.model)?.tiers.find(t => t.tier === to)), ownership: body.expected_ownership, actor }
      }
      follow(s)
      void agents.refreshSessions()
      return true
    } catch (error) {
      if (generation !== epoch) return false
      uncertain.value[s.id] = true
      errors.value[s.id] = error instanceof Error ? error.message : 'Tier request failed.'
      toast(`Tier outcome unconfirmed: ${errors.value[s.id]}`, { tone: 'error' })
      // A lost write response may still have queued a control. Re-read before
      // allowing another request; never retry a mutation with a new id here.
      try { await load(s); if (generation === epoch) follow(s) } catch { /* Keep the visible error. */ }
      return false
    } finally { if (generation === epoch) busy.value[s.id] = false }
  }
  async function ask(s: HarnessSession, tier: ServiceTier, reason: string) {
    if (!canAsk(s) || busy.value[s.id] || !reason.trim() || [...reason.trim()].length > 500) return false
    const generation = epoch
    busy.value[s.id] = true
    try {
      const answer = await askServiceTier(s.project_id, s.id, { request_id: crypto.randomUUID(), tier, reason: reason.trim() })
      if (generation !== epoch || answer.session_id !== s.id) return false
      const current = state(s)
      states.value[s.id] = { ...current, requests: [answer, ...current.requests.filter(q => q.id !== answer.id)] }
      try { await load(s) } catch { /* Request accepted; the history can be refreshed on the next read. */ }
      return true
    } catch (error) { if (generation === epoch) errors.value[s.id] = error instanceof Error ? error.message : 'Request failed.'; return false }
    finally { if (generation === epoch) busy.value[s.id] = false }
  }
  async function undoChange() {
    const receipt = undo.value
    if (!receipt || receipt.actor !== viewer.value || busy.value[receipt.session]) return
    const s = agents.sessionById(receipt.session)
    if (!s || s.project_id !== receipt.project || !sameOwnership(s.process_ownership, receipt.ownership) || unavailable(s)) { undo.value = null; toast('The session changed. Undo is no longer available.', { tone: 'error' }); return }
    const generation = epoch
    try {
      const current = await load(s)
      if (generation !== epoch || undo.value !== receipt) return
      if (!current || !undoTierAllowed(current, receipt)) throw new Error(current?.pending?.state === 'claimed' ? 'The daemon is applying this change. Check its confirmation before undoing.' : 'The tier changed again. Undo is no longer available.')
      if (await change(s, receipt.name, receipt.from, { snapshot: current, undoOf: receipt.control })) undo.value = null
    } catch (error) { toast(error instanceof Error ? error.message : 'Undo failed.', { tone: 'error' }) }
  }
  return { states, busy, errors, dialog, undo, state, report, rejection, dismissRejection, canDismissRejection, unavailable, canAsk, load, follow, watchPage, reconcile, open, close, change, ask, undoChange }
})
