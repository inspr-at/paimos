<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, watch } from 'vue'
import { useRoute } from 'vue-router'
import { leadOverlay, resetLeadOverlays } from '../../lib/leadOverlay'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
// Lead controls and their session state belong to the opened overlay, not
// every cold app page. All three share the same on-demand loading boundary.
const LeadPanel = defineAsyncComponent(() => import('./LeadPanel.vue'))
const LeadPauseSheet = defineAsyncComponent(() => import('./LeadPauseSheet.vue'))
const StartLeadSheet = defineAsyncComponent(() => import('./StartLeadSheet.vue'))

// The app shell's one host for the docked lead panel and the lead sheets, so a
// ticket preview over any page can open them. Leaving the view that opened an
// overlay closes it, as before; moving within that view keeps it.
const projects = useProjects(), session = useSession(), route = useRoute()
const panelProject = computed(() => leadOverlay.panel ? projects.byId(leadOverlay.panel) ?? null : null)
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, () => resetLeadOverlays())
watch(() => route.matched[0], () => resetLeadOverlays())
onBeforeUnmount(resetLeadOverlays)
</script>

<template>
  <LeadPanel v-if="panelProject" :key="panelProject.id" :project-id="panelProject.id" :project-key="panelProject.routeKey" :route-key="panelProject.routeKey" />
  <StartLeadSheet v-if="leadOverlay.start" :key="leadOverlay.start.projects.join()" :projects="leadOverlay.start.projects" :show-project="leadOverlay.start.showProject" />
  <LeadPauseSheet v-else-if="leadOverlay.pause" :key="leadOverlay.pause" :project-id="leadOverlay.pause" />
</template>
