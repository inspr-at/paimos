<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { leadOverlay, resetLeadOverlays } from '../../lib/leadOverlay'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import LeadPanel from './LeadPanel.vue'
import LeadPauseSheet from './LeadPauseSheet.vue'
import StartLeadSheet from './StartLeadSheet.vue'

// Hosts the docked lead panel and the lead sheets for the view that mounts it.
const projects = useProjects(), session = useSession()
const panelProject = computed(() => leadOverlay.panel ? projects.byId(leadOverlay.panel) ?? null : null)
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, () => resetLeadOverlays())
onBeforeUnmount(resetLeadOverlays)
</script>

<template>
  <LeadPanel v-if="panelProject" :key="panelProject.id" :project-id="panelProject.id" :project-key="panelProject.routeKey" :route-key="panelProject.routeKey" />
  <StartLeadSheet v-if="leadOverlay.start" :key="leadOverlay.start.projects.join()" :projects="leadOverlay.start.projects" :show-project="leadOverlay.start.showProject" />
  <LeadPauseSheet v-else-if="leadOverlay.pause" :key="leadOverlay.pause" :project-id="leadOverlay.pause" />
</template>
