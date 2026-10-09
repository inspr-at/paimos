// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { onScopeDispose, reactive, ref } from 'vue'
import { onReset } from '../lib/position'
import { onAccessChange } from '../lib/authz'
import { APIError, getNode } from '../lib/api'
import { getSession } from '../lib/agentRows'
import type { Question } from '../lib/decisionDeskApi'
import { canLaunchLead, emptyFold, foldDecisions, leadLaunchReason, pauseLead, readLead, readLeadDecisions, readLeadSettings, readOpenQuestions, startLead, type DecisionFold, type LeadDecision, type LeadSettings, type ProjectLead } from '../lib/lead'
import { useAgents } from './agents'

// AEON-741: read projections of each project's lead. The server decides state;
// writes carry the revision and generation shown on screen and refuse when the
// lead or the viewer changed meanwhile. A write makes every earlier read stale.
export interface LeadView {
  lead: ProjectLead | null; error: string; forbidden: boolean; loadedAt: number | null
  settings: LeadSettings | null
  // The project's history across generations; totals live in the fold, so
  // trimming the retained decisions never changes them.
  decisions: LeadDecision[]; fold: DecisionFold; after: number; caughtUp: boolean; decisionsError: string
  // The lead's agent, kept after its session stops so its questions stay visible.
  principal: { session: string; id: string } | null
  questions: Question[] | null; questionsMore: boolean
}
const RETAIN = 2000
const PROJECT_CAP = 50
const blank = (): LeadView => ({ lead: null, error: '', forbidden: false, loadedAt: null, settings: null, decisions: [], fold: emptyFold(), after: 0, caughtUp: false, decisionsError: '', principal: null, questions: null, questionsMore: false })
const message = (e: unknown, fallback: string) => e instanceof Error && e.message ? e.message : fallback

