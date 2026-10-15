// SPDX-License-Identifier: AGPL-3.0-only

import { nextTick, onBeforeUnmount, onMounted } from 'vue'
import { type ListItem } from '../../lib/api'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'
import type { useProjectPanel } from './useProjectPanel'
import type { useProjectSelection } from './useProjectSelection'
import type { useProjectActions } from './useProjectActions'
import type { useProjectLive } from './useProjectLive'

export function useProjectKeyboard(state: ProjectState, data: ReturnType<typeof useProjectData>, pane: ReturnType<typeof useProjectPanel>, selection: ReturnType<typeof useProjectSelection>, actions: ReturnType<typeof useProjectActions>, live: ReturnType<typeof useProjectLive>) {
  const { ticketKey, knowledgeActive, settingsActive, deliveryActive, graphActive, outlineActive, toolbar, table, panel, cursorId, creating, fullView } = state
  const { list, outline, activeLive, liveActive, sequence } = data
  const { panelItem, openKey, openRow, closePanel, expand, collapse, trail, trailBack } = pane
  const { selectable, selected, phonePicking, selectRow, selectAll, clearSelection, openBulk, bulkQueue } = selection
  const { openAssignee, openStatus, startCreate, closeCreate } = actions
  const { showUpdates } = live
  // ---------- Keyboard ----------
  function typing(target: EventTarget | null) {
    if (target instanceof HTMLInputElement && ['checkbox', 'radio'].includes(target.type)) return false
    return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
  }
  async function move(step: number) {
    let rows = sequence.value
    if (!rows.length) return
    // At the end of what is loaded, fetch the next page first so j and Next keep going.
    const at = rows.findIndex(row => row.id === (ticketKey.value && panelItem.value ? panelItem.value.id : cursorId.value))
    if (step > 0 && at === rows.length - 1 && list.cursor.value && !outlineActive.value) {
      await list.loadMore()
      rows = sequence.value
    }
    // With a ticket open, move from that ticket; the cursor follows only once the move happens
    // (an unsaved-changes guard may keep the current ticket open).
    const from = ticketKey.value && panelItem.value ? panelItem.value.id : cursorId.value
    const index = rows.findIndex(row => row.id === from)
    const next = index === -1 ? (step > 0 ? 0 : rows.length - 1) : Math.max(0, Math.min(rows.length - 1, index + step))
    const row = rows[next]
    if (ticketKey.value) {
      openKey(row.key)
      if (!fullView.value && !panel.value?.el?.contains(document.activeElement)) table.value?.focusGrid()
      return
    }
    cursorId.value = row.id
    void nextTick(() => table.value?.scrollToRow(row.id))
    table.value?.focusGrid()
  }
  function keydown(event: KeyboardEvent) {
    if (event.altKey && event.key === 'ArrowLeft' && ticketKey.value && trail.value.length && !typing(event.target as HTMLElement | null)) { event.preventDefault(); trailBack(); return }
    // The Knowledge tab and its entries have their own keys.
    if ((knowledgeActive.value || settingsActive.value || deliveryActive.value) && !ticketKey.value) return
    // Command or Control A in the list selects every loaded row.
    if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'a' && selectable.value && !event.defaultPrevented
      && !typing(event.target) && !document.querySelector('dialog[open], .floating') && !panel.value?.el?.contains(document.activeElement)) {
      event.preventDefault(); selectAll(true); return
    }
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
    if (document.querySelector('dialog[open]')) return
    const target = event.target as HTMLElement | null
    if (target?.closest?.('.floating') || document.querySelector('.floating')) return
    if (typing(target)) {
      if (!graphActive.value && event.key === 'ArrowDown' && target === toolbar.value?.input) { event.preventDefault(); target.blur(); void move(cursorId.value ? 0 : 1) }
      return
    }
    if (graphActive.value && ['j', 'k', 'ArrowDown', 'ArrowUp', 'Enter', 'o', 'n'].includes(event.key)) return
    if ((knowledgeActive.value) && ['j', 'k', 'ArrowDown', 'ArrowUp', 'Enter', 'o', '/', 'n'].includes(event.key)) return
    const row = sequence.value.find(item => item.id === cursorId.value)
    if (event.key === 'F') { event.preventDefault(); toolbar.value?.openFilterMenu(); return }
    if (selectable.value && !panel.value?.el?.contains(document.activeElement)) {
      if (event.key === 'Escape' && (selected.value.size || phonePicking.value)) { event.preventDefault(); clearSelection(); return }
      if (!ticketKey.value) {
        if (event.key === 'x' && row) { event.preventDefault(); selectRow(row, 'toggle'); return }
        if (event.key === 'J' || (event.key === 'ArrowDown' && event.shiftKey)) { event.preventDefault(); selection.extendSelection(1, move); return }
        if (event.key === 'K' || (event.key === 'ArrowUp' && event.shiftKey)) { event.preventDefault(); selection.extendSelection(-1, move); return }
        const bulkKey = ({ s: 'status', a: 'assignee', p: 'priority', l: 'labels', m: 'move', g: 'release' } as const)[event.key as 's']
        if (selected.value.size && bulkKey) { event.preventDefault(); openBulk(bulkKey); return }
      }
    }
    if (outlineActive.value && !fullView.value && outlineKey(event, row)) return
    if (event.key === 'q') {
      event.preventDefault()
      if (ticketKey.value) panel.value?.toggleQueue()
      else if (selected.value.size) void bulkQueue()
      else if (row) document.querySelector<HTMLButtonElement>(`#row-${CSS.escape(row.id)} .q-btn`)?.click()
      return
    }
    switch (event.key) {
      case 'j': case 'ArrowDown': event.preventDefault(); void move(1); break
      case 'k': case 'ArrowUp': event.preventDefault(); void move(-1); break
      case 'Enter': case 'o':
        if (event.key === 'Enter' && target?.closest('button, a, summary')) return
        if (row) { event.preventDefault(); openRow(row) }
        break
      case 'Escape':
        if (creating.value || outline.createUnder.value) { event.preventDefault(); closeCreate() }
        else if (ticketKey.value) { event.preventDefault(); closePanel() }
        break
      case '/': if (!fullView.value) { event.preventDefault(); toolbar.value?.focusSearch() } break
      case 'n': event.preventDefault(); void startCreate(outlineActive.value && !ticketKey.value && (row?.is_leaf === false || row?.kind_slug === 'epic') ? row : null); break
      case 'u': if (activeLive.value.pill.value && liveActive.value) { event.preventDefault(); showUpdates() } break
      case 'e': if (ticketKey.value) { event.preventDefault(); void panel.value?.startEdit() } break
      case 's':
        if (ticketKey.value) { event.preventDefault(); panel.value?.openStatus() }
        else if (row) { const anchor = document.querySelector<HTMLElement>(`#row-${row.id} .status-btn`); if (anchor) { event.preventDefault(); openStatus(row, anchor, 'list') } }
        break
      case 'p': if (ticketKey.value) { event.preventDefault(); panel.value?.openPriority() } break
      case 'a': if (ticketKey.value) { event.preventDefault(); panel.value?.openAssignee() } else if (row) { const anchor = document.querySelector<HTMLElement>(`#row-${CSS.escape(row.id)} .assignee-btn`); if (anchor) { event.preventDefault(); openAssignee(row, anchor) } } break
      case 'r': if (ticketKey.value) { event.preventDefault(); panel.value?.openLink() } break
      case 'g': if (ticketKey.value) { event.preventDefault(); panel.value?.openRelease() } break
      case 'c': if (ticketKey.value) { event.preventDefault(); panel.value?.focusComposer() } break
      case 'f': if (ticketKey.value) { event.preventDefault(); if (fullView.value) collapse(); else expand() } break
    }
  }

  // Outline keys: right opens or steps into, left closes or steps out, Space toggles.
  function outlineKey(event: KeyboardEvent, row: ListItem | undefined): boolean {
    if (!['ArrowRight', 'ArrowLeft', ' '].includes(event.key) || !row) return false
    if (ticketKey.value && panel.value?.el?.contains(document.activeElement)) return false
    event.preventDefault()
    const open = outline.isExpanded(row.id), children = outline.hasChildren(row.id)
    const focusRow = (id: string | null) => {
      if (!id || !sequence.value.some(item => item.id === id)) return
      cursorId.value = id
      void nextTick(() => table.value?.scrollToRow(id))
    }
    if (event.key === ' ') { if (children) outline.toggle(row.id) }
    else if (event.key === 'ArrowRight') {
      if (children && !open) outline.setExpanded(row.id, true)
      else if (children) { const index = sequence.value.findIndex(item => item.id === row.id); const next = sequence.value[index + 1]; if (next && next.parent_id === row.id) focusRow(next.id) }
    } else {
      if (children && open) outline.setExpanded(row.id, false)
      else focusRow(outline.parentOf(row.id))
    }
    table.value?.focusGrid()
    return true
  }

  onMounted(() => window.addEventListener('keydown', keydown))
  onBeforeUnmount(() => window.removeEventListener('keydown', keydown))

  return { move, keydown }
}
