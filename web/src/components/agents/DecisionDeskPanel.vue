<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useDecisionDesk } from '../../stores/decisionDesk'
import { useProjects } from '../../stores/projects'
import { deskItemID, deskProject, projectColor, type DeskProjectionItem } from '../../lib/decisionDesk'
import AppIcon from '../AppIcon.vue'

const desk = useDecisionDesk(), projects = useProjects()
onMounted(() => { void projects.load() })
// AEON-1057: each row names its project; the link opens it in a new tab.
function rowProject(item: DeskProjectionItem) {
  const project = item.project_id ? projects.byId(item.project_id) : undefined
  return project ? deskProject({ projectId: project.id, projectName: project.title, projectKey: project.key }) : undefined
}
const groups = computed(() => {
  const items = (desk.projection?.items ?? []).map(item => ({ ...item, project: rowProject(item) }))
  return [
    { label: 'Waiting on you', note: 'Something is held up until an answer.', items: items.filter(item => item.held) },
    { label: 'When you’re ready', note: 'The agents carry on meanwhile.', items: items.filter(item => !item.held) },
  ].filter(group => group.items.length)
})
const incomplete = computed(() => !!desk.projection && (desk.projection.has_more || desk.projection.items.length !== desk.count))
</script>

<template>
  <section class="desk-panel glass-card" :class="{ waiting: !!desk.count }" aria-labelledby="agents-desk-title">
    <header class="card-head">
      <h2 id="agents-desk-title">Decision Desk</h2>
      <span class="desk-count" aria-label="Open decisions">{{ desk.count ?? '?' }}</span>
      <RouterLink class="btn sm" to="/decision-desk" data-testid="agents-desk-review"><AppIcon name="chevron-right" :size="13" />Answer one by one</RouterLink>
    </header>
    <footer><RouterLink :to="{ path: '/decision-desk', query: { view: 'decided' } }" data-testid="agents-desk-history">Decided<AppIcon name="chevron-right" :size="13" /></RouterLink></footer>
    <div v-for="group in groups" :key="group.label" class="desk-group">
      <p class="group-title">{{ group.label }}<small>{{ group.note }}</small></p>
      <ul :aria-label="group.label">
        <li v-for="item in group.items" :key="deskItemID(item)" class="desk-item">
          <AppIcon name="chevron-right" :size="14" />
          <a v-if="item.project?.href" class="row-project" :href="item.project.href" target="_blank" rel="noopener" :style="{ '--pc': projectColor(item.project) }" :title="`Open ${item.project.name} in a new tab`" data-testid="agents-desk-project"><i aria-hidden="true" /><span>{{ item.project.name }}</span></a>
          <RouterLink :to="{ path: '/decision-desk', query: { item: deskItemID(item) } }" class="row-open">{{ item.title }}</RouterLink>
        </li>
      </ul>
    </div>
    <p v-if="desk.error" class="desk-status" role="status">{{ desk.error }} <button type="button" class="btn sm ghost" :disabled="desk.loading" @click="desk.refresh()">Retry</button></p>
    <p v-else-if="desk.count === null" class="desk-status" role="status">Reading the desk…</p>
    <p v-else-if="desk.count === 0" class="desk-status">Nothing waits on you.</p>
    <p v-else-if="incomplete" class="desk-status">Some source items are outside this page. <RouterLink to="/decision-desk">Open the desk for current coverage.</RouterLink></p>
  </section>
</template>

<style scoped>
.desk-panel { overflow: hidden; }.desk-panel.waiting { background: var(--gold-wash); }
.card-head { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; padding: 16px 20px; }h2 { margin: 0; font-size: 15px; font-weight: 600; }.desk-count { min-inline-size: 3ch; font: 600 12px var(--mono); font-variant-numeric: tabular-nums; }.card-head .btn { margin-left: auto; }
.desk-group { border-top: 1px solid var(--line); }.group-title { display: flex; flex-wrap: wrap; gap: 8px 16px; margin: 0; padding: 12px 20px 6px; font-size: 12px; font-weight: 600; }.group-title small { font-size: 11px; font-weight: 400; color: var(--ink-3); }ul { list-style: none; padding: 0; margin: 0; }.desk-item { position: relative; display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 12px 20px; color: var(--ink); font-size: 13px; overflow-wrap: anywhere; }.desk-item svg { flex: none; }.desk-item:hover { background: var(--row-hover); }
/* The whole row opens the item; the project link sits above that target. */
.row-open { min-width: 0; color: inherit; text-decoration: none; }.row-open::after { content: ''; position: absolute; inset: 0; }.row-open:focus-visible { outline: 0; }.desk-item:has(.row-open:focus-visible) { outline: 2px solid var(--teal); outline-offset: -2px; }
.row-project { position: relative; z-index: 1; display: inline-flex; align-items: center; gap: 6px; flex: none; max-width: 40%; color: var(--pc); font-size: 12px; font-weight: 650; text-decoration: none; white-space: nowrap; }.row-project span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }.row-project:hover { text-decoration: underline; text-underline-offset: 3px; }.row-project i { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--pc); }.desk-status { padding: 0 20px; font-size: 12px; color: var(--ink-3); }footer { display: flex; border-top: 1px solid var(--line); padding: 12px 20px; }footer a { display: flex; align-items: center; gap: 8px; min-height: 32px; font-size: 12px; color: var(--ink-2); text-decoration: none; }
@media (max-width: 720px) { .card-head { padding: 14px; }.card-head .btn { min-height: 44px; }.group-title { padding-inline: 14px; }.desk-item { padding-inline: 14px; }footer { padding-inline: 14px; }footer a { min-height: 44px; } }
</style>
