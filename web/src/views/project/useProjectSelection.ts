// SPDX-License-Identifier: AGPL-3.0-only

import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError, type BulkChange, type BulkResult, type ListItem } from '../../lib/api'
import { askDoneGate } from '../../lib/doneGateAsk'
import { benefitSkip, benefitStepSummary, completionFields, needsBenefitPrompt, skippedStatusLabel } from '../../lib/doneGate'
import { can } from '../../lib/authz'
import { toast } from '../../lib/toast'
import { rowTags } from '../../lib/ticketList'
import { rowStore } from '../../lib/rowStore'
import { plural, PRIORITIES, priorityLabel, statusMeta } from '../../lib/work'
import { queueable } from '../../lib/workQueue'
import { type LabelChoice } from '../../components/work/LabelMenu.vue'
import { AssignCancelled, assignToRelease, type ReleaseTarget } from '../../lib/releaseAssign'
import { openedMembershipMessage, releaseViewIsParent } from '../../lib/releaseMembership'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'
import type { useProjectPanel } from './useProjectPanel'

export function useProjectSelection(state: ProjectState, data: ReturnType<typeof useProjectData>, pane: ReturnType<typeof useProjectPanel>) {
  const { projects, project, projectId, queue, mayQueue, knowledgeActive, settingsActive, deliveryActive, graphActive, outlineActive, table, cursorId, fullView, me, writable } = state
  const { list, outline, activeLive, sequence } = data
  const { nativeReleases, refreshMemberships, loadProjectPeople, people, getPeopleGeneration } = pane
  // ---------- Selection and bulk changes ----------
  const selectable = computed(() => writable.value && !knowledgeActive.value && !settingsActive.value && !deliveryActive.value && !graphActive.value && !fullView.value)
  const selected = ref(new Set<string>())
  // Phone selection can be armed before the first card is chosen.
  const phoneQuery = window.matchMedia('(max-width: 720px)')
  const phone = ref(phoneQuery.matches)
  const phonePicking = ref(false)
  const picking = computed(() => phonePicking.value || selected.value.size > 0)
  const onPhone = () => { phone.value = phoneQuery.matches; if (!phone.value) phonePicking.value = false }
  let selectAnchor: string | null = null
  function selectRow(row: ListItem, mode: 'toggle' | 'range') {
    const next = new Set(selected.value)
    const rows = sequence.value
    const from = selectAnchor ? rows.findIndex(item => item.id === selectAnchor) : -1
    const to = rows.findIndex(item => item.id === row.id)
    if (mode === 'range' && from !== -1 && to !== -1) {
      for (let i = Math.min(from, to); i <= Math.max(from, to); i++) next.add(rows[i].id)
    } else {
      if (next.has(row.id)) next.delete(row.id); else next.add(row.id)
      selectAnchor = row.id
    }
    selected.value = next
    if (phone.value && next.size) phonePicking.value = true
    cursorId.value = row.id
  }
  function selectAll(on: boolean) {
    selected.value = on ? new Set(sequence.value.map(row => row.id)) : new Set()
    selectAnchor = on ? sequence.value[0]?.id ?? null : null
    if (!on) phonePicking.value = false
    else if (phone.value) phonePicking.value = true
  }
  function clearSelection() { selected.value = new Set(); selectAnchor = null; phonePicking.value = false }
  // Everything that matches, beyond the loaded pages (up to the bulk limit of 500).
  async function selectAllMatching() {
    if (list.cursor.value) await list.loadAll(500)
    selectAll(true)
  }
  // Rows that left the list leave the selection.
  const selectionRows = computed(() => outlineActive.value ? outline.loadedRows.value : list.rows.value)
  watch(selectionRows, rows => {
    if (!selected.value.size) return
    const ids = new Set(rows.map(row => row.id))
    const kept = [...selected.value].filter(id => ids.has(id))
    if (kept.length !== selected.value.size) selected.value = new Set(kept)
  })
  watch(selectable, on => { if (!on) clearSelection() })
  const selectedRows = computed(() => selectionRows.value.filter(row => selected.value.has(row.id)))
  // Selected tickets someone deleted meanwhile: the bulk bar says so, and changes leave them out.
  const selectedDeleted = computed(() => activeLive.value.deletedAmong(selected.value))
  const liveSelection = () => { const gone = new Set(selectedDeleted.value); return [...selected.value].filter(id => !gone.has(id)) }
  // Where the list is, so the bulk bar centres over it (a docked panel takes the right).
  const listFrame = ref<{ left: number; width: number } | null>(null)
  let frameObserver: ResizeObserver | undefined
  function measureFrame() {
    const el = (table.value as unknown as { $el?: HTMLElement } | undefined)?.$el
    if (!el) return
    const rect = el.getBoundingClientRect()
    listFrame.value = { left: Math.round(rect.left), width: Math.round(rect.width) }
  }
  watch(() => selected.value.size > 0, on => {
    frameObserver?.disconnect()
    window.removeEventListener('resize', measureFrame)
    if (!on) return
    measureFrame()
    const el = (table.value as unknown as { $el?: HTMLElement } | undefined)?.$el
    if (el) { frameObserver = new ResizeObserver(measureFrame); frameObserver.observe(el) }
    window.addEventListener('resize', measureFrame)
  })
  onBeforeUnmount(() => { frameObserver?.disconnect(); window.removeEventListener('resize', measureFrame) })
  const bulkBusy = ref(false)
  const bulkMenu = ref<{ kind: 'status' | 'assignee' | 'priority' | 'labels' | 'move' | 'release'; anchor: HTMLElement } | null>(null)
  let bulkMenuGeneration = 0
  const releaseIds = ref<string[]>([])
  async function openBulk(kind: NonNullable<typeof bulkMenu.value>['kind'], anchor?: HTMLElement | null) {
    const opening = ++bulkMenuGeneration
    const at = anchor ?? document.querySelector<HTMLElement>(`.bulk-bar [aria-keyshortcuts="${{ status: 's', assignee: 'a', priority: 'p', labels: 'l', move: 'm', release: 'g' }[kind]}"]`)
    if (!at) return
    if (kind === 'release') { openRelease(at, liveSelection()); return }
    if (kind === 'labels') void list.requestFacet('tag')
    if (kind === 'assignee') {
      const request = getPeopleGeneration()
      const selection = () => JSON.stringify(selectedRows.value.map(row => [row.id, row.updated_at]))
      const shown = selection()
      await loadProjectPeople()
      if (opening !== bulkMenuGeneration || request !== getPeopleGeneration() || !at.isConnected || selection() !== shown) return
    }
    if (kind === 'move') void list.loadEpics()
    bulkMenu.value = { kind, anchor: at }
  }
  function closeBulk(restore: boolean) {
    bulkMenuGeneration++
    const anchor = bulkMenu.value?.anchor
    bulkMenu.value = null
    if (restore) anchor?.focus()
  }
  const bulkPeople = computed(() => {
    const mine = me.value ? [{ value: me.value.id, label: me.value.name, hint: 'you' }] : []
    return [{ value: '', label: 'Unassigned' }, ...mine, ...people.value.filter(person => person.id !== me.value?.id).map(person => ({ value: person.id, label: person.name }))]
  })
  const bulkPriorities = [...PRIORITIES.map(p => ({ value: p.value as string, label: p.label as string })), { value: '', label: 'No priority' }]
  const bulkLabels = computed<LabelChoice[]>(() => {
    const byName = new Map<string, LabelChoice>()
    for (const [name] of Object.entries(list.facetCounts('tag'))) if (name !== 'none' && !byName.has(name.toLowerCase())) byName.set(name.toLowerCase(), { name, color: list.colors.get(name.toLowerCase()) ?? '', on: 0 })
    for (const row of selectedRows.value) for (const tag of rowTags(row)) {
      const key = tag.name.toLowerCase()
      if (!byName.has(key)) byName.set(key, { name: tag.name, color: tag.color, on: 0 })
    }
    for (const choice of byName.values()) choice.on = selectedRows.value.filter(row => rowTags(row).some(tag => tag.name.toLowerCase() === choice.name.toLowerCase())).length
    return [...byName.values()].sort((a, b) => b.on - a.on || a.name.localeCompare(b.name))
  })
  // Review: the tickets that changed meanwhile become the selection, with their newer values.
  function reviewConflicts(ids: string[]) {
    const shown = ids.filter(id => (outlineActive.value ? outline.rows.value : list.rows.value).some(row => row.id === id))
    if (!shown.length) return
    selected.value = new Set(shown)
    selectAnchor = shown[0]
    if (phone.value) phonePicking.value = true
    cursorId.value = shown[0]
    void nextTick(() => { table.value?.scrollToRow(shown[0]); table.value?.focusGrid() })
  }
  async function runBulk(change: Omit<BulkChange, 'ids'>, done: (count: number) => string, ids = [...selected.value]) {
    const gone = new Set(selectedDeleted.value)
    ids = ids.filter(id => !gone.has(id))
    if (!ids.length || bulkBusy.value) return
    closeBulk(false)
    bulkBusy.value = true
    try {
      const bulkRows = selectionRows.value
      const placements = new Map(bulkRows.map(row => [row.id, { parent: row.parent_id, epic: outline.epicOf(row.id) }]))
      const result = await list.applyBulk({ ids, ...change }, bulkRows)
      for (const answer of result.items) {
        const from = placements.get(answer.id), row = rowStore.visibleRow(answer.id)
        if (from && row && row.parent_id !== from.parent) outline.relocate(row, from.parent, from.epic)
      }
      // Changed meanwhile: nothing was overwritten; Review shows those tickets.
      const conflicts = result.skipped.filter(item => item.code === 'conflict')
      const reported = result.skipped.filter(item => !benefitSkip(item) && item.code !== 'conflict')
      const held = result.skipped.filter(item => benefitSkip(item))
      const changed = result.items.length
      const skipped = reported.length
      const eventId = result.event_id
      if (changed) {
        toast(`${done(changed)}${skipped ? ` · ${plural(skipped, 'ticket')} skipped` : ''}`, {
          timeout: 8000, action: eventId ? { label: 'Undo', run: () => void undoBulk(eventId) } : undefined,
        })
      } else if (!skipped && !held.length && !conflicts.length) toast(`Nothing to change: ${plural(result.unchanged.length, 'ticket is', 'tickets are')} already so`)
      if (conflicts.length) {
        const ids = conflicts.map(item => item.id)
        const which = conflicts.length === 1 ? `${conflicts[0].key ?? 'A ticket'} was` : `${plural(conflicts.length, 'ticket')} were`
        toast(`${which} changed elsewhere meanwhile and kept ${conflicts.length === 1 ? 'its' : 'their'} newer version.`, { tone: 'error', timeout: 10000, action: { label: 'Review', run: () => reviewConflicts(ids) } })
      }
      if (skipped) {
        const first = reported[0]
        toast(`${first.key ?? 'A ticket'} was skipped: ${first.reason}${skipped > 1 ? ` (and ${skipped - 1} more)` : ''}`, { tone: 'error' })
      }
      if (changed || conflicts.length) { void list.load(); void projects.load(true); for (const node of result.items) outline.refreshStatsFor(node.id) }
      if (change.state && held.length) await offerBenefitSkips(held, change.state)
    } catch (e) {
      toast(`Nothing was changed: ${problem(e)}`, { tone: 'error' })
    } finally {
      bulkBusy.value = false
      table.value?.focusGrid()
    }
  }
  async function stepBenefitGate(rows: ListItem[], state: string) {
    let done = 0
    const skipped: ListItem[] = []
    for (let index = 0; index < rows.length; index++) {
      const row = list.rows.value.find(item => item.id === rows[index].id) ?? rows[index]
      if (!needsBenefitPrompt(row, state)) {
        if (await list.setStatus(row, state)) { done++; void projects.load(true); outline.refreshStatsFor(row.id) }
        else skipped.push(row)
        continue
      }
      const text = await askDoneGate({ key: row.key, title: row.title, state, fields: row.fields ?? {} }, { index: index + 1, total: rows.length })
      if (!text) { skipped.push(row); continue }
      if (await list.setStatus(row, state, { fields: completionFields(row.fields, text) })) {
        done++
        void projects.load(true)
        outline.refreshStatsFor(row.id)
      } else skipped.push(row)
    }
    return { done, skipped }
  }
  function announceBenefitStep(done: number, skipped: ListItem[]) {
    if (!done && !skipped.length) return
    const still = skippedStatusLabel(skipped.map(row => statusMeta(row.state).label))
    const phone = window.matchMedia('(max-width: 700px)').matches
    toast(benefitStepSummary(done, skipped.length, still), phone ? { sticky: true, key: 'benefit-step' } : { timeout: 8000, key: 'benefit-step' })
  }
  async function offerBenefitSkips(held: BulkResult['skipped'], state: string) {
    const rows = held.flatMap(item => {
      const row = list.rows.value.find(entry => entry.id === item.id)
      return row ? [row] : []
    })
    if (!rows.length) return
    const outcome = await stepBenefitGate(rows, state)
    announceBenefitStep(outcome.done, outcome.skipped)
  }
  async function undoBulk(eventId: number) {
    try {
      await list.undoBulk(eventId)
      toast('Undone: the tickets are as they were')
      void list.load(); if (outlineActive.value) outline.reload(); void projects.load(true); void refreshMemberships()
    } catch (e) {
      toast(e instanceof APIError && e.status === 409 ? 'Some of them changed since, so nothing was undone.' : `Undo did not work: ${problem(e)}`, { tone: 'error' })
    }
  }
  const count = (n: number) => plural(n, 'ticket')
  async function bulkStatus(state: string) {
    if (bulkBusy.value) return
    closeBulk(false)
    await runBulk({ state }, n => `${count(n)} ${n === 1 ? 'is' : 'are'} now ${statusMeta(state).label}`)
  }
  function bulkArchive() { void runBulk({ state: 'archived' }, n => `Archived ${count(n)}`) }
  function bulkAssign(value: string) {
    const name = bulkPeople.value.find(person => person.value === value)?.label ?? 'someone'
    void runBulk({ assignee: value || null }, n => value ? `Assigned ${count(n)} to ${name}` : `Unassigned ${count(n)}`)
  }
  function bulkPriority(value: string) { void runBulk({ priority: value || null }, n => value ? `${count(n)} ${n === 1 ? 'is' : 'are'} now ${priorityLabel(value)} priority` : `Cleared the priority of ${count(n)}`) }
  function bulkLabelsApply(change: { add: { name: string; color?: string }[]; remove: string[] }) {
    void runBulk({ tags_add: change.add, tags_remove: change.remove }, n => `Changed the labels of ${count(n)}`)
  }
  function bulkMove(epic: { id: string; key: string; title: string } | null) {
    const target = epic?.id ?? project.value?.id
    if (!target) return
    void runBulk({ parent_id: target }, n => epic ? `Moved ${count(n)} to ${epic.title}` : `Took ${count(n)} out of their epic`)
  }
  function openRelease(anchor: HTMLElement, ids: string[]) {
    bulkMenuGeneration++
    if (!ids.length || !can('releases.write', project.value?.id)) return
    releaseIds.value = ids
    bulkMenu.value = { kind: 'release', anchor }
  }
  let releaseAttempt = 0
  async function chooseRelease(target: ReleaseTarget) {
    const projectId = project.value?.id
    if (!projectId || bulkBusy.value) return
    const ids = [...releaseIds.value]
    const rows = list.rows.value.filter(row => ids.includes(row.id))
    const tickets = ids.map(id => {
      const row = rows.find(item => item.id === id)
      return { id, key: row?.key ?? id, title: row?.title ?? '', state: row?.state, kind: row?.kind_slug, isParent: releaseViewIsParent(nativeReleases.value.get(id)) }
    })
    closeBulk(false)
    const attempt = ++releaseAttempt
    bulkBusy.value = true
    const here = () => attempt === releaseAttempt && project.value?.id === projectId
    try {
      const outcome = await assignToRelease(projectId, tickets, target)
      if (!here()) return
      const changed = outcome.opened ? openedMembershipMessage(outcome.opened) : null
      if (changed) {
        toast(changed)
        void refreshMemberships()
        return
      }
      const joined = outcome.opened?.status === 'added'
        ? outcome.opened.count
        : outcome.result?.walker.tickets.filter(ticket => (outcome.result?.leaf_node_ids ?? ids).includes(ticket.ticket_node_id) && ticket.included).length ?? 0
      if (!joined) {
        toast('Nothing was added to the release.')
        void refreshMemberships()
        return
      }
      const title = outcome.opened?.status === 'added' ? outcome.opened.releaseTitle : outcome.releaseTitle
      const leftOut = outcome.skipped.length ? ` · ${plural(outcome.skipped.length, 'ticket')} left out` : ''
      const eventId = outcome.result?.event_id
      toast(`Added ${plural(joined, 'ticket')} to ${title}${leftOut}`, {
        timeout: 8000,
        action: eventId ? { label: 'Undo', run: () => void undoBulk(eventId) } : undefined,
      })
      clearSelection()
      void list.load()
      void refreshMemberships()
    } catch (error) {
      if (!here()) return
      if (error instanceof AssignCancelled) return
      toast(problem(error), { tone: 'error' })
    } finally {
      if (attempt === releaseAttempt) {
        bulkBusy.value = false
        if (project.value?.id === projectId) table.value?.focusGrid()
      }
    }
  }

  async function bulkQueue() {
    const id = projectId.value
    if (!id || !mayQueue.value || bulkBusy.value || queue.busy) return
    bulkBusy.value = true
    let added = 0
    const skipped: string[] = []
    try {
      for (const row of selectedRows.value) {
        if (queue.entry(id, row.id)) continue
        if (!queueable(row)) { skipped.push(row.key); continue }
        try {
          if (!(await queue.checkReadiness(row)).ready) { skipped.push(row.key); continue }
          await queue.add(id, row.id); added++
        }
        catch (e) { toast(`${row.key}: ${e instanceof Error ? e.message : 'queue failed'}`, { tone: 'error' }); break }
      }
      toast(`${added} queued${skipped.length ? `; not ready or not queueable: ${skipped.join(', ')}` : ''}`)
    } finally { bulkBusy.value = false }
  }
  // Shift with j/k or the arrows grows the selection from the cursor row.
  function extendSelection(step: number, move: (step: number) => Promise<void>) {
    const rows = sequence.value
    const at = rows.findIndex(row => row.id === cursorId.value)
    if (at === -1) { void move(step); return }
    const to = Math.max(0, Math.min(rows.length - 1, at + step))
    const next = new Set(selected.value)
    next.add(rows[at].id); next.add(rows[to].id)
    if (!selectAnchor) selectAnchor = rows[at].id
    selected.value = next
    cursorId.value = rows[to].id
    void nextTick(() => table.value?.scrollToRow(rows[to].id))
    table.value?.focusGrid()
  }

  function problem(e: unknown) { return e instanceof Error ? e.message : 'Something went wrong' }
  onMounted(() => phoneQuery.addEventListener('change', onPhone))
  onBeforeUnmount(() => phoneQuery.removeEventListener('change', onPhone))

  return { selectable, selected, phonePicking, picking, selectRow, selectAll, clearSelection, selectAllMatching, selectedRows, selectedDeleted, liveSelection, listFrame, bulkBusy, bulkMenu, releaseIds, openBulk, closeBulk, bulkPeople, bulkPriorities, bulkLabels, bulkStatus, bulkArchive, bulkAssign, bulkPriority, bulkLabelsApply, bulkMove, openRelease, chooseRelease, bulkQueue, extendSelection }
}
