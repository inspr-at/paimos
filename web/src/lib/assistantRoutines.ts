// SPDX-License-Identifier: AGPL-3.0-only
import type { Kind, WorkNode } from './api'
import { assistantRequest, type AssistantRoutine, type AssistantFacts } from './assistants'
import { assistantCopy } from './assistantsCopy'
import { isKnowledgeType } from './knowledge'
import { recurrenceName, recurrenceZone, triggerWords, type Recurrence } from './recurrences'
import { checkedDetail, checkedRuns, runTitle } from './routineRuns'
import { ticketPath } from './work'

export interface OwnedAssistantRoutine {
  owner: string; wire: Recurrence; view: AssistantRoutine
  activity?: NonNullable<AssistantFacts['activity']>[number]
  lastActivity?: AssistantFacts['lastActivity']
}
interface AgentOwner { owner_principal_id: string }
interface AgentResult { run_id: string; status: 'succeeded' | 'failed'; node_id?: string }

// D exposes an owner read per visible definition, not an owner-filtered list.
// Bound discovery to two 100-row UUID pages, six reads at a time. History is
// the latest run of at most twenty owned definitions, with that window explicit.
export async function readAssistantRoutines(owners: Set<string>, projectKey: (id: string) => string | undefined, now: number, signal: AbortSignal): Promise<{ rows: OwnedAssistantRoutine[]; truncated: boolean }> {
  const candidates: Recurrence[] = [], seen = new Set<string>()
  let after = '', truncated = false
  for (let page = 0; page < 2; page++) {
    const result = await assistantRequest<{ items: Recurrence[]; next_cursor?: string | null }>(`/recurrences?scope=project${after ? `&after=${encodeURIComponent(after)}` : ''}`, 'GET', undefined, signal)
    if (!Array.isArray(result.items) || result.items.length > 100) throw new Error(assistantCopy().invalidResponse)
    candidates.push(...result.items)
    if (!result.next_cursor) { truncated = false; break }
    if (seen.has(result.next_cursor)) throw new Error(assistantCopy().invalidResponse)
    seen.add(result.next_cursor); after = result.next_cursor; truncated = true
  }
  const rows: OwnedAssistantRoutine[] = []
  let cursor = 0
  await Promise.all(Array.from({ length: Math.min(6, candidates.length) }, async () => {
    while (cursor < candidates.length) {
      const wire = candidates[cursor++]!
      const owner = await assistantRequest<AgentOwner | null>(`/recurrences/${encodeURIComponent(wire.id)}/agent-owner`, 'GET', undefined, signal)
      if (owner && owners.has(owner.owner_principal_id)) rows.push({ owner: owner.owner_principal_id, wire, view: presentAssistantRoutine(wire) })
    }
  }))
  rows.sort((a, b) => a.view.name.localeCompare(b.view.name) || a.wire.id.localeCompare(b.wire.id))
  let kinds: Promise<{ items: Kind[] }> | undefined
  const words = assistantCopy()
  cursor = 0
  await Promise.all(Array.from({ length: Math.min(6, rows.length, 20) }, async () => {
    while (cursor < Math.min(rows.length, 20)) {
      const row = rows[cursor++]!
      try {
        const page = checkedRuns(await assistantRequest<unknown>(`/recurrences/${encodeURIComponent(row.wire.id)}/runs?limit=1`, 'GET', undefined, signal))
        if (page.items.length > 1) throw new Error(words.invalidResponse)
        const latest = page.items[0]
        if (!latest) { row.view.lastState = words.noRoutineRuns; continue }
        if (latest.owner_principal_id !== row.owner) { row.view.lastState = words.otherRoutineOwner; continue }
        const runId = latest.id
        const base = `/recurrences/${encodeURIComponent(row.wire.id)}/runs/${encodeURIComponent(runId)}`
        const detail = checkedDetail(await assistantRequest<unknown>(base, 'GET', undefined, signal), runId)
        // Grants can change owners; frozen historical work must never be
        // attributed to the routine's current assistant without this check.
        if (detail.run.owner_principal_id !== row.owner) continue
        row.view.lastState = runTitle(detail.run, now)
        row.activity = { at: detail.run.created_at, kind: 'runs', text: words.routineRun(row.view.name, row.view.lastState), href: `/agents/routines?routine=${encodeURIComponent(row.wire.id)}&run=${encodeURIComponent(runId)}` }
        if (['running', 'done', 'failed'].includes(detail.run.state)) row.lastActivity = { at: detail.run.updated_at, text: words.routineRun(row.view.name, row.view.lastState) }
        const result = await assistantRequest<AgentResult | null>(`${base}/agent-result`, 'GET', undefined, signal)
        if (!result?.node_id || result.run_id !== runId || result.status !== 'succeeded') continue
        const node = await assistantRequest<WorkNode>(`/nodes/${encodeURIComponent(result.node_id)}`, 'GET', undefined, signal)
        kinds ??= assistantRequest<{ items: Kind[] }>('/kinds', 'GET', undefined, signal)
        const catalog = await kinds
        if (!Array.isArray(catalog.items) || catalog.items.length > 500) throw new Error(words.invalidResponse)
        const kind = catalog.items.find(item => item.id === node.kind_id)?.slug, key = projectKey(row.wire.project_id)
        if (!key || !kind) continue
        if (isKnowledgeType(kind) && typeof node.fields.slug === 'string') row.view.result = { label: node.title, href: `/p/${encodeURIComponent(key)}/knowledge/${encodeURIComponent(kind)}/${encodeURIComponent(node.fields.slug)}` }
        else if (['work', 'ticket', 'task', 'epic'].includes(kind)) row.view.result = { label: `${node.key} · ${node.title}`, href: ticketPath(key, node.key) }
      } catch (error) {
        if (signal.aborted) throw error
        // A missing history/result remains unknown. The owned routine and its
        // schedule still stand, and the panel discloses the bounded read window.
        row.view.lastState = words.routineHistoryUnavailable
      }
    }
  }))
  return { rows, truncated }
}
export function presentAssistantRoutine(wire: Recurrence): AssistantRoutine {
  return { id: wire.id, projectId: wire.project_id, name: recurrenceName(wire), schedule: triggerWords(wire.trigger), next: wire.next_at, paused: wire.paused, revision: wire.revision, timezone: recurrenceZone(wire.trigger) }
}
