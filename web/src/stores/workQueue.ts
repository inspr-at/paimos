// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { onScopeDispose, reactive, ref } from 'vue'
import { onReset } from '../lib/position'
import { addToQueue, movedQueue, readQueue, removeFromQueue, reorderQueue, resetQueue, type QueueSnapshot, type QueueTarget } from '../lib/workQueue'

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
      } finally { if (pending.get(project) === request) pending.delete(project) }
    })()
    pending.set(project, request); return request
  }
  function entry(project: string, ticket: string) { return snapshots[project]?.items.find(item => item.ticket_id === ticket) ?? null }
  async function write(project: string, action: () => Promise<unknown>) {
    if (busy.value) throw new Error('A queue change is already being saved.')
    const started = epoch
    busy.value = true
    try { await action(); if (started === epoch) await load(project, true) }
    finally { if (started === epoch) busy.value = false }
  }
  const add = (project: string, ticket: string, target?: QueueTarget) => write(project, () => addToQueue(ticket, target))
  const remove = (project: string, ticket: string) => write(project, () => removeFromQueue(ticket))
  const reset = (project: string) => write(project, () => resetQueue(project))
  const order = (project: string, ids: string[]) => write(project, () => reorderQueue(project, ids))
  function move(project: string, ticket: string, direction: -1 | 1 | 'top') {
    const ids = snapshots[project]?.items.filter(item => !item.target_agent_id).map(item => item.ticket_id) ?? []
    const next = movedQueue(ids, ticket, direction)
    return next === ids ? Promise.resolve() : order(project, next)
  }
  return { snapshots, errors, busy, load, entry, add, remove, reset, move, order }
})
