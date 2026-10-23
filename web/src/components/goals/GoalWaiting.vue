<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One canonical local projection plus the shared browser connection store.
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import { vClipTip } from '../../lib/clipTip'
import { useDecisionDesk } from '../../stores/decisionDesk'
import { useProjects } from '../../stores/projects'
import { deskItemID, deskLinkOut, deskListKey, type DeskProjectionItem } from '../../lib/decisionDesk'
import { useDeskConnections } from '../../stores/deskConnections'
import DeskInstanceItems from '../DeskInstanceItems.vue'
import type { Goal } from '../../lib/goals'

defineProps<{ goals: readonly Goal[] }>()
const desk = useDecisionDesk(), projects = useProjects()
const elsewhere = useDeskConnections()
const heldFirst = <T extends { held: boolean }>(list: readonly T[]) => [...list.filter(item => item.held), ...list.filter(item => !item.held)]
// Tags show only when there is more than one instance to tell apart.
const multi = computed(() => elsewhere.reads.length > 0)
const here = computed(() => elsewhere.here)
const items = computed(() => heldFirst(desk.projection?.items ?? []))
const rows = computed(() => items.value.slice(0, 3).map((item: DeskProjectionItem) => {
  const project = item.project_id ? projects.byId(item.project_id) : undefined
  return {
    definitionKey: item.definition_change?.project_key, id: deskItemID(item), key: deskListKey(item, project?.routeKey ?? ''),
    question: item.kind === 'action_request' ? 'Action request' : item.title,
    by: item.asked_by?.name ?? '',
    to: deskLinkOut(item) || { path: '/decision-desk', query: { item: deskItemID(item) } },
  }
}))
const unknown = computed(() => elsewhere.discovery === 'unknown')
const split = computed(() => elsewhere.reads.filter(r => r.summary).map(r => `${r.summary!.open} on ${r.tag}`).join(' · '))
const hereCount = computed(() => desk.hereCount)
const count = computed(() => desk.count)
const more = computed(() => (hereCount.value ?? 0) > rows.value.length ? `${(hereCount.value ?? 0) - rows.value.length} more here` : '')
const coming = computed(() => desk.projection?.upcoming?.[0])
const comingLabel = computed(() => coming.value ? `${coming.value.label}${coming.value.ticket_key ? ` (${coming.value.ticket_key})` : ''}` : '')
const reading = computed(() => hereCount.value === null)
</script>

