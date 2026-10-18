<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Waiting on you (AEON-1457 decision 3): the person's live Decision Desk items, from
// the same projection as the header badge (and its count). At most three first lines
// (key · question · goal) and Open Desk; on a phone the first question, "N more" and
// Open Desk. A quiet single line when nothing waits. A warm full tint, never an edge bar.
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import { vClipTip } from '../../lib/clipTip'
import { useDecisionDesk } from '../../stores/decisionDesk'
import { useProjects } from '../../stores/projects'
import { deskItemID, deskLinkOut, type DeskProjectionItem } from '../../lib/decisionDesk'
import type { Goal } from '../../lib/goals'

const props = defineProps<{ goals: readonly Goal[] }>()
const desk = useDecisionDesk(), projects = useProjects()
// A question attached to a goal names the goal; anything else names its project.
const goalOf = computed(() => {
  const map = new Map<string, string>()
  for (const goal of props.goals) for (const id of goal.waits.question_ids) map.set(id, goal.title)
  return map
})
const items = computed(() => {
  const all = desk.projection?.items ?? []
  return [...all.filter(item => item.held), ...all.filter(item => !item.held)]
})
const rows = computed(() => items.value.slice(0, 3).map((item: DeskProjectionItem) => {
  const project = item.project_id ? projects.byId(item.project_id) : undefined
  return {
    id: deskItemID(item), key: project?.routeKey ?? '',
    question: item.kind === 'action_request' ? 'Action request' : item.title,
    where: (item.kind === 'question' ? goalOf.value.get(item.id) : undefined) ?? project?.title ?? (item.project_id ? '' : 'Workspace'),
    to: deskLinkOut(item) || { path: '/decision-desk', query: { item: deskItemID(item) } },
  }
}))
const count = computed(() => desk.count)
</script>

<template>
  <section v-if="desk.error && count === null" class="waiting is-empty" aria-label="Waiting on you">
    <div class="waiting-head">
      <span class="w-ic"><AppIcon name="alert" :size="15" /></span>
      <span class="w-line"><span>Waiting on you couldn’t be read</span></span>
      <button type="button" class="btn sm ghost waiting-open" @click="desk.refresh()">Try again</button>
    </div>
  </section>
  <section v-else-if="count === null" class="waiting is-empty is-reading" aria-label="Waiting on you" aria-busy="true">
    <div class="waiting-head"><span class="w-ic"><AppIcon name="person-wait" :size="15" /></span><span class="w-line"><span class="skeleton" /></span></div>
  </section>
  <section v-else-if="count === 0" class="waiting is-empty is-clear" aria-label="Waiting on you">
    <div class="waiting-head">
      <span class="w-ic"><AppIcon name="check" :size="15" /></span>
      <span class="w-line"><span>Nothing waits on you</span></span>
      <RouterLink class="btn sm ghost waiting-open" to="/decision-desk">Open Desk</RouterLink>
    </div>
  </section>
  <section v-else class="waiting" aria-label="Waiting on you">
    <div class="waiting-head">
      <span class="w-ic"><AppIcon name="person-wait" :size="15" /></span>
      <span class="waiting-title">Waiting on you<span class="n-badge mono" :aria-label="`${count} open`">{{ count }}</span></span>
      <RouterLink class="btn sm waiting-open" to="/decision-desk">Open Desk<AppIcon name="arrow" :size="13" /></RouterLink>
    </div>
    <ul class="waiting-list">
      <li v-for="row in rows" :key="row.id">
        <RouterLink class="waiting-item" :to="row.to">
          <span class="wkey mono">{{ row.key }}</span>
          <span v-clip-tip class="wq">{{ row.question }}</span>
          <span class="wgoal">{{ row.where }}</span>
        </RouterLink>
      </li>
    </ul>
    <RouterLink v-if="count > 1" class="w-more" to="/decision-desk">{{ count - 1 }} more</RouterLink>
  </section>
</template>

<style scoped>
.waiting, .waiting-head, .waiting-list, .waiting-list > li, .waiting-item { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; }
.waiting-list { margin: 2px 0 0; padding: 0; list-style: none; }
.waiting { position: relative; isolation: isolate; padding: 14px 0 12px; align-content: start; }
.waiting::before { content: ''; position: absolute; inset: 0 -20px; background: var(--gold-wash); z-index: -1; pointer-events: none; }
.waiting.is-empty::before { display: none; }
.waiting::after { content: ''; position: absolute; left: -20px; right: -20px; bottom: 0; height: 1px; background: var(--line); }
.waiting-head { align-items: center; min-height: 30px; }
.w-ic { grid-column: 1; justify-self: center; display: grid; place-items: center; color: var(--gold-ink); }
.waiting-title { grid-column: 2 / 5; display: inline-flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 650; color: var(--ink); white-space: nowrap; }
.n-badge { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px; background: var(--surface-sunken); color: var(--ink-2); font-size: 11px; font-weight: 600; font-variant-numeric: tabular-nums; }
.waiting-open { grid-column: -4 / -1; justify-self: end; gap: 6px; }
.waiting-item { position: relative; align-items: center; min-height: 34px; color: var(--ink); text-decoration: none; }
.waiting-item::before { content: ''; position: absolute; inset: 1px -14px; border-radius: 8px; pointer-events: none; }
@media (hover: hover) { .waiting-item:hover::before { background: var(--row-hover); } }
.waiting-item:focus-visible { outline: none; box-shadow: none; }
.waiting-item:focus-visible::before { box-shadow: var(--focus-ring); }
.wkey { position: relative; grid-column: 2; font-size: 11.5px; font-weight: 500; line-height: 1; color: var(--ink-2); }
.wq { position: relative; grid-column: 3 / -3; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; font-weight: 500; }
.wgoal { position: relative; grid-column: -3 / -2; justify-self: end; font-size: 12.5px; color: var(--ink-3); white-space: nowrap; }
.w-more { display: none; }
.waiting.is-empty { padding: 11px 0; align-content: center; min-height: 52px; }
.is-clear .w-ic { color: var(--ok); }
.is-reading .w-ic { color: var(--ink-3); }
.w-line { grid-column: 2 / -4; display: flex; align-items: baseline; gap: 10px; min-width: 0; font-size: 13px; color: var(--ink-2); }
.w-line .skeleton { width: 180px; }
@media (max-width: 760px) {
  .waiting { display: block; margin: 0 -8px; padding: 8px 14px 10px; }
  .waiting::after { left: 0; right: 0; }
  .waiting::before { inset: 0; }
  .waiting-head { display: flex; align-items: center; gap: 10px; min-height: 48px; }
  .waiting-title { grid-column: auto; }
  .waiting-open { margin-left: auto; min-height: 44px; }
  .waiting-list, .waiting-list > li { display: block; }
  .waiting-item { display: block; min-height: 52px; padding: 7px 0; }
  .waiting-item::before { inset: 0 -8px; }
  .wkey, .wgoal { display: none; }
  .wq { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; font-size: 14px; line-height: 1.35; }
  .waiting-list > li:nth-child(n+2) { display: none; }
  .w-more { position: relative; display: flex; align-items: center; min-height: 44px; margin: 0; font-size: 13px; color: var(--ink-3); }
  .waiting.is-empty { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; padding-top: 8px; padding-bottom: 8px; }
  .waiting.is-empty .waiting-head { display: contents; }
  .w-line { display: block; flex: 1 1 200px; }
}
</style>
