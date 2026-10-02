// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { defineStore } from 'pinia'
import { useSession } from './session'
import { useAgents } from './agents'
import { can } from '../lib/authz'
import type { HarnessSession, ProcessOwnership } from '../lib/agents'
import { askServiceTier, changeServiceTier, decideServiceTier, readServiceTier } from '../lib/agentRows'
import { sameOwnership, tierPrice, tierReport, tierUnavailable, undoTierAllowed, type ServiceTier, type TierChange, type TierRequest, type TierState } from '../lib/serviceTier'
import { toast } from '../lib/toast'

export const useServiceTiers = defineStore('service-tiers', () => {
  const auth = useSession(), agents = useAgents()
  const states = ref<Record<string, TierState>>({})
  const busy = ref<Record<string, boolean>>({})
  const errors = ref<Record<string, string>>({})
  const uncertain = ref<Record<string, boolean>>({})
  const dialog = shallowRef<{ instance: string; session: HarnessSession; name: string; anchor: HTMLElement; ask: boolean } | null>(null)
  const undo = shallowRef<{ session: string; project: string; name: string; from: ServiceTier; to: ServiceTier; revision: number; control: string; price: string; ownership: ProcessOwnership; actor: string } | null>(null)
  const viewer = computed(() => auth.identity ? `${auth.identity.tenant.id}.${auth.identity.principal.id}` : '')
  const grant = computed(() => ({ person: auth.identity?.principal.kind === 'person', can }))
  let epoch = 0
  const reads = new Map<string, number>(), timers = new Map<string, ReturnType<typeof setTimeout>>()
  watch(viewer, () => {
    epoch++; reads.clear(); states.value = {}; busy.value = {}; errors.value = {}; uncertain.value = {}; dialog.value = null; undo.value = null
    for (const timer of timers.values()) clearTimeout(timer)
    timers.clear()
  }, { flush: 'sync' })
  onScopeDispose(() => { for (const timer of timers.values()) clearTimeout(timer); timers.clear(); epoch++ })
  function state(s: HarnessSession): TierState {
    const saved = states.value[s.id]
    if (saved && saved.revision >= (s.service_tier_revision ?? 0)) return saved
    return { session_id: s.id, revision: s.service_tier_revision ?? 0, active_tier: s.service_tier ?? null, pending: null, read_only: false, reports: s.service_tier_reports ?? [], requests: [] }
  }
  const report = (s: HarnessSession) => tierReport(s, state(s).reports)
  const unavailable = (s: HarnessSession) => uncertain.value[s.id] ? 'The previous tier outcome is unknown. Check its result before changing again.' : tierUnavailable(s, grant.value, Date.now(), state(s))
  const canAsk = (s: HarnessSession) => auth.identity?.principal.kind === 'agent' && auth.identity.principal.id === s.agent_principal_id &&
    s.management_mode === 'managed' && !s.stopped_at && !s.archived_at && !s.watch && s.phase !== 'stopped' && s.phase !== 'stopping' && s.advertised_capabilities.includes('service_tier_v1') && !state(s).read_only
  function adopt(s: HarnessSession, answer: TierState) {
    if (answer.session_id !== s.id) throw new Error('The tier response belongs to another session.')
    if (!states.value[s.id] || answer.revision >= states.value[s.id]!.revision) states.value[s.id] = answer
  }
  async function load(s: HarnessSession) {
    const generation = epoch, read = (reads.get(s.id) ?? 0) + 1
    reads.set(s.id, read)
    const answer = await readServiceTier(s.project_id, s.id)
    if (generation !== epoch || reads.get(s.id) !== read) return
    adopt(s, answer)
    uncertain.value[s.id] = false
    return answer
  }
  function follow(s: HarnessSession) {
    clearTimeout(timers.get(s.id)); timers.delete(s.id)
    if (!state(s).pending || !viewer.value) return
    const generation = epoch
    timers.set(s.id, setTimeout(async () => {
      timers.delete(s.id)
      if (generation !== epoch) return
      try {
        await load(s)
        if (generation !== epoch) return
        if (state(s).pending) follow(s)
        else void agents.refreshSessions()
      } catch (error) {
        if (generation === epoch) errors.value[s.id] = `Confirmation unavailable. ${error instanceof Error ? error.message : 'Refresh and check the result.'}`
      }
    }, 1200))
  }
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
  async function change(s: HarnessSession, name: string, to: ServiceTier, options: { request?: TierRequest; decision?: 'approve' | 'decline'; withUndo?: boolean; snapshot?: TierState } = {}) {
    if (busy.value[s.id] || uncertain.value[s.id]) return false
    const reason = unavailable(s)
    if (reason && options.decision !== 'decline') { toast(reason, { tone: 'error' }); return false }
    if (!grant.value.person || !can('harness.control', s.project_id)) return false
    const before = options.snapshot ?? state(s), generation = epoch, actor = viewer.value
    const body = changeBody(s, to, before)
    busy.value[s.id] = true; errors.value[s.id] = ''
    reads.set(s.id, (reads.get(s.id) ?? 0) + 1)
    try {
      const answer = options.request
        ? await decideServiceTier(s.project_id, s.id, options.request.id, { ...body, decision: options.decision ?? 'approve' })
        : await changeServiceTier(s.project_id, s.id, body)
      if (generation !== epoch) return false
      adopt(s, answer)
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
      if (await change(s, receipt.name, receipt.from, { snapshot: current })) undo.value = null
    } catch (error) { toast(error instanceof Error ? error.message : 'Undo failed.', { tone: 'error' }) }
  }
  return { states, busy, errors, dialog, undo, state, report, unavailable, canAsk, load, follow, open, close, change, ask, undoChange }
})
