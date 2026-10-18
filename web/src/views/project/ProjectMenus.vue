<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import { plural } from '../../lib/work'
import AssigneeMenu from '../../components/work/AssigneeMenu.vue'
import QueueView from '../../components/work/QueueView.vue'
import FilterSheet from '../../components/work/FilterSheet.vue'
import StatusMenu from '../../components/work/StatusMenu.vue'
import SaveViewPanel from '../../components/work/SaveViewPanel.vue'
import LabelMenu from '../../components/work/LabelMenu.vue'
import OptionMenu from '../../components/work/OptionMenu.vue'
import EpicPicker from '../../components/work/EpicPicker.vue'
import ReleasePicker from '../../components/work/ReleasePicker.vue'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const props = defineProps<{ project: Project }>()
const { project } = toRefs(props)
const { closeAssignee, assignPerson, closeStatus, chooseStatus, list, total, knownStates, options, sheetLoading, sheetOpened, setHideStates, manualShowClosed, toggleValue, excludeValue, clearFilters, setDate, openKey, selected, selectedRows, bulkBusy, bulkMenu, releaseIds, closeBulk, bulkPeople, bulkPriorities, bulkLabels, bulkStatus, bulkAssign, bulkPriority, bulkLabelsApply, bulkMove, chooseRelease, projectId, queueAnchor, assigneeMenu, closeQueue, ticketsHeader, filters, graphActive, outlineActive, toolbar, filterSheet, statusMenu, writable, canSaveView, savePanel, saveBusy, saveError, viewBar, startSave, closeSave, submitSave } = useProjectPageContext()
const bindFilterSheet = bindProjectRef(filterSheet)
</script>

<template>
  <StatusMenu :project-id="projectId ?? undefined" v-if="statusMenu" :anchor="statusMenu.anchor" :derived="statusMenu.row.status_derived" :children-count="statusMenu.row.work_children_count" :node-id="statusMenu.row.id" :revision="statusMenu.row.updated_at" :current="statusMenu.row.state" :known-states="knownStates" :ticket-key="statusMenu.row.key" @choose="chooseStatus" @close="closeStatus" />
  <FilterSheet
    :ref="bindFilterSheet" :summary="project" :filters="filters" :options="options" :loading="sheetLoading" :total="total" :view="graphActive ? 'graph' : outlineActive ? 'outline' : 'list'" :can-save="!graphActive && canSaveView"
    :project-header="ticketsHeader"
    @toggle="toggleValue" @exclude="excludeValue" @clear-all="clearFilters" @show-closed="manualShowClosed" @hide-states="setHideStates" @date="setDate"
    @opened="sheetOpened" @save-view="anchor => startSave(viewBar?.$el?.offsetParent ? viewBar.$el : anchor)" @display="toolbar?.openDisplay()"
  />
  <SaveViewPanel
    v-if="savePanel" :key="`${savePanel.mode}-${savePanel.view?.id ?? 'new'}`" :anchor="savePanel.anchor" :mode="savePanel.mode" :name="savePanel.name" :project-title="project.title"
    :busy="saveBusy" :error="saveError" @submit="submitSave" @close="closeSave"
  />
  <QueueView v-if="queueAnchor" :project-id="project.id" :anchor="queueAnchor" :filtered="filters.status.includes('queued')" @close="closeQueue" @open="key => { closeQueue(false); openKey(key) }" @filter="toggleValue('status', 'queued')" />
  <AssigneeMenu v-if="assigneeMenu" :row="assigneeMenu.row" :project-id="project.id" :anchor="assigneeMenu.anchor" :people="bulkPeople" :can-assign="writable" @choose="assignPerson" @close="closeAssignee" @changed="list.load()" />
  <StatusMenu :project-id="projectId ?? undefined" v-if="bulkMenu?.kind === 'status'" :anchor="bulkMenu.anchor" current="" :known-states="knownStates" :ticket-key="plural(selected.size, 'ticket')" @choose="bulkStatus" @close="closeBulk" />
  <OptionMenu
    v-else-if="bulkMenu?.kind === 'assignee'" :anchor="bulkMenu.anchor" title="Assignee" :subject="plural(selected.size, 'ticket')" kind="assignee" :options="bulkPeople" current="-" :searchable="bulkPeople.length > 8"
    @choose="bulkAssign" @close="closeBulk"
  />
  <OptionMenu v-else-if="bulkMenu?.kind === 'priority'" :anchor="bulkMenu.anchor" title="Priority" :subject="plural(selected.size, 'ticket')" kind="priority" :options="bulkPriorities" current="-" @choose="bulkPriority" @close="closeBulk" />
  <LabelMenu v-else-if="bulkMenu?.kind === 'labels'" :anchor="bulkMenu.anchor" :labels="bulkLabels" :count="selectedRows.length" :busy="bulkBusy" @apply="bulkLabelsApply" @close="closeBulk" />
  <EpicPicker v-else-if="bulkMenu?.kind === 'move'" :anchor="bulkMenu.anchor" :project-id="project.id" current="-" :subject="plural(selected.size, 'ticket')" allow-none @choose="bulkMove" @close="closeBulk" />
  <ReleasePicker v-else-if="bulkMenu?.kind === 'release'" :anchor="bulkMenu.anchor" :project-id="project.id" :subject="plural(releaseIds.length, 'ticket')" @choose="chooseRelease" @close="closeBulk" />
</template>
