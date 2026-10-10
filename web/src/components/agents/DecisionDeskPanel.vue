<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useDecisionDesk } from '../../stores/decisionDesk'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { deskItemID, deskLinkOut, deskProject, projectColor, type DeskProjectionItem } from '../../lib/decisionDesk'
import { getDoctrineInbox } from '../../lib/doctrine'
import { readDoctrineProjects } from '../../lib/decisionDeskApi'
import AppIcon from '../AppIcon.vue'

const desk = useDecisionDesk(), projects = useProjects(), session = useSession()
const owner = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}`)
onMounted(() => { void projects.load() })
const doctrineProjects = ref(new Map<string, string>()), projectError = ref('')
let projectGeneration = 0
watch([() => desk.projection, owner], async ([projection, identity], [, previousOwner]) => {
  const turn = ++projectGeneration
  // Polls replace the projection object. Keep resolved links under the pointer,
  // but discard the previous person's cache and any late lookup on invalidation.
  if (!projection || identity !== previousOwner) { doctrineProjects.value = new Map(); projectError.value = '' }
  const ids = new Set(projection?.items.filter(item => item.kind === 'doctrine' && !item.project_id && !doctrineProjects.value.has(item.id)).map(item => item.id))
  if (!ids.size) return
  projectError.value = ''
  const current = () => turn === projectGeneration && desk.projection === projection && identity === owner.value && session.authenticationCurrent()
  try {
    const inbox = await getDoctrineInbox()
    if (!current()) return
    const rules = inbox.items.filter(rule => ids.has(rule.id))
    const tickets = await readDoctrineProjects(rules)
    if (!current()) return
    const resolved = new Map(doctrineProjects.value)
    for (const rule of rules) {
      const project = rule.ticket ? tickets.get(rule.ticket) : undefined
      if (project) resolved.set(rule.id, project)
      else if (!rule.ticket) resolved.set(rule.id, '') // A workspace rule is resolved without a project.
    }
    doctrineProjects.value = resolved
  } catch {
    if (current()) projectError.value = 'The projects of some rule changes could not be read.'
  }
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { projectGeneration++ })
const panelError = computed(() => desk.error || projectError.value)
// AEON-1057: each row names its project; the link opens it in a new tab.
function rowProject(item: DeskProjectionItem) {
  const id = item.project_id || (item.kind === 'doctrine' ? doctrineProjects.value.get(item.id) : undefined)
  const project = id ? projects.byId(id) : undefined
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
        <li v-for="item in group.items" :key="deskItemID(item)" class="desk-line">
          <AppIcon name="chevron-right" :size="14" />
          <a v-if="item.project?.href" class="row-project" :href="item.project.href" target="_blank" rel="noopener" :style="{ '--pc': projectColor(item.project) }" :title="`Open ${item.project.name} in a new tab`" data-testid="agents-desk-project"><i aria-hidden="true" /><span>{{ item.project.name }}</span></a>
          <RouterLink :to="deskLinkOut(item) || { path: '/decision-desk', query: { item: deskItemID(item) } }" class="desk-item" :data-testid="`agents-desk-item-${deskItemID(item)}`">{{ item.title }}</RouterLink>
        </li>
      </ul>
    </div>
    <p v-if="panelError" class="desk-status" role="status">{{ panelError }} <button type="button" class="btn sm ghost" :disabled="desk.loading" @click="desk.refresh()">Retry</button></p>
    <p v-else-if="desk.count === null" class="desk-status" role="status">Reading the desk…</p>
    <p v-else-if="desk.count === 0" class="desk-status">Nothing waits on you.</p>
    <p v-else-if="incomplete" class="desk-status">Some source items are outside this page. <RouterLink to="/decision-desk">Open the desk for current coverage.</RouterLink></p>
  </section>
</template>

<style scoped>
.desk-panel { overflow: hidden; }.desk-panel.waiting { background: var(--gold-wash); }
.card-head { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; padding: 16px 20px; }h2 { margin: 0; font-size: 15px; font-weight: 600; }.desk-count { min-inline-size: 3ch; font: 600 12px var(--mono); font-variant-numeric: tabular-nums; }.card-head .btn { margin-left: auto; }
.desk-group { border-top: 1px solid var(--line); }.group-title { display: flex; flex-wrap: wrap; gap: 8px 16px; margin: 0; padding: 12px 20px 6px; font-size: 12px; font-weight: 600; }.group-title small { font-size: 11px; font-weight: 400; color: var(--ink-3); }ul { list-style: none; padding: 0; margin: 0; }.desk-line { position: relative; display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 12px 20px; color: var(--ink); font-size: 13px; overflow-wrap: anywhere; }.desk-line svg { flex: none; }.desk-line:hover { background: var(--row-hover); }
/* The whole row opens the item; the project link sits above that target. */
.desk-item { min-width: 0; color: inherit; text-decoration: none; }.desk-item::after { content: ''; position: absolute; inset: 0; }.desk-item:focus-visible { outline: 0; }.desk-line:has(.desk-item:focus-visible) { outline: 2px solid var(--teal); outline-offset: -2px; }
.row-project { position: relative; z-index: 1; display: inline-flex; align-items: center; gap: 6px; flex: none; max-width: 40%; color: var(--pc); font-size: 12px; font-weight: 650; text-decoration: none; white-space: nowrap; }.row-project span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }.row-project:hover { text-decoration: underline; text-underline-offset: 3px; }.row-project i { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--pc); }.desk-status { padding: 0 20px; font-size: 12px; color: var(--ink-3); }footer { display: flex; border-top: 1px solid var(--line); padding: 12px 20px; }footer a { display: flex; align-items: center; gap: 8px; min-height: 32px; font-size: 12px; color: var(--ink-2); text-decoration: none; }
@media (max-width: 720px) { .card-head { padding: 14px; }.card-head .btn { min-height: 44px; }.group-title { padding-inline: 14px; }.desk-line { padding-inline: 14px; }footer { padding-inline: 14px; }footer a { min-height: 44px; } }
</style>
