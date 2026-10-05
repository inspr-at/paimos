// SPDX-License-Identifier: AGPL-3.0-only
import { computed, ref, watch, type Ref } from 'vue'
import { APIError } from './api'
import { nextPick, orderedTickets, selectionOf, ticketGroups, walkOrder, type TicketGroup, type Walker } from './journey'
import type { JourneyData, PlanOwner } from './useJourneyData'
import { toast } from './toast'

// The release's selection: which tickets are in it, grouped by feature, with the
// tri-state feature boxes and their memory of the last partial pick. Every change
// is one plan write (the whole order and the included set) on the walker's
// revision; writes run one after another and a refused one restores the list.
export function usePlan(data: JourneyData, editable: Ref<boolean>, afterSave: (owner: PlanOwner) => void = () => {}) {
  const walker = computed(() => data.walker.value.value)
  const groups = computed<TicketGroup[]>(() => walker.value ? ticketGroups(walker.value, data.epicOf) : [])
  const order = computed(() => walkOrder(groups.value))
  const included = computed(() => new Set((walker.value?.tickets ?? []).filter(t => t.included).map(t => t.ticket_node_id)))
  // The last partial pick per feature, so a third click restores it.
  const memory = ref(new Map<string, string[]>())
  const saving = ref(false)
  let chain: Promise<unknown> = Promise.resolve()
  watch(data.planOwner, (owner, previous) => {
    // A refresh fences pending writes, but the partial pick belongs to the
    // project/release and survives new request generations for that record.
    if (owner?.projectId !== previous?.projectId || owner?.releaseId !== previous?.releaseId) memory.value = new Map()
    saving.value = false; chain = Promise.resolve()
  }, { flush: 'sync' })
  watch([groups, data.walker.status], () => {
    if (!data.capturePlanOwner()) return
    const copy = new Map<string, string[]>()
    for (const group of groups.value) {
      if (!group.feature) continue
      const ids = new Set(group.tickets.map(t => t.ticket_node_id))
      const remembered = memory.value.get(group.id)?.filter(id => ids.has(id))
      if (remembered?.length) copy.set(group.id, remembered)
    }
    memory.value = copy
  }, { flush: 'sync' })

  function remember(next: Set<string>) {
    const copy = new Map(memory.value)
    for (const group of groups.value) {
      if (group.feature && selectionOf(group.tickets, next) === 'some') copy.set(group.id, group.tickets.filter(t => next.has(t.ticket_node_id)).map(t => t.ticket_node_id))
    }
    memory.value = copy
  }
  function apply(next: Set<string>) {
    const current = walker.value
    const owner = data.capturePlanOwner()
    if (!current || !owner || !editable.value) return Promise.resolve(false)
    const before = current
    // Show it at once; the server's answer replaces it.
    data.patchWalker({ ...current, tickets: current.tickets.map(t => ({ ...t, included: next.has(t.ticket_node_id) })) } as Walker, owner)
    remember(next)
    const run = async () => {
      if (!data.isPlanOwnerCurrent(owner)) return false
      if (!editable.value) { data.patchWalker(before, owner); return false }
      saving.value = true
      try {
        const latest = data.walker.value.value!
        const saved = await data.savePlan(owner, orderedTickets(latest).map(t => t.ticket_node_id), orderedTickets(latest).filter(t => next.has(t.ticket_node_id)).map(t => t.ticket_node_id))
        if (!saved || !data.isPlanOwnerCurrent(owner)) return false
        // A plan write moves the journey's revision (and may change the next action).
        afterSave(owner)
        return true
      } catch (e) {
        if (!data.isPlanOwnerCurrent(owner)) return false
        data.patchWalker(before, owner)
        const stale = e instanceof APIError && e.status === 409
        toast(stale ? `The plan changed elsewhere (${e.message}). The newest plan is shown.` : `The plan was not saved: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
        if (stale) void data.loadWalker(owner.releaseId, true)
        return false
      } finally { if (data.isPlanOwnerCurrent(owner)) saving.value = false }
    }
    const result = chain.then(run, run)
    chain = result
    return result
  }
  function toggleTicket(id: string) {
    const next = new Set(included.value)
    if (next.has(id)) next.delete(id); else next.add(id)
    return apply(next)
  }
  function toggleFeature(groupId: string) {
    const group = groups.value.find(g => g.id === groupId)
    if (!group) return Promise.resolve(false)
    // The partial pick about to be replaced is the one a later click restores.
    remember(included.value)
    return apply(nextPick(group.tickets, included.value, memory.value.get(groupId)))
  }
  const selection = (group: TicketGroup) => selectionOf(group.tickets, included.value)
  const stats = computed(() => {
    const tickets = walker.value?.tickets ?? []
    const inRelease = tickets.filter(t => included.value.has(t.ticket_node_id))
    const estimated = inRelease.filter(t => t.estimated_hours != null)
    return {
      total: tickets.length, inRelease: inRelease.length, backlog: tickets.length - inRelease.length,
      hours: estimated.reduce((sum, t) => sum + (t.estimated_hours ?? 0), 0), unestimated: inRelease.length - estimated.length,
      emptyFeatures: groups.value.filter(g => g.feature && !g.feature.derived && !g.tickets.some(t => included.value.has(t.ticket_node_id))),
    }
  })
  return { walker, groups, order, included, memory, saving, toggleTicket, toggleFeature, selection, stats }
}
export type Plan = ReturnType<typeof usePlan>
