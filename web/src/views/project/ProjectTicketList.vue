<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import { can } from '../../lib/authz'
import TicketTable from '../../components/work/TicketTable.vue'
import LiveUpdatesChip from '../../components/work/LiveUpdatesChip.vue'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const props = defineProps<{ project: Project }>()
const { project } = toRefs(props)
const { openAssignee, copyKey, openStatus, openEpic, quickCreate, closeCreate, moveRow, tablePrefs, tableLayout, usageVisible, saveWidths, list, outline, activeLive, liveActive, liveDataStale, rowsById, groups, total, showAssignee, expectedRows, knownStates, filtered, manualShowClosed, clearFilters, sortBy, toggleGroup, setAllGroups, flipGroups, groupFigures, showUpdates, panelItem, nativeReleases, openRow, newTab, selectable, selected, phonePicking, selectRow, selectAll, openRelease, routeKey, filters, density, now, outlineActive, table, scrollRoot, cursorId, collapsed, creating, writable, columnFigures, focusFirst } = useProjectPageContext()
const bindTable = bindProjectRef(table)
</script>

<template>

  <LiveUpdatesChip v-if="liveActive && activeLive.pill.value" :text="activeLive.pill.value" :overflow="activeLive.pending.overflow" @show="showUpdates" />
  <p v-if="liveActive" class="sr-only live-said" role="status" aria-live="polite">{{ activeLive.message.value }}</p>
  <TicketTable
    :ref="bindTable" :expected-rows="expectedRows" :groups="groups" :group="filters.group" :rows-by-id="rowsById" :cursor-id="cursorId" :open-id="panelItem?.id ?? null"
    :query="filters.q" :sort="filters.sort" :density="density"
    :loading="outlineActive ? outline.loading.value : list.loading.value" :loading-more="outlineActive ? outline.loadingMoreRoot.value : list.loadingMore.value"
    :error="outlineActive ? outline.error.value || list.error.value : list.error.value" :more-error="list.moreError.value"
    :has-more="outlineActive ? outline.hasMoreRoot.value : !!list.cursor.value" :filtered="filtered" :hiding-closed="!filters.showClosed"
    :collapsed="collapsed" :total="total" :project-key="routeKey" :scroll-root="scrollRoot" :now="now" :show-assignee="showAssignee"
    :creating="creating" :project-id="project.id" :known-states="knownStates" :create="quickCreate" @close-create="closeCreate"
    :outline="outlineActive ? outline.entries.value : null" :can-drag="outlineActive && writable" :prefs="tablePrefs" :cost-allowed="usageVisible"
    :selectable="selectable" :selected="selected" :picking="phonePicking" :can-assign-release="can('releases.write', project.id)" :native-releases="nativeReleases" @select="selectRow" @select-all="selectAll" :column-figures="columnFigures"
    :group-desc="!!filters.groupDesc" :group-figures="filters.group !== 'none' && !outlineActive ? groupFigures : null" @all-groups="setAllGroups" @flip-groups="flipGroups"
    @layout="(visible, customised) => tableLayout = { visible, customised }" @widths="saveWidths"
    @toggle-row="outline.toggle" @toggle-no-epic="outline.noEpicCollapsed.value = !outline.noEpicCollapsed.value"
    @more-children="id => id === project!.id ? outline.loadMoreRoot() : outline.loadChildren(id, true)" @move="moveRow"
    @open="openRow" @cursor="id => cursorId = id" @sort="sortBy" @status="(row, anchor) => openStatus(row, anchor, 'list')" @release="(row, anchor) => openRelease(anchor, [row.id])"
    @assignee="openAssignee" @copy="row => copyKey(row.key)" @new-tab="row => newTab(row.key)" @toggle-group="toggleGroup" @open-epic="openEpic"
    @retry="outlineActive ? outline.reload() : list.load()" @more="outlineActive ? outline.loadMoreRoot() : list.loadMore()" @grid-focus="focusFirst" @clear-filters="clearFilters" @show-closed="manualShowClosed(true)"
    :live-labels="liveActive ? activeLive.labels.value : undefined" :live-flash="liveActive ? activeLive.flash.value : undefined"
    :live-stale="liveDataStale"
    :live-pill="liveActive && activeLive.pill.value ? { text: activeLive.pill.value, overflow: activeLive.pending.overflow } : null" @show-updates="showUpdates"
  />
</template>
