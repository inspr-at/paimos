<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The Decision Desk preview (AEON-1214): the former Agents Desk panel, opened
// from the header's Desk chevron on every page. It reads the canonical
// projection behind the header badge; every row opens its item on the Desk.
import { computed, onMounted, ref, watch } from 'vue'
import { useDecisionDesk } from '../../stores/decisionDesk'
import { useProjects } from '../../stores/projects'
import { deskItemID, deskLinkOut, type DeskProjectionItem } from '../../lib/decisionDesk'
import { plural, relativeTime } from '../../lib/work'
import AppIcon, { type IconName } from '../AppIcon.vue'

// On phones the preview is a full-height sheet with its link pinned at the foot.
defineProps<{ phone?: boolean }>()
const emit = defineEmits<{ navigate: [] }>()
const desk = useDecisionDesk(), projects = useProjects()
// A preview, not the Desk: the first rows, and how many the Desk holds.
const ROWS = 12
const KIND: Record<DeskProjectionItem['kind'], string> = {
  question: 'Question', approval: 'Approval', action_request: 'Action request', doctrine: 'Rule change', tier_request: 'Tier request',
  key_trim: 'Key trim', stepup: 'Step-up', account_matrix: 'Account matrix', routine_consent: 'Routine consent', lesson: 'Lesson to adopt',
}
// AEON-1339: each row's kind mark, as on the Desk; held questions and approvals are amber.
const KIND_ICON: Record<DeskProjectionItem['kind'], IconName> = { question: 'help', approval: 'clock', action_request: 'send', doctrine: 'book', tier_request: 'gauge', key_trim: 'key', stepup: 'shield', account_matrix: 'users', routine_consent: 'repeat', lesson: 'book' }
const root = ref<HTMLElement>()
// A dialog: keyboard focus moves to its first row (or the Desk link) as it opens.
onMounted(() => { void projects.load(); (root.value?.querySelector<HTMLElement>('.dp-row') ?? root.value?.querySelector<HTMLElement>('.dp-go'))?.focus() })
// Rule changes name their project through their ticket. The store's cache outlives this preview,
// so a rule row names its project as the Agents panel does, from the first frame once read (AEON-1339).
watch(() => desk.projection, () => { void desk.resolveRuleProjects() }, { immediate: true })
const projectError = computed(() => desk.ruleProjectError)
// Ages read against the projection's own time, so they change with each poll.
const now = computed(() => Date.parse(desk.projection?.as_of ?? '') || Date.now())
// AEON-1057: each row names its project; workspace-wide items say Workspace.
function where(item: DeskProjectionItem) {
  const id = item.project_id || (item.kind === 'doctrine' ? desk.ruleProjects.get(item.id) : undefined)
  if (id) return projects.byId(id)?.title ?? ''
  return item.kind === 'doctrine' && id === undefined ? '' : 'Workspace'
}
const items = computed(() => {
  const all = desk.projection?.items ?? []
  return [...all.filter(item => item.held), ...all.filter(item => !item.held)]
})
// The Desk returns to the page under the preview after the last decision of a round opened here.
const shown = computed(() => items.value.slice(0, ROWS).map(item => ({
  // One name for action requests (AEON-1339): the kind is the title, so the detail does not repeat it.
  key: deskItemID(item), held: item.held, title: item.kind === 'action_request' ? KIND.action_request : item.title, kind: item.kind, project: where(item),
  detail: [item.kind === 'action_request' ? '' : KIND[item.kind], relativeTime(item.created_at, { now: now.value })].filter(Boolean).join(' · '),
  to: deskLinkOut(item) || { path: '/decision-desk', query: { from: 'preview', item: deskItemID(item) } },
})))
const groups = computed(() => [
  { label: 'Waiting on you', note: 'Something is held up until an answer.', rows: shown.value.filter(row => row.held) },
  { label: 'When you’re ready', note: 'The agents carry on meanwhile.', rows: shown.value.filter(row => !row.held) },
].filter(group => group.rows.length))
const status = computed(() => desk.error ? 'Count unavailable' : desk.count === null ? 'Reading' : desk.count ? `${plural(desk.count, 'thing')} for you` : 'Nothing for you')
// Honest coverage: the Desk's own total, never this list's length.
const partial = computed(() => desk.count !== null && shown.value.length < desk.count ? `${shown.value.length} of ${desk.count} shown here.` : '')
</script>

<!-- Controls never move (AEON-541): reads, failures and refreshes change only the
     rows, which grow downward or scroll. On wide screens the link to the Desk sits
     in the head above them; on phones it is pinned at the foot of a full-height sheet. -->
