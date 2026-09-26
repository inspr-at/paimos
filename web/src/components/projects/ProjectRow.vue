<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { Project } from '../../stores/projects'
import type { ProjectColumnId } from '../../lib/projectColumns'
import { useLiveAgents } from '../../stores/liveAgents'
import { absoluteTime, highlight, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import LiveAgents from './LiveAgents.vue'
import PeopleStack from './PeopleStack.vue'
import StatCount, { STATS, type StatKind } from './StatCount.vue'

// One project in the list: a link across the row's columns (subgrid of the
// list), and its … button beside it. Phones stack the row into a small card.
// Agents at work (AEON-184) show as a compact live chip at the end of the
// project column (robots and the ticket key; robots only on a phone), beside
// the link rather than in it, over room the name gives up while they work.
const props = defineProps<{
  project: Project; columns: ProjectColumnId[]; term: string; now: number; to: string; label: string
  selected: boolean; dragging: boolean; menuOpen: boolean; showArchived?: boolean
}>()
const emit = defineEmits<{ menu: [anchor: HTMLElement] }>()
const live = useLiveAgents()
const agents = computed(() => live.forProject(props.project.id))
const stat = (id: ProjectColumnId): StatKind | null => id === 'open' || id === 'doing' || id === 'done' ? id : null
const value = (project: Project, kind: StatKind) => kind === 'open' ? project.open : kind === 'doing' ? project.in_progress : project.done
</script>

<template>
  <li class="project-item" :class="{ selected, dragging, menu: menuOpen, archived: project.archived, live: agents.length }" :data-project-id="project.id" draggable="true">
    <RouterLink class="project-row item-link" :to="to" :aria-label="label" aria-describedby="arrange-hint" aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown" draggable="false">
      <span class="key-badge"><template v-for="(part, i) in highlight(project.routeKey, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
      <span class="project-text">
        <span class="project-name">
          <span class="name"><template v-for="(part, i) in highlight(project.title, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
          <span v-if="project.frozen" class="chip state-chip frozen">Frozen</span>
          <span v-else-if="project.state === 'deleted'" class="chip state-chip">Deleted</span>
          <span v-else-if="project.archived && showArchived" class="chip state-chip">Archived</span>
        </span>
        <span class="project-desc">{{ project.description || 'No description' }}</span>
      </span>
      <template v-for="id in columns" :key="id">
        <StatCount v-if="stat(id)" class="stat" :kind="stat(id)!" :value="value(project, stat(id)!)" />
        <span v-else-if="id === 'progress'" class="progress" :data-tip="`${project.done.toLocaleString('en-GB')} of ${(project.total - project.cancelled).toLocaleString('en-GB')} done${project.cancelled ? ` · ${project.cancelled} cancelled` : ''}`">
          <span class="bar"><i :style="{ width: `${project.percent}%` }" /></span>
          <span class="mono pct">{{ project.total ? `${project.percent}%` : '—' }}</span>
        </span>
        <span v-else-if="id === 'people'" class="people-cell"><PeopleStack :people="project.people" :size="20" :max="3" /></span>
        <time v-else class="activity" :datetime="project.last_activity" :data-tip="absoluteTime(project.last_activity)">{{ relativeTime(project.last_activity, { now, long: true }) }}</time>
      </template>
      <span class="stats-line" aria-hidden="true">
        <span v-for="s in STATS" :key="s.kind" class="line-stat"><StatCount :kind="s.kind" :value="value(project, s.kind)" :size="10" class="mini" /><span class="word">{{ s.label.toLowerCase() }}</span></span>
      </span>
    </RouterLink>
    <LiveAgents v-if="agents.length" class="row-live" variant="row" :agents="agents" :project="project" />
    <button
      type="button" class="icon-btn sm flat row-more" :aria-label="`Actions for ${project.routeKey} ${project.title}`" aria-haspopup="menu" :aria-expanded="menuOpen"
      data-tip="Actions · Shift F10" @click="emit('menu', $event.currentTarget as HTMLElement)"
    ><AppIcon name="more" :size="15" /></button>
  </li>
</template>

<style scoped>
/* The row is a subgrid of the list: the link spans every column but the last,
   where the … button sits, so the button never covers a value. */
.project-item { position: relative; display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; margin: 0 6px; padding: 0 14px; border-radius: 10px; }
.project-row { display: grid; grid-template-columns: subgrid; grid-column: 1 / -2; align-items: center; }
@media (hover: hover) { .project-item:hover { background: var(--row-hover); } }
.project-item:has(.item-link:active) { background: var(--row-selected); }
.project-item:has(.item-link:focus-visible) { background: var(--row-selected); box-shadow: var(--focus-ring); }
.project-item.selected { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.project-item.selected:has(.item-link:focus-visible) { box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.project-item.dragging { opacity: .45; }
.project-item.archived .project-row { opacity: .72; }
.project-row { position: relative; min-height: 60px; padding: 8px 0; border-radius: 10px; color: var(--ink); text-decoration: none; }
.project-row:focus-visible { box-shadow: none; }
.key-badge { justify-self: start; }
.project-text { display: grid; gap: 1px; min-width: 0; }
/* The live chip's place: the end of the project column (an absolutely placed
   grid child takes its grid area as containing block). */
.row-live { position: absolute; grid-column: 2 / 3; grid-row: 1; top: 50%; right: 0; translate: 0 -50%; z-index: 1; max-width: 150px; }
.project-item.live .project-text { padding-right: 158px; }
.project-name { display: flex; align-items: center; gap: 8px; min-width: 0; }
.name { font-size: 14.5px; font-weight: 650; letter-spacing: -.005em; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.project-desc { font-size: 13px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.state-chip { height: 18px; padding: 0 7px; font-size: 10px; text-transform: uppercase; letter-spacing: .08em; }
.state-chip.frozen { color: var(--gold-ink); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); }
.stat { justify-self: stretch; }
.stats-line { display: none; }
.progress { display: flex; align-items: center; gap: 10px; }
.progress .bar { flex: 1; }
.pct { width: 36px; font-size: 12px; color: var(--ink-2); text-align: right; }
.people-cell { display: flex; align-items: center; min-height: 22px; }
.activity { font-size: 12.5px; color: var(--ink-2); text-align: right; white-space: nowrap; }
.row-more { grid-column: -2 / -1; justify-self: end; width: 30px; height: 30px; margin-right: -4px; color: var(--ink-3); opacity: 0; }
.project-item:hover .row-more, .project-item:focus-within .row-more, .project-item.menu .row-more { opacity: 1; }
.row-more:hover, .project-item.menu .row-more { color: var(--teal-ink); }
@media (hover: none) { .row-more { opacity: 1; } }
@media (max-width: 760px) {
  .project-item { display: block; margin: 0 4px; padding: 0; }
  .project-row {
    display: grid; grid-template-columns: auto minmax(0, 1fr) auto;
    grid-template-areas: "key key time" "text text text" "bar bar bar" "stats stats stats";
    row-gap: 6px; padding: 12px 48px 12px 10px; min-height: 44px;
  }
  .project-row .key-badge { grid-area: key; }
  .project-text { grid-area: text; }
  .activity { display: block; grid-area: time; font-size: 12px; }
  .progress { grid-area: bar; }
  .project-row > .stat, .people-cell { display: none; }
  .name { white-space: normal; }
  .project-desc { white-space: normal; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .row-more { position: absolute; top: 4px; right: 2px; margin: 0; width: 44px; height: 44px; }
  .row-live { top: auto; right: 8px; bottom: 12px; translate: none; max-width: 66px; }
  .project-item.live .project-text { padding-right: 0; }
  .project-item.live .stats-line { padding-right: 30px; min-height: 32px; align-items: center; }
  .stats-line { grid-area: stats; display: flex; flex-wrap: wrap; gap: 4px 16px; }
  .line-stat { display: inline-flex; align-items: center; gap: 5px; }
  .mini { gap: 5px; font-size: 11.5px; }
  .mini :deep(.stat-num) { margin-left: 0; }
  .word { font: 500 11.5px/1.4 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
}
</style>