export const useProjectLeads = defineStore('projectLeads', () => {
  const views = reactive<Record<string, LeadView>>({})
  const keys = reactive<Record<string, { key: string; title: string } | null>>({})
  const busy = reactive<Record<string, boolean>>({})
  const truncated = ref(false)
  let epoch = 0
  const pending = new Map<string, Promise<void>>()
  // Per project: bumped by every write, so a read that began earlier is dropped.
  const writes = new Map<string, number>()
  const clear = () => {
    epoch++; writes.clear()
    for (const id of Object.keys(views)) delete views[id]
    for (const id of Object.keys(keys)) delete keys[id]
    for (const id of Object.keys(busy)) delete busy[id]
    pending.clear(); truncated.value = false
  }
  onScopeDispose(onReset(clear))
  onScopeDispose(onAccessChange(change => {
    const projects = Object.keys(views)
    clear()
    if (change === 'refresh') for (const id of projects) void load(id)
  }))
  const view = (id: string) => views[id] ??= blank()

  /** Lead state only: the Agents page reads one per project, bounded. */
  async function loadLead(id: string, started = epoch) {
    const wrote = writes.get(id) ?? 0
    const stale = () => started !== epoch || (writes.get(id) ?? 0) !== wrote
    try {
      const lead = await readLead(id)
      if (stale()) return
      const shown = view(id).lead
      if (shown && lead.revision < shown.revision) return
      Object.assign(view(id), { lead, error: '', forbidden: false, loadedAt: Date.now() })
    } catch (e) {
      if (stale()) return
      Object.assign(view(id), { error: message(e, 'The lead could not be read.'), forbidden: e instanceof APIError && e.status === 403, loadedAt: Date.now() })
    }
  }
  async function loadMany(ids: string[]) {
    const started = epoch, list = ids.slice(0, PROJECT_CAP)
    truncated.value = ids.length > PROJECT_CAP
    let next = 0
    const worker = async () => { while (next < list.length && started === epoch) await loadLead(list[next++]!, started) }
    await Promise.all(Array.from({ length: Math.min(4, list.length) }, worker))
  }

  /** Everything the band and panel show for one project. */
  function load(id: string): Promise<void> {
    if (!id) return Promise.resolve()
    if (pending.has(id)) return pending.get(id)!
    const started = epoch
    const request = (async () => {
      await loadLead(id, started)
      if (started !== epoch) return
      const current = view(id), lead = current.lead
      if (!lead || current.error) return
      // Settings change rarely; the Start lead sheet always reads them fresh.
      const tasks: Promise<unknown>[] = current.settings ? [] : [
        readLeadSettings(id).then(settings => { if (started === epoch) view(id).settings = settings }).catch(() => { if (started === epoch) view(id).settings = null }),
      ]
      if (lead && lead.state !== 'none') {
        tasks.push(loadDecisions(id, started))
        if (lead.session_id && current.principal?.session !== lead.session_id) tasks.push(loadPrincipal(id, lead.session_id, started))
        tasks.push(loadQuestions(id, started))
      } else Object.assign(current, { decisions: [], fold: emptyFold(), after: 0, caughtUp: true, questions: [], questionsMore: false })
      await Promise.all(tasks)
    })().finally(() => { if (pending.get(id) === request) pending.delete(id) })
    pending.set(id, request)
    return request
  }
  async function loadDecisions(id: string, started: number) {
    const from = view(id).after
    try {
      const page = await readLeadDecisions(id, from)
      // Another read already moved the cursor: folding again would count twice.
      if (started !== epoch || view(id).after !== from) return
      const next = view(id)
      const fresh = page.items.filter(item => item.event_id > from)
      foldDecisions(next.fold, fresh)
      const merged = [...next.decisions, ...fresh]
      Object.assign(next, { decisions: merged.slice(-RETAIN), after: page.after, caughtUp: page.caughtUp, decisionsError: '' })
      void resolveKeys(merged.slice(-60).flatMap(d => d.request.ticket_node_id ? [d.request.ticket_node_id] : []), started)
    } catch (e) { if (started === epoch) view(id).decisionsError = message(e, 'The lead’s decisions could not be read.') }
  }
  async function loadPrincipal(id: string, session: string, started: number) {
    try {
      const read = await getSession(id, session)
      // A reset or another lead meanwhile: the read belongs to nobody on screen now.
      if (started !== epoch || view(id).lead?.session_id !== session) return
      // The session read passes the session ledger like every other copy (AEON-449).
      const [row] = useAgents().admitSessions([read])
      if (row) view(id).principal = { session, id: row.agent_principal_id }
    } catch { /* read again with the next load; the previous lead's asker stays */ }
  }
  async function loadQuestions(id: string, started: number) {
    try {
      const page = await readOpenQuestions(id)
      if (started !== epoch) return
      Object.assign(view(id), { questions: page.items.filter(q => q.project_id === id), questionsMore: page.more })
    } catch {
      // Unread questions are reported, never hidden: the previous read stays, marked incomplete.
      if (started === epoch) view(id).questionsMore = true
    }
  }
  /** Ticket keys for ids the lead reported; at most 12 reads per call, each once. */
  async function resolveKeys(ids: string[], started = epoch) {
    const wanted = [...new Set(ids)].filter(id => !(id in keys)).slice(0, 12)
    await Promise.all(wanted.map(async id => {
      keys[id] = null
      try { const node = await getNode(id); if (started === epoch) keys[id] = { key: node.key, title: node.title } }
      catch { if (started === epoch) delete keys[id] }
    }))
  }
  const keyOf = (id: string) => keys[id]?.key ?? 'a ticket'

  async function write(id: string, action: (lead: ProjectLead) => Promise<ProjectLead>): Promise<ProjectLead | undefined> {
    const lead = view(id).lead
    if (busy[id] || !lead) return
    const started = epoch
    const invalidate = () => { writes.set(id, (writes.get(id) ?? 0) + 1); pending.delete(id) }
    busy[id] = true
    invalidate()
    try {
      const next = await action(lead)
      if (started !== epoch) return
      view(id).lead = next
      return next
    } finally {
      // Reads that began while the write was in flight may predate it too; read afresh.
      if (started === epoch) { busy[id] = false; invalidate(); void load(id) }
    }
  }
  const start = (id: string) => write(id, lead => {
    if (!canLaunchLead(lead)) throw new Error(leadLaunchReason(lead))
    return startLead(id, lead.revision)
  })
  const pause = (id: string) => write(id, lead => pauseLead(id, lead.revision, lead.generation))
  return { views, keys, busy, truncated, view, load, loadLead, loadMany, start, pause, keyOf, resolveKeys }
})
