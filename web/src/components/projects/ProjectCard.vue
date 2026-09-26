<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { Project } from '../../stores/projects'
import { useLiveAgents } from '../../stores/liveAgents'
import { absoluteTime, highlight, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import LiveAgents from './LiveAgents.vue'
import PeopleStack from './PeopleStack.vue'
import ProgressRing from './ProgressRing.vue'
import StatCount from './StatCount.vue'

// One project as a card: key, name, a line of description, progress as a ring,
// Open, Doing and Done with their icons in one straight line, who was active
// lately and when. The card is a link; its … button opens the same menu as a row.
// Dragging a card (the page does it) arranges your own order: while carried, a
// lifted copy follows the pointer and the card itself marks where it will land.
// While agents work in the project (AEON-184) their activity rings turn in the
// footer, over the people who were active lately (kept, hidden, so the card
// never changes size); the chip is the link's sibling, so it can be a button.
const props = defineProps<{ project: Project; term: string; now: number; to: string; label: string; selected: boolean; dragging: boolean; menuOpen: boolean }>()
const emit = defineEmits<{ menu: [anchor: HTMLElement] }>()
const live = useLiveAgents()
const agents = computed(() => live.forProject(props.project.id))
</script>

<template>
  <li class="card" :class="{ selected, dragging, menu: menuOpen, archived: project.archived, live: agents.length }" :data-project-id="project.id">
    <RouterLink class="card-link item-link" :to="to" :aria-label="label" aria-describedby="arrange-hint" aria-keyshortcuts="Alt+ArrowLeft Alt+ArrowRight Alt+ArrowUp Alt+ArrowDown" draggable="false">
      <span class="card-top">
        <span class="key-badge"><template v-for="(part, i) in highlight(project.routeKey, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
        <span v-if="project.frozen" class="chip state-chip frozen">Frozen</span>
        <span v-else-if="project.state === 'deleted'" class="chip state-chip">Deleted</span>
      </span>
      <span class="card-name"><template v-for="(part, i) in highlight(project.title, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
      <span class="card-desc">{{ project.description || 'No description' }}</span>
      <span class="card-mid">
        <span class="ring-wrap" :data-tip="`${project.done.toLocaleString('en-GB')} of ${(project.total - project.cancelled).toLocaleString('en-GB')} done${project.cancelled ? ` · ${project.cancelled} cancelled` : ''}`">
          <ProgressRing :percent="project.percent" :empty="!project.total" :size="56" />
        </span>
        <span class="counts">
          <StatCount kind="open" :value="project.open" label />
          <StatCount kind="doing" :value="project.in_progress" label />
          <StatCount kind="done" :value="project.done" label />
        </span>
      </span>
      <span class="card-foot">
        <PeopleStack :people="project.people" :size="22" :class="{ 'under-live': agents.length }" />
        <time class="activity" :datetime="project.last_activity" :data-tip="absoluteTime(project.last_activity)">{{ relativeTime(project.last_activity, { now, long: true }) }}</time>
      </span>
    </RouterLink>
    <LiveAgents v-if="agents.length" class="card-live" :agents="agents" :project="project" />
    <span class="card-grip" aria-hidden="true" data-tip="Drag to arrange · Alt and the arrow keys"><AppIcon name="grip" :size="12" /></span>
    <button
      type="button" class="icon-btn sm flat card-more" :aria-label="`Actions for ${project.routeKey} ${project.title}`" aria-haspopup="menu" :aria-expanded="menuOpen"
      data-tip="Actions · Shift F10" @click="emit('menu', $event.currentTarget as HTMLElement)"
    ><AppIcon name="more" :size="15" /></button>
  </li>
</template>

<style scoped>
.card {
  position: relative; display: flex; min-width: 0; border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%); box-shadow: var(--shadow);
  -webkit-backdrop-filter: blur(18px) saturate(1.15); backdrop-filter: blur(18px) saturate(1.15);
}
@media (hover: hover) { .card:hover { box-shadow: var(--shadow), 0 16px 32px -22px rgba(16, 35, 39, .45); } }
@media (hover: hover) and (prefers-reduced-motion: no-preference) {
  .card { transition: box-shadow .18s ease, transform .18s ease; }
  .card:hover { transform: translateY(-1px); }
}
.card:has(.card-link:focus-visible) { box-shadow: var(--focus-ring), var(--shadow); }
.card.selected { background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%), var(--row-selected); box-shadow: 0 0 0 1.5px var(--teal), var(--shadow); }
.card.selected:has(.card-link:focus-visible) { box-shadow: 0 0 0 1.5px var(--teal), var(--focus-ring), var(--shadow); }
/* The open place a carried card will land in: a calm aqua well, contents hidden. */
.card.dragging { border-color: transparent; background: var(--row-selected); box-shadow: inset 0 0 0 1.5px var(--chip-teal-line); -webkit-backdrop-filter: none; backdrop-filter: none; }
.card.dragging > *, .card.dragging::before { visibility: hidden; }
.card.archived .card-link { opacity: .72; }
.card { -webkit-user-select: none; user-select: none; }
.card-link { display: flex; flex-direction: column; flex: 1; min-width: 0; padding: 16px 18px 14px; border-radius: inherit; color: var(--ink); text-decoration: none; }
.card-link:focus-visible { box-shadow: none; }
.card-top { display: flex; align-items: center; gap: 8px; min-height: 22px; padding-right: 56px; }
.state-chip { height: 18px; padding: 0 7px; font-size: 10px; text-transform: uppercase; letter-spacing: .08em; }
.state-chip.frozen { color: var(--gold-ink); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); }
.card-name { margin-top: 12px; font-size: 16px; font-weight: 650; letter-spacing: -.01em; line-height: 1.3; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.card-desc { margin-top: 3px; font-size: 13px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.card-mid { display: flex; align-items: center; gap: 20px; margin-top: 18px; }
.ring-wrap { display: inline-flex; }
.counts { display: grid; gap: 7px; width: min(100%, 176px); }
.counts :deep(.stat-count) { gap: 8px; }
/* Always as tall as a row of faces, so the live chip sits on the same line in every card. */
.card-foot { display: flex; align-items: center; gap: 10px; min-height: 35px; margin-top: 18px; padding-top: 12px; border-top: 1px solid var(--line); }
.under-live { visibility: hidden; }
/* The live chip rides the footer line, left, leaving the time its place. */
.card { container: live-card / inline-size; }
.card-live { position: absolute; left: 15px; bottom: 10px; z-index: 2; max-width: calc(100% - 30px - 92px); }
.activity { margin-left: auto; font-size: 12.5px; color: var(--ink-2); white-space: nowrap; }
.card-more { position: absolute; top: 12px; right: 12px; width: 30px; height: 30px; color: var(--ink-3); opacity: 0; }
.card:hover .card-more, .card:focus-within .card-more, .card.menu .card-more { opacity: 1; }
/* Beside the … button on hover, a grip says the card can be dragged. */
.card-grip { position: absolute; top: 12px; right: 42px; display: grid; place-items: center; width: 18px; height: 30px; color: var(--ink-3); cursor: grab; opacity: 0; }
@media (hover: hover) { .card:hover .card-grip { opacity: 1; } }
@media (hover: hover) and (prefers-reduced-motion: no-preference) { .card-grip, .card-more { transition: opacity .15s ease; } }
.card-more:hover, .card.menu .card-more { color: var(--teal-ink); }
@media (hover: none) { .card-more { opacity: 1; } .card-grip { display: none; } }
@media (max-width: 600px) { .card-link { padding: 14px 16px 12px; } .card-top { padding-right: 40px; } .card-more { top: 6px; right: 6px; width: 44px; height: 44px; } .card-live { left: 13px; bottom: 8px; max-width: calc(100% - 26px - 88px); } }
</style>

<style>
/* The lifted copy of a carried card (lives on <body>, so not scoped). */
li.card.card-ghost { position: fixed; z-index: 80; margin: 0; list-style: none; pointer-events: none; will-change: translate; }
li.card.card-ghost.lifted { scale: 1.025; box-shadow: 0 0 0 1px var(--chip-teal-line), 0 30px 60px -22px rgba(8, 24, 27, .46), 0 10px 22px -12px rgba(8, 24, 27, .3); }
li.card.card-ghost.dropped { opacity: 0; scale: .96; }
.card-ghost .card-more, .card-ghost .card-grip { display: none; }
.card-ghost-count { position: absolute; top: -9px; right: -9px; display: grid; place-items: center; min-width: 24px; height: 24px; padding: 0 7px; border-radius: 999px; background: #0e6f6c; color: #fff; font: 700 12px/1 var(--font); box-shadow: 0 0 0 2px var(--surface-raised-2), 0 4px 10px -4px rgba(8, 24, 27, .4); }
@media (prefers-reduced-motion: no-preference) {
  li.card.card-ghost { transition: scale .16s ease, box-shadow .16s ease, opacity .16s ease; }
  li.card.card-ghost.settling { transition: translate .22s cubic-bezier(.2, .75, .3, 1), scale .22s ease, box-shadow .22s ease; }
}
html.arranging-cards, html.arranging-cards * { cursor: grabbing !important; }
</style>
