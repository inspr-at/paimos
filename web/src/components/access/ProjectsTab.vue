<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useAccess } from '../../stores/access'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import ProjectAccess from './ProjectAccess.vue'

// Project access: every project, with how many people and agents have a role on
// it. /settings/access/projects/<id> shows who can work on it and through what.
const props = defineProps<{ detail?: string }>()
const access = useAccess()
const projects = useProjects()
const term = ref('')
const project = computed(() => props.detail ? projects.byId(props.detail) ?? null : null)
const bound = (id: string) => [...access.people.filter(p => p.status === 'active'), ...access.agents].filter(p => p.project_roles?.some(r => r.project_id === id)).length
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return projects.projects.filter(p => !needle || `${p.routeKey} ${p.title}`.toLowerCase().includes(needle)).sort((a, b) => Number(a.archived) - Number(b.archived) || a.title.localeCompare(b.title))
})
const roleNames = (id: string) => [...new Set(access.people.flatMap(p => p.project_roles.filter(r => r.project_id === id).map(r => r.role.name)))].join(', ')
onMounted(() => { void projects.load() })
</script>

<template>
  <ProjectAccess v-if="project" :key="project.id" :project="project" />
  <div v-else-if="detail && projects.loaded" class="gone">
    <p>This project is not here any more.</p>
    <RouterLink class="btn sm" to="/settings/access/projects"><AppIcon name="arrow-left" :size="13" />All projects</RouterLink>
  </div>
  <div v-else-if="detail" class="set-skeleton" role="status" aria-label="Loading project"><span class="skeleton" /><span class="skeleton" /></div>
  <div v-else class="projects-tab">
    <div class="list-tools">
      <label class="search-field find">
        <AppIcon name="search" :size="14" />
        <input v-model="term" class="field" type="search" placeholder="Find a project" aria-label="Find a project" autocomplete="off" spellcheck="false" />
      </label>
      <p class="lead">Everyone with a workspace role reaches every project with it. A project role adds access to one project, for example for a guest.</p>
    </div>
    <div v-if="!projects.loaded" class="set-skeleton" role="status" aria-label="Loading projects"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
    <ul v-else class="projects">
      <li v-for="p in shown" :key="p.id">
        <RouterLink class="project" :to="`/settings/access/projects/${p.id}`">
          <span class="key-badge">{{ p.routeKey }}</span>
          <span class="p-text"><span class="p-title">{{ p.title }}<span v-if="p.archived" class="archived">Archived</span></span><span class="p-roles">{{ roleNames(p.id) ? `Project roles: ${roleNames(p.id)}` : 'Workspace roles only' }}</span></span>
          <span class="p-count">{{ bound(p.id) === 1 ? '1 with a project role' : `${bound(p.id)} with a project role` }}</span>
          <AppIcon name="chevron-right" :size="14" class="go" />
        </RouterLink>
      </li>
    </ul>
    <p v-if="projects.loaded && !shown.length" class="empty">{{ term ? `No project matches “${term}”.` : 'No projects yet.' }}</p>
  </div>
</template>

<style scoped>
.projects-tab { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; }
.list-tools { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }
.find { width: 260px; }
.lead { flex: 1; min-width: 240px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.projects { display: grid; margin: 0; padding: 0; list-style: none; }
.project { display: grid; grid-template-columns: max-content minmax(0, 1fr) auto 16px; align-items: center; gap: 14px; min-height: 54px; margin: 0 -10px; padding: 6px 10px; border-radius: 10px; color: var(--ink); text-decoration: none; }
.projects li + li .project { box-shadow: 0 -1px 0 var(--line); }
@media (hover: hover) { .project:hover { background: var(--row-hover); } .project:hover .go { color: var(--teal-ink); } }
.project:focus-visible { box-shadow: var(--focus-ring); }
.key-badge { justify-self: start; }
.p-text { display: grid; gap: 1px; min-width: 0; }
.p-title { display: flex; align-items: center; gap: 8px; font-size: 13.5px; font-weight: 600; }
.archived { height: 18px; padding: 0 7px; border-radius: 999px; box-shadow: inset 0 0 0 1px var(--line-2); font: 500 10px/18px var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.p-roles { font-size: 12.5px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.p-count { font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.go { color: var(--ink-3); }
.empty, .gone p { padding: 10px 0; font-size: 13px; color: var(--ink-3); }
.gone { display: grid; justify-items: start; gap: 8px; }
@media (max-width: 600px) {
  .find { width: 100%; }
  .find .field { height: 44px; font-size: 16px; }
  .project { grid-template-columns: max-content minmax(0, 1fr) 16px; }
  .p-count { display: none; }
  .p-roles { white-space: normal; }
}
</style>