<template>
  <section v-if="desk.error && hereCount === null" class="waiting is-empty" aria-label="Waiting on you">
    <div class="waiting-head">
      <span class="w-ic"><AppIcon name="alert" :size="15" /></span>
      <span class="w-line"><span>Waiting on you couldn’t be read</span></span>
      <button type="button" class="btn sm ghost waiting-open" @click="desk.refresh()">Try again</button>
    </div>
  </section>
  <section v-else-if="reading" class="waiting is-empty is-reading" aria-label="Waiting on you" aria-busy="true">
    <div class="waiting-head"><span class="w-ic"><AppIcon name="person-wait" :size="15" /></span><span class="w-line"><span class="skeleton" /></span></div>
  </section>
  <section v-else-if="desk.allClear" class="waiting is-empty is-clear" aria-label="Waiting on you">
    <div class="waiting-head">
      <span class="w-ic"><AppIcon name="check" :size="15" /></span>
      <span class="w-line"><span>Nothing waits on you</span><span v-if="coming" v-clip-tip class="coming"><b>Coming</b> · {{ comingLabel }}</span></span>
      <RouterLink class="btn sm ghost waiting-open" to="/decision-desk">Open Desk</RouterLink>
    </div>
  </section>
  <section v-else class="waiting" aria-label="Waiting on you">
    <div class="waiting-head">
      <span class="w-ic"><AppIcon name="person-wait" :size="15" /></span>
      <span class="waiting-title">Waiting on you<span class="n-badge mono" :aria-label="`${count} open`" :data-tip="desk.countTip">{{ count }}</span><span v-if="split" class="w-split">· {{ split }}</span></span>
      <RouterLink class="btn sm waiting-open" to="/decision-desk">Open Desk<AppIcon name="arrow" :size="13" /></RouterLink>
    </div>
    <ul class="waiting-list">
      <li v-for="row in rows" :key="row.id">
        <RouterLink class="waiting-item" :to="row.to" :aria-label="`${multi ? `${here}, ` : ''}${row.key}: ${row.question}${row.by ? `. Asked by ${row.by}` : ''}. Open in the Desk`">
          <span v-if="multi" class="wtag mono" :data-tip="`On ${here}, this instance`">{{ here }}</span>
          <span v-clip-tip class="wkey mono">{{ row.key }}</span>
          <span class="wline">
            <span v-clip-tip class="wq"><template v-if="row.definitionKey"><span class="definition-project-key">{{ row.definitionKey }}</span>{{ row.question.slice(row.definitionKey.length) }}</template><template v-else>{{ row.question }}</template></span>
            <span v-if="row.by" class="wby">· asked by {{ row.by }}</span>
          </span>
          <span class="wopen">Open</span>
        </RouterLink>
      </li>
      <li v-if="unknown" class="w-coverage">
        <div class="waiting-item w-err" role="status">
          <span class="werr">Other instances couldn’t be read<span aria-hidden="true">·</span><button type="button" class="w-retry" @click="elsewhere.refresh()">Try again</button></span>
        </div>
      </li>
    </ul>
    <DeskInstanceItems v-for="read in elsewhere.reads" :key="read.tag" :read="read" variant="goals" />
    <RouterLink v-if="more" class="w-more" to="/decision-desk">{{ more }}</RouterLink>
    <p v-if="coming" v-clip-tip class="coming"><b>Coming</b> · {{ comingLabel }}</p>
  </section>
</template>

