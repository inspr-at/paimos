<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useDecisionDesk } from '../../stores/decisionDesk'
import { useProjects } from '../../stores/projects'
import { deskItemID, deskLinkOut, deskProject, type DeskProjectionItem } from '../../lib/decisionDesk'
import AppIcon, { type IconName } from '../AppIcon.vue'
import { relativeTime } from '../../lib/work'

const desk = useDecisionDesk(), projects = useProjects()
onMounted(() => { void projects.load() })
// Rule changes name their project through their ticket; the store keeps one cache for this panel and the header preview.
watch(() => desk.projection, () => { void desk.resolveRuleProjects() }, { immediate: true })
const panelError = computed(() => desk.error || desk.ruleProjectError)
// AEON-1057: each row names its project; the link opens it in a new tab.
function rowProject(item: DeskProjectionItem) {
  const id = item.project_id || (item.kind === 'doctrine' ? desk.ruleProjects.get(item.id) : undefined)
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
// AEON-1339: the Desk row anatomy, compact: a kind mark per row, the title, the project with the Projects folder icon.
const KIND_ICON: Record<DeskProjectionItem['kind'], IconName> = { question: 'help', approval: 'clock', action_request: 'send', doctrine: 'book', tier_request: 'gauge', key_trim: 'key', stepup: 'shield', account_matrix: 'users', routine_consent: 'repeat', lesson: 'book' }
const now = computed(() => Date.parse(desk.projection?.as_of ?? '') || Date.now())
// One name for action requests on the panel, the preview and the Desk (AEON-1339); the source title stays for other readers.
const rowTitle = (item: DeskProjectionItem) => item.kind === 'action_request' ? 'Action request' : item.title
// The Desk returns here after the last decision of a round opened from this panel.
const itemLink = (item: DeskProjectionItem) => deskLinkOut(item) || { path: '/decision-desk', query: { from: 'agents', item: deskItemID(item) } }
const incomplete = computed(() => !!desk.projection && (desk.projection.has_more || desk.projection.items.length !== desk.count))
</script>

<template>
  <section class="desk-panel glass-card" :class="{ waiting: !!desk.count }" aria-labelledby="agents-desk-title">
    <header class="card-head">
      <h2 id="agents-desk-title" tabindex="-1">Decision Desk</h2>
      <span class="desk-count" aria-label="Open decisions">{{ desk.count ?? '?' }}</span>
      <RouterLink class="btn sm" :to="{ path: '/decision-desk', query: { from: 'agents' } }" data-testid="agents-desk-review"><AppIcon name="chevron-right" :size="13" />Answer one by one</RouterLink>
    </header>
    <footer><RouterLink :to="{ path: '/decision-desk', query: { view: 'decided' } }" data-testid="agents-desk-history">Decided<AppIcon name="chevron-right" :size="13" /></RouterLink></footer>
    <div v-for="group in groups" :key="group.label" class="desk-group">
      <p class="group-title">{{ group.label }}<small>{{ group.note }}</small></p>
      <ul :aria-label="group.label">
        <li v-for="item in group.items" :key="deskItemID(item)" class="desk-line" :class="{ held: item.held }">
          <span class="mark" :class="`k-${item.kind}`" aria-hidden="true"><AppIcon :name="KIND_ICON[item.kind]" :size="15" /></span>
          <span class="dl-body">
            <RouterLink :to="itemLink(item)" class="desk-item" :data-testid="`agents-desk-item-${deskItemID(item)}`">{{ rowTitle(item) }}</RouterLink>
            <span class="dl-meta"><a v-if="item.project?.href" class="row-project" :href="item.project.href" target="_blank" rel="noopener" :data-tip="`Open ${item.project.name} in a new tab`" data-testid="agents-desk-project"><AppIcon name="folder" :size="13" /><span>{{ item.project.name }}</span></a><span v-else-if="item.project" class="row-project quiet"><AppIcon name="folder" :size="13" /><span>{{ item.project.name }}</span></span><span v-if="item.created_at" class="dl-age">{{ relativeTime(item.created_at, { now }) }}</span></span>
          </span>
        </li>
      </ul>
    </div>
    <p v-if="panelError" class="desk-status src-error" role="status"><AppIcon name="alert" :size="14" /><span>{{ desk.error ? 'Couldn’t load the desk' : 'Couldn’t load the projects of some rule changes' }}</span><span class="sep">·</span><button type="button" class="linkish" :disabled="desk.loading" @click="desk.refresh()">Try again</button></p>
    <p v-else-if="desk.count === null" class="desk-status" role="status">Reading the desk…</p>
    <p v-else-if="desk.count === 0" class="desk-status">Nothing waits on you.</p>
    <p v-else-if="incomplete" class="desk-status">Some source items are outside this page. <RouterLink to="/decision-desk">Open the desk for current coverage.</RouterLink></p>
  </section>
</template>

<style scoped>
.desk-panel { overflow: hidden; }.desk-panel.waiting { background: var(--gold-wash); }
.card-head { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; padding: 16px 20px; }h2 { margin: 0; font-size: 15px; font-weight: 600; }.desk-count { min-inline-size: 3ch; font: 600 12px var(--mono); font-variant-numeric: tabular-nums; }.card-head .btn { margin-left: auto; }
.desk-group { border-top: 1px solid var(--line); }.group-title { display: flex; flex-wrap: wrap; gap: 8px 16px; margin: 0; padding: 12px 20px 6px; font-size: 12px; font-weight: 600; }.group-title small { font-size: 11px; font-weight: 400; color: var(--ink-3); }ul { list-style: none; padding: 0; margin: 0; }.desk-line { position: relative; display: grid; grid-template-columns: 28px minmax(0, 1fr); align-items: start; gap: 10px; min-height: 44px; padding: 10px 20px; color: var(--ink); font-size: 13px; overflow-wrap: anywhere; }.desk-line svg { flex: none; }.desk-line:hover { background: var(--row-hover); }
.mark { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; color: var(--ink-2); background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.mark.k-question { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.mark.k-approval, .desk-line.held .mark.k-question { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.dl-body { display: grid; gap: 2px; min-width: 0; }.desk-item { font-weight: 600; line-height: 1.35; }
.dl-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 8px; font-size: 12px; color: var(--ink-3); }
.src-error { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }.src-error svg { color: var(--ink-3); }.sep { color: var(--ink-3); }
.linkish { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }.linkish:hover { text-decoration: underline; text-underline-offset: 2px; }.linkish:disabled { opacity: .55; cursor: default; }.linkish:focus-visible { outline: none; box-shadow: var(--focus-ring); }
h2:focus { outline: none; }
/* The whole row opens the item; the project link sits above that target. */
.desk-item { min-width: 0; color: inherit; text-decoration: none; }.desk-item::after { content: ''; position: absolute; inset: 0; }.desk-item:focus-visible { outline: 0; }.desk-line:has(.desk-item:focus-visible) { outline: 2px solid var(--teal); outline-offset: -2px; }
.row-project { position: relative; z-index: 1; display: inline-flex; align-items: center; gap: 5px; flex: none; max-width: 100%; color: var(--ink-2); font-size: 12px; font-weight: 600; text-decoration: none; white-space: nowrap; }.row-project svg { color: var(--ink-3); }.row-project span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }a.row-project:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 3px; }.row-project.quiet { font-weight: 450; }.desk-status { padding: 0 20px; font-size: 12px; color: var(--ink-3); }footer { display: flex; border-top: 1px solid var(--line); padding: 12px 20px; }footer a { display: flex; align-items: center; gap: 8px; min-height: 32px; font-size: 12px; color: var(--ink-2); text-decoration: none; }
@media (max-width: 720px) { .card-head { padding: 14px; }.card-head .btn { min-height: 44px; }.group-title { padding-inline: 14px; }.desk-line { padding-inline: 14px; }footer { padding-inline: 14px; }footer a { min-height: 44px; } }
</style>
