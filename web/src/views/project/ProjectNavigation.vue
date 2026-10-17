<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import AppIcon from '../../components/AppIcon.vue'
import ViewSwitch from '../../components/ViewSwitch.vue'
import ViewBar from '../../components/work/ViewBar.vue'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const props = defineProps<{ project: Project }>()
const { project } = toRefs(props)
const { setSection, sectionSwitch, projectSections, section, ticketsHeader, me, views, activeView, viewDirty, canSaveView, showViewBar, defaultViewId, hrefFor, openView, viewBar, startSave, startRename, saveActive, duplicate, setDefaultView, share, copyViewLink, remove } = useProjectPageContext()
const bindViewBar = bindProjectRef(viewBar)
</script>

<template>
  <div class="project-navigation" :class="{ 'legacy-navigation': !ticketsHeader }">
    <ViewSwitch :options="projectSections" :selected="section" label="Project sections" look="sections" tips :default-id="sectionSwitch.defaultId.value" @select="setSection" @set-default="sectionSwitch.setDefault" />
    <RouterLink v-if="ticketsHeader" class="attention-view-link" :to="{ path: '/tickets', query: { view: 'needs-attention', project_id: project.id } }"><AppIcon name="flag" :size="13" />Needs attention</RouterLink>
    <span v-if="ticketsHeader" class="nav-divider" aria-hidden="true" />
  <ViewBar
    v-if="ticketsHeader || showViewBar" :ref="bindViewBar" :views="views.items" :active-id="activeView?.id ?? null" :dirty="viewDirty" :default-id="defaultViewId" :can-save-new="canSaveView && !activeView"
    :me="me?.id ?? null" :href-for="hrefFor" @open="id => openView(id)" @save="saveActive" @save-as="startSave" @reset="openView(activeView?.id ?? null, true)"
    @rename="startRename" @duplicate="duplicate" @set-default="setDefaultView" @share="share" @copy-link="copyViewLink" @remove="remove"
  />
    <div v-if="ticketsHeader" id="project-view-settings" class="project-view-settings" />
  </div>
</template>