<template>
  <div ref="root" class="dp">
    <div class="dp-head">
      <div class="dp-title">
        <h3>Decision Desk</h3>
        <span class="dp-count" role="status">{{ status }}</span>
      </div>
      <RouterLink v-if="!phone" class="dp-go" to="/decision-desk" @click="emit('navigate')">Open the Decision Desk<AppIcon name="chevron-right" :size="13" /></RouterLink>
    </div>
    <div class="dp-body">
      <p v-if="desk.error" class="dp-note dp-error" role="alert"><AppIcon name="alert" :size="14" /><span>Couldn’t load the desk</span><span class="dp-sep">·</span><button type="button" class="dp-retry" :disabled="desk.loading" @click="desk.refresh()">Try again</button></p>
      <p v-else-if="desk.count === null" class="dp-note">Reading the Desk…</p>
      <p v-else-if="!groups.length" class="dp-note">Nothing waits on you.</p>
      <section v-for="group in groups" :key="group.label" class="dp-group" :aria-label="group.label">
        <p class="dp-group-title">{{ group.label }}<small>{{ group.note }}</small></p>
        <ul>
          <li v-for="row in group.rows" :key="row.key">
            <RouterLink class="dp-row" :to="row.to" :aria-label="`${row.title}, ${[row.project, row.detail].filter(Boolean).join(' · ')}`" :data-testid="`desk-preview-item-${row.key}`" @click="emit('navigate')">
              <span class="dp-ic" :class="[`k-${row.kind}`, { held: row.held }]"><AppIcon :name="KIND_ICON[row.kind]" :size="14" /></span>
              <span class="dp-t"><b>{{ row.title }}</b><small><span v-if="row.project" class="dp-project"><AppIcon name="folder" :size="12" />{{ row.project }}</span><span v-if="row.project && row.detail"> · </span>{{ row.detail }}</small></span>
              <span class="dp-open" aria-hidden="true">Open</span>
            </RouterLink>
          </li>
        </ul>
      </section>
      <p v-if="projectError" class="dp-note">{{ projectError }}</p>
      <p v-if="partial && !phone" class="dp-partial">{{ partial }}</p>
    </div>
    <div v-if="phone" class="dp-foot">
      <span v-if="partial" class="dp-partial">{{ partial }}</span>
      <RouterLink class="dp-go" to="/decision-desk" @click="emit('navigate')">Open the Decision Desk<AppIcon name="chevron-right" :size="13" /></RouterLink>
    </div>
  </div>
</template>

<style scoped>
.dp { display: flex; flex: 1 1 auto; flex-direction: column; gap: 8px; min-height: 0; }
.dp-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 36px; padding: 0 6px; }
.dp-title { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
h3 { margin: 0; font-size: 14px; font-weight: 600; }
.dp-count { color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.dp-body { flex: 1 1 auto; min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.dp-note { margin: 0; padding: 10px 6px; color: var(--ink-2); font-size: 12.5px; }
.dp-group + .dp-group { margin-top: 6px; padding-top: 6px; border-top: 1px solid var(--line); }
.dp-group-title { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; margin: 0; padding: 4px 6px; color: var(--ink-2); font-size: 12px; font-weight: 600; }
.dp-group-title small { color: var(--ink-3); font-size: 11.5px; font-weight: 400; }
ul { margin: 0; padding: 0; list-style: none; }
li + li .dp-row { box-shadow: 0 -1px 0 var(--line); }
/* The whole row opens the item on the Desk. */
.dp-row { display: grid; grid-template-columns: 28px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 52px; padding: 6px; border-radius: 8px; color: var(--ink); text-decoration: none; }
@media (hover: hover) { .dp-row:hover { background: var(--row-hover); } }
.dp-row:active { background: var(--row-selected); }
.dp-row:focus-visible { outline: none; background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.dp-ic { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; color: var(--ink-2); background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.dp-ic.k-question { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.dp-ic.k-approval, .dp-ic.k-question.held { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.dp-project { display: inline-flex; align-items: center; gap: 4px; }.dp-project svg { color: var(--ink-3); }
.dp-error { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }.dp-error svg { color: var(--ink-3); }.dp-sep { color: var(--ink-3); }
.dp-retry { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }.dp-retry:hover { text-decoration: underline; text-underline-offset: 2px; }.dp-retry:disabled { opacity: .55; cursor: default; }.dp-retry:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.dp-t { display: grid; gap: 2px; min-width: 0; overflow-wrap: anywhere; }
.dp-t b { font-size: 13px; font-weight: 600; line-height: 1.35; }
.dp-t small { color: var(--ink-3); font-size: 11.5px; line-height: 1.35; }
.dp-open { display: inline-flex; align-items: center; height: 28px; padding: 0 11px; border-radius: 999px; background: var(--btn-bg); box-shadow: var(--shadow-btn), inset 0 0 0 1px var(--glass-edge); color: var(--ink); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.dp-foot { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 4px 12px; padding: 0 6px; border-top: 1px solid var(--line); }
.dp-partial { margin: 0; color: var(--ink-3); font-size: 12px; }
.dp-body > .dp-partial { padding: 8px 6px 2px; }
.dp-go { display: inline-flex; flex-shrink: 0; align-items: center; gap: 2px; min-height: 36px; margin-left: auto; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; text-decoration: none; white-space: nowrap; }
.dp-go:hover { text-decoration: underline; text-underline-offset: 2px; }
.dp-go:focus-visible { outline: none; border-radius: 6px; box-shadow: var(--focus-ring); }
/* Phones and touch: the footer link and Retry are full 44 px targets. */
@media (max-width: 600px), (pointer: coarse) {
  .dp-go, .dp-retry { min-height: 44px; }
  .dp-t b { font-size: 14px; }
}
</style>

<!-- The panel around the preview belongs to HeaderMenu; on phones it is teleported, so its size lives here.
     On wide screens it sits inside the Desk place: it starts from the body's text, never the place's one-line label. -->
<style>
.hm-panel.desk-panel.pop { display: flex; flex-direction: column; width: min(460px, calc(100vw - 24px)); max-height: calc(100dvh - var(--header-h) - 12px); padding: 8px 8px 6px; overflow: hidden; font: 400 14px/1.5 var(--font); white-space: normal; text-align: start; }
/* Phones: a full-height sheet; its head and foot stay put while the rows scroll between them. */
.hm-panel.desk-panel.pop.sheet { top: max(8px, env(safe-area-inset-top)); width: auto; max-height: none; padding: 18px 10px calc(8px + env(safe-area-inset-bottom)); overflow: hidden; }
</style>
