<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { provide } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import LeadBand from '../components/lead/LeadBand.vue'
import { run } from '../lib/commands'
import { useProjectPage } from './project/useProjectPage'
import { PROJECT_PAGE } from './project/context'
import ProjectHeader from './project/ProjectHeader.vue'
import ProjectNavigation from './project/ProjectNavigation.vue'
import ProjectToolbar from './project/ProjectToolbar.vue'
import ProjectSections from './project/ProjectSections.vue'
import ProjectBulkBar from './project/ProjectBulkBar.vue'
import ProjectDetailHost from './project/ProjectDetailHost.vue'
import ProjectMenus from './project/ProjectMenus.vue'

const page = useProjectPage()
provide(PROJECT_PAGE, page)
const { project, projects, projectKey, ticketsHeader, headerDensity, ticketKey, fullView,
  knowledgeDocked, knowledgeEntryOpen, toolbarHeight, selectable, selected, liveActive, activeLive,
  bulkHeight, knowledgeActive, settingsActive, deliveryActive, graphActive } = page
</script>

<template>
  <section class="project-page" :class="[ticketsHeader ? `header-${headerDensity}` : '', { 'panel-open': (!!ticketKey && !fullView) || knowledgeDocked, 'ticket-dock': !!ticketKey && !fullView, 'full-view': fullView, 'knowledge-entry': knowledgeEntryOpen, 'knowledge-dock': knowledgeDocked }]" :style="{ '--toolbar-h': `${toolbarHeight}px` }" :aria-labelledby="project && !knowledgeEntryOpen ? 'project-title' : undefined">
    <template v-if="project">
      <div v-show="!fullView && !knowledgeEntryOpen" class="list-view" :class="{ selecting: selectable && selected.size, 'has-live-updates': liveActive && activeLive.pill.value }" :style="{ '--live-obstacle-h': `${bulkHeight ? bulkHeight + 8 : 0}px` }">
      <div id="project-header-fold" v-show="!ticketsHeader || headerDensity !== 'collapsed'" class="project-fold">
      <ProjectHeader :project="project" />
      <LeadBand :project-id="project.id" :project-key="project.routeKey" :route-key="project.routeKey" />
      <ProjectNavigation :project="project" />
      </div>
      <ProjectToolbar :project="project" />
      <ProjectSections :project="project" />
      <ProjectBulkBar :project="project" />
      <p v-if="!knowledgeActive && !settingsActive && !deliveryActive && !graphActive" class="hint">
        <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <kbd class="keycap"><AppIcon name="enter" /></kbd> open · <kbd class="keycap">/</kbd> search ·
        <button type="button" class="hint-link" @click="run({ name: 'shortcuts' })"><kbd class="keycap">?</kbd> all shortcuts</button>
      </p>
      </div>

      <ProjectDetailHost :project="project" />
      <ProjectMenus :project="project" />
    </template>

    <div v-else-if="projects.error && !projects.loaded" class="page-state" role="alert">
      <AppIcon name="alert" :size="20" />
      <h1>Projects could not be loaded</h1>
      <p>{{ projects.error }}</p>
      <button type="button" class="btn" @click="projects.load(true)">Try again</button>
    </div>
    <div v-else-if="projects.loaded" class="page-state">
      <AppIcon name="folder" :size="20" />
      <h1>No project called {{ projectKey.toUpperCase() }}</h1>
      <p>It may have been renamed or archived.</p>
      <RouterLink class="btn" to="/"><AppIcon name="arrow" :size="14" />All projects</RouterLink>
    </div>
    <div v-else class="head-skeleton" role="status" aria-label="Loading project">
      <span class="skeleton sk-a" /><span class="skeleton sk-b" /><span class="skeleton sk-c" />
    </div>
  </section>
</template>

<style scoped src="./project/projectPage.css" />
