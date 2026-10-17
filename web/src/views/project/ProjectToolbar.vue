<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import ListToolbar from '../../components/work/ListToolbar.vue'
import HeaderNavMenu from '../../components/work/HeaderNavMenu.vue'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const props = defineProps<{ project: Project }>()
const { project } = toRefs(props)
const { startCreate, toolbarColumns, saveColumns, resetColumns, list, outline, sequence, total, options, chipLabel, facetLoading, needOptions, update, setHideStates, manualShowClosed, setSection, setView, toggleValue, excludeValue, clearFilters, setDate, setSort, setGroup, setAllGroups, selectable, selected, phonePicking, picking, clearSelection, selectedDeleted, projectSections, section, headerDensity, ticketsHeader, filters, density, setDensity, knowledgeView, ticketSwitch, knowledgeSwitch, viewMode, settingsActive, deliveryActive, graphActive, graphState, toolbarWrap, stickMark, toolbar, filterSheet, stuck, me, views, activeView, viewDirty, canSaveView, defaultViewId, hrefFor, openView, viewBar, startSave } = useProjectPageContext()
const bindStickMark = bindProjectRef(stickMark)
const bindToolbar = bindProjectRef(toolbar)
const bindToolbarWrap = bindProjectRef(toolbarWrap)
</script>

<template>
  <div :ref="bindStickMark" class="stick-mark" aria-hidden="true" />
  <div v-if="!settingsActive && !deliveryActive" :ref="bindToolbarWrap" class="toolbar-wrap" :class="{ stuck }">
    <ListToolbar
      :ref="bindToolbar" :summary="project" :filters="filters" :options="options" :label="chipLabel" :total="total" :loading="graphActive ? graphState.loading : list.loading.value" :density="density" :stuck="stuck"
      :facet-loading="facetLoading" :settings-target="ticketsHeader ? '#project-view-settings' : undefined" :project-header="ticketsHeader" :collapsed-header="ticketsHeader && headerDensity === 'collapsed'"
      @search="q => update({ q })" @toggle="toggleValue" @exclude="excludeValue" @clear="dimension => update({ [dimension]: [] })" @clear-all="clearFilters"
      @show-closed="manualShowClosed" @hide-states="setHideStates" @group="setGroup" @sort="setSort" @density="setDensity" @date="setDate"
      @open-sheet="filterSheet?.open()" @need-options="needOptions" @create="startCreate()"
      :view="viewMode === 'settings' || viewMode === 'delivery' ? 'list' : viewMode" :knowledge-view="knowledgeView" @view="setView"
      :default-view="viewMode === 'knowledge' ? knowledgeSwitch.defaultId.value : ticketSwitch.defaultId.value" @set-default="value => (viewMode === 'knowledge' ? knowledgeSwitch : ticketSwitch).setDefault(value)" @expand-all="outline.expandAll()" @collapse-all="outline.collapseAll()"
      @expand-groups="setAllGroups(true)" @collapse-groups="setAllGroups(false)"
      :columns="toolbarColumns" @columns="saveColumns" @columns-reset="resetColumns"
    >
      <!-- Collapsed header: the sections and saved views it hides, at the top of the Display menu. -->
      <template #header-nav="{ anchor, close }">
    <HeaderNavMenu
      :sections="projectSections" :section="section" :attention="{ path: '/tickets', query: { view: 'needs-attention', project_id: project.id } }"
      :views="views.items" :active-id="activeView?.id ?? null" :dirty="viewDirty" :default-id="defaultViewId" :me="me?.id ?? null" :href-for="hrefFor" :can-save-new="canSaveView && !activeView"
      @section="id => { close(false); setSection(id) }" @attention="close(false)" @view="id => { close(true); openView(id) }"
      @view-menu="close(false); viewBar?.openMenuFor(anchor)" @save-as="close(false); startSave(anchor)"
    />
      </template>
    </ListToolbar>
    <div v-if="selectable && (sequence.length || picking)" class="phone-pick" :class="{ on: picking }">
      <p v-if="picking" class="phone-pick-status">
    <span aria-live="polite"><b class="mono">{{ selected.size.toLocaleString('en-GB') }}</b> selected</span>
    <template v-if="selectedDeleted.length"><span class="dot" aria-hidden="true">·</span><span class="gone">{{ selectedDeleted.length.toLocaleString('en-GB') }} deleted</span></template>
    <span class="dot" aria-hidden="true">·</span>
    <button type="button" @click="clearSelection">Cancel</button>
      </p>
      <button v-else type="button" class="quiet" @click="phonePicking = true">Select</button>
    </div>
  </div>

</template>
