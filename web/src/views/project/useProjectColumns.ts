// SPDX-License-Identifier: AGPL-3.0-only

import { computed, ref } from 'vue'
import { can } from '../../lib/authz'
import { planningPresent } from '../../lib/planning'
import { orderOf, pickerColumns, PINNED, type ColumnId, type ListPrefs } from '../../lib/columns'
import { usePreference } from '../../lib/preferences'
import { type ListFilters } from '../../lib/ticketList'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'

export function useProjectColumns(state: ProjectState, data: ReturnType<typeof useProjectData>, update: (patch: Partial<ListFilters>) => void) {
  const { projectId, filters } = state
  const { rowsById } = data
  // ---------- Columns: the person's order, visibility and widths for this project ----------
  const listPref = computed(() => projectId.value ? usePreference<ListPrefs>(`list:${projectId.value}`) : null)
  const listPrefs = computed(() => listPref.value?.value.value ?? null)
  // A view (or a shared link) may carry its own column set; widths stay the person's.
  const tablePrefs = computed<ListPrefs | null>(() => {
    const cols = filters.value.cols
    if (!cols) return listPrefs.value
    const rest = orderOf(listPrefs.value).filter(id => !PINNED.includes(id) && !cols.includes(id))
    return { ...(listPrefs.value ?? {}), order: [...PINNED, ...cols, ...rest], visible: cols }
  })
  const tableLayout = ref<{ visible: ColumnId[]; customised: boolean }>({ visible: [], customised: false })
  // Cost is offered only to people who may see usage (harness.read).
  const usageVisible = computed(() => can('harness.read', projectId.value ?? undefined))
  const toolbarColumns = computed(() => {
    const columns = pickerColumns(tablePrefs.value, tableLayout.value.visible, usageVisible.value)
    const notes = columns.visible.includes('list_cost') && !planningPresent([...rowsById.value.values()]).list_cost
      ? { list_cost: 'Nothing reported in this list yet; cells show —' } : {}
    return { ...columns, notes }
  })
  // In a saved view the columns belong to the view (they become part of the list's
  // state); on the plain list they are the person's own for this project.
  function saveColumns(order: ColumnId[], visible: ColumnId[]) {
    if (filters.value.view || filters.value.cols) { update({ cols: order.filter(id => visible.includes(id) && !PINNED.includes(id)) }); return }
    listPref.value?.save({ ...(listPrefs.value ?? {}), order, visible }, 0)
  }
  function resetColumns() {
    if (filters.value.cols) { update({ cols: null }); return }
    const { order: _order, visible: _visible, ...rest } = listPrefs.value ?? {}
    listPref.value?.save(rest, 0)
  }
  function saveWidths(widths: Partial<Record<ColumnId, number>>) { listPref.value?.save({ ...(listPrefs.value ?? {}), widths }) }

  return { listPref, listPrefs, tablePrefs, tableLayout, usageVisible, toolbarColumns, saveColumns, resetColumns, saveWidths }
}
