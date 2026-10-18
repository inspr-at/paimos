// SPDX-License-Identifier: AGPL-3.0-only

import { nextTick, watch } from 'vue'
import { createNode, type ListItem } from '../../lib/api'
import { entryStatus, requestEntryPanel, type EntryChecks } from '../../lib/entryChecks'
import { benefitGateError } from '../../lib/doneGate'
import { can } from '../../lib/authz'
import { asListItem, guardedMove, keyPrefix, kinds } from '../../lib/useTicket'
import { toast } from '../../lib/toast'
import { command, consume } from '../../lib/commands'
import { remember } from '../../lib/recents'
import { type EpicRef } from '../../lib/ticketList'
import { rowStore } from '../../lib/rowStore'
import type { QuickDraft } from '../../components/work/QuickCreateRow.vue'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'
import type { useProjectPanel } from './useProjectPanel'
import type { useProjectKnowledge } from './useProjectKnowledge'

export function useProjectActions(state: ProjectState, data: ReturnType<typeof useProjectData>, pane: ReturnType<typeof useProjectPanel>, knowledgePage: ReturnType<typeof useProjectKnowledge>) {
  const { router, projects, session, ticketKey, project, projectId, routeKey, vocabulary, assigneeMenu, sectionPath, knowledgeActive, knowledgeEntryOpen, outlineActive, toolbarWrap, table, scrollRoot, cursorId, statusMenu, creating, fullView, skipGuard } = state
  const { list, outline } = data
  const { panelItem, loadProjectPeople, openKey, listQuery, closePanel, collapse, getPeopleGeneration } = pane
  const { knowledgeListQuery, knowledgeTab } = knowledgePage
  async function openAssignee(row: ListItem, anchor: HTMLElement) {
    const request = getPeopleGeneration(), revision = row.updated_at
    await loadProjectPeople()
    if (request !== getPeopleGeneration() || !anchor.isConnected || row.updated_at !== revision) return
    assigneeMenu.value = { row, anchor }
  }
  function closeAssignee(restore: boolean) { const anchor = assigneeMenu.value?.anchor; assigneeMenu.value = null; if (restore) anchor?.focus() }
  async function assignPerson(value: string) {
    const row = assigneeMenu.value?.row
    closeAssignee(true)
    if (!row || !can('nodes.write', projectId.value ?? undefined)) return
    await list.applyBulk({ ids: [row.id], assignee: value || null }).catch(e => toast(e.message, { tone: 'error' }))
  }
  // ---------- Actions ----------
  function copyKey(key: string) {
    navigator.clipboard.writeText(key).then(() => toast(`${key} copied`), () => toast(`${key} could not be copied`, { tone: 'error' }))
  }
  function openStatus(row: ListItem, anchor: HTMLElement, from: 'list' | 'panel') {
    statusMenu.value = statusMenu.value?.row.id === row.id && statusMenu.value.from === from ? null : { row, anchor, from }
  }
  function closeStatus(restore: boolean) {
    const menu = statusMenu.value
    statusMenu.value = null
    if (restore) menu?.anchor.focus()
  }
  function chooseStatus(state: string, checks: EntryChecks | null = null) {
    const menu = statusMenu.value
    statusMenu.value = null
    if (!menu) return
    const current = rowStore.latest(menu.row.id) ?? menu.row
    const block = checks?.blocks.find(b => b.to_status === entryStatus(state))
    // Use the menu's current advisory snapshot to open the one checklist before
    // changing the row. The mutation still enforces the live definition.
    if (checks?.node_id === current.id && checks.revision === current.updated_at && block?.checks.some(c => c.applicable && !c.passed)) {
      requestEntryPanel({ row: current, toStatus: state, anchor: menu.anchor })
      return
    }
    void list.setStatus(menu.row, state).then(changed => { if (changed) { void projects.load(true); outline.refreshStatsFor(menu.row.id) } })
    if (menu.from === 'panel') menu.anchor.focus()
    else table.value?.focusGrid()
  }
  function openEpic(epic: EpicRef) { openKey(epic.key) }

  // ---------- Create and remove ----------
  async function startCreate(under: ListItem | null = null) {
    const project = projectId.value, person = session.identity?.principal.id, tenant = session.identity?.tenant.id
    const revision = under?.updated_at
    await vocabulary.load()
    if (project !== projectId.value || person !== session.identity?.principal.id || tenant !== session.identity?.tenant.id || (under && outline.node(under.id)?.updated_at !== revision)) return
    if (!vocabulary.loaded) { toast(vocabulary.error, { tone: 'error' }); return }
    if (fullView.value) collapse()
    if (under && outlineActive.value) {
      creating.value = false
      outline.startCreateUnder(under.id)
      await nextTick(); table.value?.focusCreate()
      return
    }
    outline.startCreateUnder(null)
    creating.value = true
    if (scrollRoot.value && toolbarWrap.value && scrollRoot.value.scrollTop > toolbarWrap.value.offsetTop) scrollRoot.value.scrollTop = toolbarWrap.value.offsetTop
    await nextTick(); table.value?.focusCreate()
  }
  async function quickCreate(draft: QuickDraft): Promise<boolean> {
    const current = project.value
    if (!current) return false
    try {
      const catalog = await kinds()
      const kind = catalog.find(candidate => candidate.slug === 'work') ?? catalog.find(candidate => candidate.slug === draft.kind || candidate.slug === 'ticket')
      if (!kind) throw new Error('this workspace has no such type')
      const fields: Record<string, unknown> = draft.priority ? { priority: draft.priority } : {}
      const node = await createNode({
        kind_id: kind.id, title: draft.title, state: draft.state, fields,
        parent_id: draft.epic?.id ?? current.id, key_prefix: keyPrefix(current.routeKey),
      })
      const parent = draft.epic ? { ...draft.epic, kind_slug: kind.slug === 'work' ? 'work' : 'epic' } : { id: current.id, key: current.key, title: current.title, kind_slug: 'project' }
      const created = asListItem(node, kind, parent, { id: current.id, key: current.key, title: current.title })
      list.insertRow(created)
      outline.insert(created)
      cursorId.value = created.id
      toast(`Created ${node.key}`, { action: { label: 'Open', run: () => openKey(node.key) } })
      void projects.load(true)
      return true
    } catch (e) {
      if (benefitGateError(e)) {
        toast('The ticket was not created. Add a 2–4 word pill and a benefit in both languages.', { tone: 'error' })
        return false
      }
      toast(`The ticket was not created: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
      return false
    }
  }
  function childCreated(item: ListItem) { list.insertRow(item); outline.insert(item); void projects.load(true) }
  function childMoved(item: ListItem, fromParent: string | null) {
    outline.relocate(item, fromParent, fromParent && (outline.node(fromParent)?.is_leaf === false || outline.node(fromParent)?.kind_slug === 'epic') ? fromParent : null)
  }
  function closeCreate() { creating.value = false; outline.startCreateUnder(null) }
  // Drag and drop in the Outline: the same guarded move as the workspace's "Move to another epic".
  async function moveRow(row: ListItem, epic: ListItem | null) {
    const current = project.value
    if (!current) return
    const parent = epic ? { id: epic.id, key: epic.key, title: epic.title, kind_slug: epic.kind_slug } : { id: current.id, key: current.key, title: current.title, kind_slug: 'project' }
    if (await guardedMove(row, parent, childMoved) === 'ok') {
      if (epic) outline.setExpanded(epic.id, true)
      cursorId.value = row.id
    }
  }
  function removed(item: ListItem) {
    outline.remove(item)
    list.removeRow(item.id)
    void projects.load(true)
    skipGuard.value = true
    void router.replace({ path: sectionPath(), query: listQuery() }).finally(() => { skipGuard.value = false })
  }

  // Commands from the palette: New ticket in this project.
  watch(command, async value => {
    const next = value?.command
    if (next?.name === 'new-knowledge' && project.value && next.projectKey === project.value.routeKey) {
      consume()
      if (!knowledgeActive.value || knowledgeEntryOpen.value) await router.push({ path: `/p/${encodeURIComponent(routeKey.value)}/knowledge`, query: knowledgeListQuery.value })
      setTimeout(() => knowledgeTab.value?.openCreate(), 0)
      return
    }
    if (next?.name !== 'new-ticket' || !project.value || next.projectKey !== project.value.routeKey) return
    consume()
    if (ticketKey.value) closePanel()
    void nextTick(() => startCreate())
  }, { immediate: true })
  // Recently opened work and projects, for the palette.
  watch(panelItem, item => {
    if (item && project.value) remember({ type: 'ticket', key: item.key, title: item.title, state: item.state, kind: item.kind_slug, projectKey: project.value.routeKey })
  })
  watch(project, current => { if (current) remember({ type: 'project', key: current.routeKey, title: current.title }) }, { immediate: true })

  return { openAssignee, closeAssignee, assignPerson, copyKey, openStatus, closeStatus, chooseStatus, openEpic, startCreate, quickCreate, childCreated, childMoved, closeCreate, moveRow, removed }
}