<style scoped>
.definition-project-key { white-space: nowrap; overflow-wrap: normal; }
.waiting, .waiting-head, .waiting-list, .waiting-list > li, .waiting-item { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; }
.waiting-list { margin: 2px 0 0; padding: 0; list-style: none; }
.waiting { position: relative; isolation: isolate; padding: 14px 0 12px; align-content: start; }
.waiting::before { content: ''; position: absolute; inset: 0 -20px; background: var(--gold-wash); z-index: -1; pointer-events: none; }
.waiting.is-empty::before { display: none; }
.waiting::after { content: ''; position: absolute; left: -20px; right: -20px; bottom: 0; height: 1px; background: var(--line); }
.waiting-head { align-items: center; min-height: 30px; }
.w-ic { grid-column: 1; justify-self: center; display: grid; place-items: center; color: var(--gold-ink); }
.waiting-title { grid-column: 2 / 5; display: inline-flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 650; color: var(--ink); white-space: nowrap; }
.w-split { font-size: 12.5px; font-weight: 500; color: var(--ink-3); }
.n-badge { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px; background: var(--surface-sunken); color: var(--ink-2); font-size: 11px; font-weight: 600; font-variant-numeric: tabular-nums; }
.waiting-open { grid-column: -4 / -1; justify-self: end; gap: 6px; }
.waiting-item { position: relative; align-items: center; min-height: 34px; color: var(--ink); text-decoration: none; }
.waiting-item::before { content: ''; position: absolute; inset: 1px -14px; border-radius: 8px; pointer-events: none; }
@media (hover: hover) { .waiting-item:hover::before { background: var(--row-hover); } }
.waiting-item:focus-visible { outline: none; box-shadow: none; }
.waiting-item:focus-visible::before { box-shadow: var(--focus-ring); }
.wtag { position: relative; grid-column: 1; justify-self: center; font-size: 10.5px; font-weight: 500; line-height: 1; color: var(--ink-3); font-variant-ligatures: none; }
/* Column 2 is shared and sizes to content (AEON-541). A ticket key past a normal key clips here. */
.wkey { position: relative; grid-column: 2; min-width: 0; max-width: 14ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; font-weight: 500; line-height: 1; color: var(--ink-2); }
.wline { position: relative; grid-column: 3 / -3; display: flex; align-items: baseline; gap: 6px; min-width: 0; }
.wq { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; font-weight: 500; }
.wby { flex: none; font-size: 12.5px; color: var(--ink-3); white-space: nowrap; }
.wopen { position: relative; grid-column: -3 / -1; justify-self: end; display: inline-flex; align-items: center; gap: 4px; font-size: 12.5px; font-weight: 600; color: var(--ink-2); white-space: nowrap; }
.wopen svg { color: var(--ink-3); }
@media (hover: hover) { .waiting-item:hover .wopen, .waiting-item:hover .wopen svg { color: var(--teal-ink); } }
.waiting-item:focus-visible .wopen, .waiting-item:focus-visible .wopen svg { color: var(--teal-ink); }
.w-err:hover::before { background: none; }
.werr { position: relative; grid-column: 2 / -3; display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 13px; color: var(--ink-3); }
.w-retry { position: relative; height: 26px; padding: 0 6px; margin-left: -4px; border: 0; border-radius: 6px; background: transparent; color: var(--teal-ink); font-size: 13px; font-weight: 600; }
@media (hover: hover) { .w-retry:hover { background: var(--row-hover); } }
.w-retry:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.w-more { display: none; }
.coming { grid-column: 3 / -3; min-width: 0; margin: 4px 0 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-3); }
.coming b { font-weight: 600; color: var(--ink-2); }
.waiting.is-empty { padding: 11px 0; align-content: center; min-height: 52px; }
.is-clear .w-ic { color: var(--ok); }
.is-reading .w-ic { color: var(--ink-3); }
.w-line { grid-column: 2 / -4; display: flex; align-items: baseline; gap: 10px; min-width: 0; font-size: 13px; color: var(--ink-2); }
.w-line .coming { grid-column: auto; margin: 0; }
.w-line .skeleton { width: 180px; }
@media (max-width: 760px), (pointer: coarse) {
  .w-retry::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); }
}
@media (max-width: 760px) {
  .waiting { display: block; margin: 0 -8px; padding: 8px 14px 10px; }
  .waiting::after { left: 0; right: 0; }
  .waiting::before { inset: 0; }
  .waiting-head { display: flex; align-items: center; gap: 10px; min-height: 48px; }
  .waiting-title { grid-column: auto; }
  .w-split { display: none; }
  .waiting-open { margin-left: auto; min-height: 44px; }
  .waiting-list, .waiting-list > li { display: block; }
  .waiting-item { display: flex; align-items: baseline; gap: 10px; min-height: 52px; padding: 7px 0; }
  .waiting-item::before { inset: 0 -8px; }
  .wkey, .wby, .wopen { display: none; }
  .wtag { flex: none; font-size: 11px; }
  .wline { display: block; flex: 1 1 auto; }
  .wq { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; font-size: 14px; line-height: 1.35; }
  .waiting-list > li:nth-child(n+2) { display: none; }
  .waiting-list > li.w-coverage { display: block; }
  .w-err { padding: 0; align-items: center; }
  .werr { display: flex; }
  .w-more { position: relative; display: flex; align-items: center; min-height: 44px; margin: 0; font-size: 13px; color: var(--ink-3); }
  .coming { margin: 4px 0 0; font-size: 13px; }
  .waiting.is-empty { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; padding-top: 8px; padding-bottom: 8px; }
  .waiting.is-empty .waiting-head { display: contents; }
  .w-line { display: block; flex: 1 1 200px; }
  .w-line .coming { display: block; margin-top: 2px; }
}
</style>
