// SPDX-License-Identifier: AGPL-3.0-only

import { nextTick, onBeforeUnmount, onMounted, watch } from 'vue'
import { type ListItem } from '../../lib/api'
import { ensurePermissions, permissionsKnown } from '../../lib/authz'
import { scopeOwner } from '../../lib/identityScope'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'
import type { useProjectPanel } from './useProjectPanel'
import type { useProjectSelection } from './useProjectSelection'
import type { useProjectActions } from './useProjectActions'
import type { useProjectLive } from './useProjectLive'

export function useProjectKeyboard(state: ProjectState, data: ReturnType<typeof useProjectData>, pane: ReturnType<typeof useProjectPanel>, selection: ReturnType<typeof useProjectSelection>, actions: ReturnType<typeof useProjectActions>, live: ReturnType<typeof useProjectLive>) {
  const { ticketKey, projectId, session, knowledgeActive, settingsActive, deliveryActive, graphActive, outlineActive, toolbar, table, panel, cursorId, creating, fullView, writable, commentable } = state
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
  // The title and the comment box can be on screen while this project's grants
  // are still in flight. One e or one c is kept for that ticket and that person.
  // Another ticket, person, field, dialog, or the other of those two keys spends it,
  // even when the person has left that field or closed that dialog again.
  type PendingShortcut = { key: string; projectId: string; owner: string; epoch: number }
  let editEpoch = 0
  let pendingEdit: PendingShortcut | null = null
  let commentEpoch = 0
  let pendingComment: PendingShortcut | null = null
  function clearPendingEdit() {
    if (!pendingEdit) return
    pendingEdit = null
    editEpoch++
    stopInterruptions()
  }
  function clearPendingComment() {
    if (!pendingComment) return
    pendingComment = null
    commentEpoch++
    stopInterruptions()
  }
  // Checking for a field or dialog only when grants arrive misses one the person
  // has already left. While a key waits, entering a field or opening a dialog spends it.
  const blockers = 'dialog[open], .floating'
  let interruptions: MutationObserver | null = null
  function interrupted() { clearPendingEdit(); clearPendingComment() }
  function fieldEntered(event: FocusEvent) { if (typing(event.target)) interrupted() }
  function dialogOpened(records: MutationRecord[]) {
    const opened = !!document.querySelector(blockers) || records.some(record => record.type === 'attributes'
      ? record.target instanceof HTMLDialogElement
      : [...record.addedNodes].some(node => node instanceof Element && (node.matches(blockers) || !!node.querySelector(blockers))))
    if (opened) interrupted()
  }
  function watchInterruptions() {
    if (interruptions) return
    document.addEventListener('focusin', fieldEntered, true)
    interruptions = new MutationObserver(dialogOpened)
    interruptions.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['open'] })
  }
  function stopInterruptions() {
    if (!interruptions || pendingEdit || pendingComment) return
    document.removeEventListener('focusin', fieldEntered, true)
    interruptions.disconnect()
    interruptions = null
  }
  function samePersonTicket(pending: PendingShortcut) {
    return ticketKey.value === pending.key && projectId.value === pending.projectId
      && scopeOwner(session.identity) === pending.owner && session.authenticationCurrent()
  }
  function shortcutBlocked() {
    return typing(document.activeElement) || !!document.querySelector('dialog[open], .floating')
  }
  // The workspace mounts before its ticket loads, and startEdit does nothing
  // without one. e waits for the ticket it was pressed on.
  function ticketShown(pending: PendingShortcut) {
    return panelItem.value?.key.toLowerCase() === pending.key.toLowerCase()
  }
  function openRememberedEdit(pending: PendingShortcut) {
    const workspace = panel.value
    if (pending.epoch !== editEpoch || pendingEdit !== pending || !workspace?.startEdit || !ticketShown(pending) || shortcutBlocked()) return false
    clearPendingEdit()
    void workspace.startEdit()
    return true
  }
  async function applyPendingEdit(pending: PendingShortcut) {
    if (pending.epoch !== editEpoch || pendingEdit !== pending) return
    if (!samePersonTicket(pending)) { clearPendingEdit(); return }
    if (!permissionsKnown(pending.projectId)) return
    // Known and not writable is a reader, or an answer that never arrived.
    // Either way the key is spent: a later grant must not open an editor.
    if (!writable.value) { clearPendingEdit(); return }
    await nextTick()
    if (pending.epoch !== editEpoch || pendingEdit !== pending) return
    if (!samePersonTicket(pending) || !writable.value || shortcutBlocked()) { clearPendingEdit(); return }
    // The workspace can still be mounting or loading its ticket, or it remounted and dropped its own wait.
    if (!panel.value?.startEdit || !ticketShown(pending)) return
    openRememberedEdit(pending)
  }
  function commentField(): HTMLTextAreaElement | null {
    const root = panel.value?.el
    if (!(root instanceof HTMLElement)) return null
    const field = root.querySelector('textarea[aria-label="Add a comment"]')
    return field instanceof HTMLTextAreaElement ? field : null
  }
  // The comment box can be on screen and disabled while grants are in flight.
  // Focusing it then does nothing. One c is kept until that box can take the
  // caret. A reader, another shortcut, or a ticket they have left spends it,
  // so a later grant does not focus the box on its own.
  function openRememberedComment(pending: PendingShortcut) {
    if (pending.epoch !== commentEpoch || pendingComment !== pending || shortcutBlocked()) return false
    const field = commentField()
    if (!field || field.disabled) return false
    // The focus enters a field, which already spends this c; clearing again changes nothing.
    panel.value?.focusComposer()
    if (document.activeElement !== field) return false
    clearPendingComment()
    return true
  }
  async function applyPendingComment(pending: PendingShortcut) {
    if (pending.epoch !== commentEpoch || pendingComment !== pending) return
    if (!samePersonTicket(pending)) { clearPendingComment(); return }
    if (!permissionsKnown(pending.projectId)) return
    if (!commentable?.value) { clearPendingComment(); return }
    await nextTick()
    if (pending.epoch !== commentEpoch || pendingComment !== pending) return
    if (!samePersonTicket(pending) || !commentable?.value || shortcutBlocked()) { clearPendingComment(); return }
    const field = commentField()
    if (!field) return
    if (field.disabled) { clearPendingComment(); return }
    openRememberedComment(pending)
  }
  function rememberComment() {
    clearPendingEdit()
    const key = ticketKey.value
    const id = projectId.value
    const owner = scopeOwner(session.identity)
    if (!key || !id || !owner || !session.authenticationCurrent()) return
    if (permissionsKnown(id) && !commentable?.value) return
    const pending: PendingShortcut = { key, projectId: id, owner, epoch: ++commentEpoch }
    pendingComment = pending
    watchInterruptions()
    if (permissionsKnown(id) && commentable?.value) {
      if (!openRememberedComment(pending)) void applyPendingComment(pending)
      return
    }
    void ensurePermissions(id).then(() => { void applyPendingComment(pending) })
  }
  function rememberEdit() {
    clearPendingComment()
    const key = ticketKey.value
    const id = projectId.value
    const owner = scopeOwner(session.identity)
    if (!key || !id || !owner || !session.authenticationCurrent()) return
    if (permissionsKnown(id) && !writable.value) return
    const pending: PendingShortcut = { key, projectId: id, owner, epoch: ++editEpoch }
    pendingEdit = pending
    watchInterruptions()
    // Grants already on screen: edit in this turn, as a press after the button exists does.
    if (permissionsKnown(id) && writable.value) {
      if (!openRememberedEdit(pending)) void applyPendingEdit(pending)
      return
    }
    void ensurePermissions(id).then(() => { void applyPendingEdit(pending) })
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
      case 'e': if (ticketKey.value) { event.preventDefault(); rememberEdit() } break
      case 's':
        if (ticketKey.value) { event.preventDefault(); panel.value?.openStatus() }
        else if (row) { const anchor = document.querySelector<HTMLElement>(`#row-${CSS.escape(row.id)} .status-btn, #row-${CSS.escape(row.id)} .phone-state`); if (anchor) { event.preventDefault(); openStatus(row, anchor, 'list') } }
        break
      case 'p': if (ticketKey.value) { event.preventDefault(); panel.value?.openPriority() } break
      case 'a': if (ticketKey.value) { event.preventDefault(); panel.value?.openAssignee() } else if (row) { const anchor = document.querySelector<HTMLElement>(`#row-${CSS.escape(row.id)} .assignee-btn`); if (anchor) { event.preventDefault(); openAssignee(row, anchor) } } break
      case 'r': if (ticketKey.value) { event.preventDefault(); panel.value?.openLink() } break
      case 'g': if (ticketKey.value) { event.preventDefault(); panel.value?.openRelease() } break
      case 'c': if (ticketKey.value) { event.preventDefault(); rememberComment() } break
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

  watch(panel, () => {
    const edit = pendingEdit, comment = pendingComment
    if (edit) void applyPendingEdit(edit)
    if (comment) void applyPendingComment(comment)
  })
  watch(panelItem, () => {
    const edit = pendingEdit, comment = pendingComment
    if (edit) void applyPendingEdit(edit)
    if (comment) void applyPendingComment(comment)
  })
  // A refresh can replace the request a key waits on. The answer that lands then
  // may change no grant at all, so arriving grants settle the key as well.
  const grantsKnown = () => !!projectId.value && permissionsKnown(projectId.value)
  watch([grantsKnown, writable], () => { const pending = pendingEdit; if (pending) void applyPendingEdit(pending) })
  watch([grantsKnown, () => commentable?.value ?? false], () => { const pending = pendingComment; if (pending) void applyPendingComment(pending) })
  watch(ticketKey, key => {
    if (pendingEdit && key !== pendingEdit.key) clearPendingEdit()
    if (pendingComment && key !== pendingComment.key) clearPendingComment()
  })
  watch(projectId, id => {
    if (pendingEdit && id !== pendingEdit.projectId) clearPendingEdit()
    if (pendingComment && id !== pendingComment.projectId) clearPendingComment()
  })
  watch(() => scopeOwner(session.identity), () => {
    if (pendingEdit) clearPendingEdit()
    if (pendingComment) clearPendingComment()
  })
  onMounted(() => window.addEventListener('keydown', keydown))
  onBeforeUnmount(() => { clearPendingEdit(); clearPendingComment(); window.removeEventListener('keydown', keydown) })

  return { move, keydown }
}
