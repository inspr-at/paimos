// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { onScopeDispose, reactive, ref } from 'vue'
import { onReset } from '../lib/position'
import { onAccessChange } from '../lib/authz'
import { getNode } from '../lib/api'
import { rowStore } from '../lib/rowStore'
import { addToQueue, movedQueue, readQueue, removeFromQueue, moveQueue, resetQueue, type QueueSnapshot, type QueueTarget } from '../lib/workQueue'

export const useWorkQueue = defineStore('workQueue', () => {
  // Read projections only; order and queue membership always come from the run service.
  const snapshots = reactive<Record<string, QueueSnapshot>>({})
  const errors = reactive<Record<string, string>>({})
  const pending = new Map<string, Promise<void>>()
  const busy = ref(false)
  let epoch = 0
  const turns = new Map<string, number>()
  const clear = () => { epoch++; for (const key of Object.keys(snapshots)) delete snapshots[key]; for (const key of Object.keys(errors)) delete errors[key]; pending.clear(); turns.clear(); busy.value = false }
  onScopeDispose(onReset(clear))
  onScopeDispose(onAccessChange(change => {
    const projects = Object.keys(snapshots)
    clear()
    if (change === 'refresh') for (const project of projects) void load(project, true)
  }))
  function load(project: string, force = false): Promise<void> {
    if (!project) return Promise.resolve()
    if (!force && pending.has(project)) return pending.get(project)!
    const started = epoch, turn = (turns.get(project) ?? 0) + 1
    turns.set(project, turn)
    const request = (async () => {
      try {
        const result = await readQueue(project)
        if (started !== epoch || turn !== turns.get(project)) return
        snapshots[project] = result; delete errors[project]
      } catch (e) {
        if (started !== epoch || turn !== turns.get(project)) return
        errors[project] = e instanceof Error ? e.message : 'The work queue could not be loaded.'
      }
    })().finally(() => { if (pending.get(project) === request) pending.delete(project) })
    pending.set(project, request); return request
  }
  function entry(project: string, ticket: string) { return snapshots[project]?.items.find(item => item.ticket_id === ticket) ?? null }
  async function write(project: string, action: () => Promise<unknown>) {
    if (busy.value) throw new Error('A queue change is already being saved.')
    const started = epoch
    busy.value = true
    try {
      await action()
      if (started === epoch) {
        await load(project, true)
        if (errors[project]) throw new Error(`Saved, but the queue could not be refreshed: ${errors[project]}. Retry its read before another change.`)
      }
    }
    finally { if (started === epoch) busy.value = false }
  }
  const add = (project: string, ticket: string, target?: QueueTarget) => write(project, async () => {
    const started = epoch
    const current = entry(project, ticket)
    const changingRoute = current && ((current.target_agent_id ?? null) !== (target?.agent_id ?? null) || (target && current.model_profile_id !== target.profile_id))
    // Part A rejects changing an active entry's target. A deliberate route
    // change replaces that entry through its existing remove/add operations.
    if (changingRoute) await removeFromQueue(ticket)
    if (started !== epoch) return
    try { await addToQueue(ticket, target) }
    catch (e) {
      if (changingRoute && started === epoch) {
        await load(project, true)
        throw new Error(`The ticket left its previous queue place, but the new route failed: ${e instanceof Error ? e.message : 'request refused'}. Choose Queue or another target to retry.`)
      }
      throw e
    }
    if (started !== epoch) return
    const sent = rowStore.mark()
    // New/Backlog become Open on add; adopt that authoritative node revision.
    const node = await getNode(ticket).catch(() => null)
    if (node && started === epoch) rowStore.adoptNode(node, sent, { show: true })
  })
  const remove = (project: string, ticket: string) => write(project, () => removeFromQueue(ticket))
  const reset = (project: string) => write(project, () => resetQueue())
  function order(project: string, ids: string[], ticket: string) {
    const before = snapshots[project]?.items.filter(item => !item.target_agent_id) ?? []
    const from = before.findIndex(item => item.ticket_id === ticket), to = ids.indexOf(ticket)
    if (from < 0 || to < 0 || from === to) return Promise.resolve()
    // Positions are workspace-relative even in a project-filtered response.
    const position = before[to]!.position
    return write(project, () => moveQueue(ticket, position))
  }
  function move(project: string, ticket: string, direction: -1 | 1 | 'top') {
    const current = entry(project, ticket)
    if (!current || current.target_agent_id) return Promise.resolve()
    if (direction === 'top' && current.position !== 1) return write(project, () => moveQueue(ticket, 1))
    const ids = snapshots[project]?.items.filter(item => !item.target_agent_id).map(item => item.ticket_id) ?? []
    const next = movedQueue(ids, ticket, direction)
    return next === ids ? Promise.resolve() : order(project, next, ticket)
  }
  return { snapshots, errors, busy, load, entry, add, remove, reset, move, order }
})
