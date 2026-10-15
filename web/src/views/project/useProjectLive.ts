// SPDX-License-Identifier: AGPL-3.0-only

import { nextTick, onBeforeUnmount } from 'vue'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'
import type { useProjectPanel } from './useProjectPanel'

export function useProjectLive(state: ProjectState, data: ReturnType<typeof useProjectData>, pane: ReturnType<typeof useProjectPanel>) {
  const { projects, toolbarWrap, table, panel, scrollRoot, cursorId, fullView } = state
  const { list, activeLive, sequence } = data
  const { fetched, panelItem } = pane
  // ---------- Live updates: applying keeps the panel, cursor, scroll anchor and focus ----------
  // The first row still in view that stays, and where it is on screen.
  function scrollAnchor(removing: Set<string>) {
    const root = scrollRoot.value, grid = table.value?.el
    // At the very top nothing is out of view: new rows show where they belong.
    if (!root || !grid || root.scrollTop <= 0) return null
    const top = toolbarWrap.value?.getBoundingClientRect().bottom ?? 0
    for (const tr of grid.querySelectorAll<HTMLElement>('tr.ticket-row[id^="row-"]')) {
      const id = tr.id.slice(4)
      if (removing.has(id)) continue
      const rect = tr.getBoundingClientRect()
      if (rect.bottom > top) return { id, top: rect.top }
    }
    return null
  }
  function aroundLiveApply(removing: Set<string>, apply: () => void) {
    // The panel keeps its ticket when the row leaves the list.
    const open = panelItem.value
    if (open && removing.has(open.id)) fetched.value = open
    if (cursorId.value && removing.has(cursorId.value)) {
      const order = sequence.value
      const at = order.findIndex(row => row.id === cursorId.value)
      const next = order.slice(at + 1).find(row => !removing.has(row.id)) ?? order.slice(0, Math.max(0, at)).reverse().find(row => !removing.has(row.id))
      cursorId.value = next?.id ?? null
    }
    const active = document.activeElement
    const focusLeaves = !!active?.closest?.('tr.ticket-row') && removing.has(active.closest('tr.ticket-row')!.id.slice(4))
    const anchor = scrollAnchor(removing)
    apply()
    void nextTick(() => {
      const root = scrollRoot.value
      const row = anchor && document.getElementById(`row-${anchor.id}`)
      if (root && row) root.scrollTop += row.getBoundingClientRect().top - anchor!.top
      if (focusLeaves) table.value?.focusGrid()
    })
  }
  // Counts in the toolbar and the header follow, once a burst has settled.
  let liveCounts: ReturnType<typeof setTimeout> | undefined
  function liveApplied() {
    clearTimeout(liveCounts)
    liveCounts = setTimeout(() => { void list.refreshCounts(); void projects.load(true) }, 300)
  }
  onBeforeUnmount(() => clearTimeout(liveCounts))
  function showUpdates() {
    activeLive.value.apply()
    if (!fullView.value && !panel.value?.el?.contains(document.activeElement)) table.value?.focusGrid()
  }

  return { aroundLiveApply, liveApplied, showUpdates }
}
