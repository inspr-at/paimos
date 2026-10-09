<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'
import type { SavedView } from '../../lib/api'
import AppIcon from '../AppIcon.vue'
import type { ProjectTab } from './projectNavigation'

// What the collapsed project header hides, as two blocks at the top of the Display
// menu: the project's sections and the saved views. Anything that needs a popover
// of its own (a view's options, saving a view) is handed to the page, which opens
// it under the Display button once this menu has closed.
const props = defineProps<{
  sections: readonly ProjectTab[]
  section: string
  attention: RouteLocationRaw
  views: SavedView[]
  activeId: string | null
  dirty: boolean
  defaultId: string | null
  me: string | null
  hrefFor: (id: string | null) => string
  // The plain list differs from its defaults: offer to keep it as a view.
  canSaveNew?: boolean
}>()
const emit = defineEmits<{
  section: [id: string]
  attention: []
  view: [id: string | null]
  viewMenu: []
  saveAs: []
}>()
const mine = (view: SavedView) => view.owner_principal_id === props.me
function open(event: MouseEvent, id: string | null) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.button === 1) return
  event.preventDefault()
  emit('view', id)
}
const tip = (view: SavedView) => view.shared && !mine(view) ? 'Shared with the project' : view.shared ? 'Yours, shared with the project' : undefined
</script>

<template>
  <nav class="nav-sections" aria-label="Project sections">
    <button
      v-for="item in sections" :key="item.id" type="button" class="nav-item" :aria-current="item.id === section ? 'page' : undefined"
      :data-autofocus="item.id === section ? '' : undefined" @click="emit('section', item.id)"
    ><AppIcon :name="item.icon" :size="15" />{{ item.label }}</button>
    <RouterLink class="nav-item" :to="attention" @click="emit('attention')"><AppIcon name="flag" :size="15" />Needs attention</RouterLink>
  </nav>
  <nav class="nav-views" aria-label="Saved views">
    <p class="eyebrow">Saved views</p>
    <ul class="rows">
      <li class="row">
        <a class="view-row" :href="hrefFor(null)" :aria-current="activeId ? undefined : 'page'" @click="open($event, null)">
          <AppIcon name="list" :size="13" class="lead" /><span v-clip-tip class="name">All tickets</span>
        </a>
        <button v-if="!activeId && canSaveNew" type="button" class="row-action" aria-label="Save view" data-tip="Keep this list as a view of the project" @click="emit('saveAs')"><AppIcon name="bookmark" :size="13" /></button>
      </li>
      <li v-for="view in views" :key="view.id" class="row">
        <a class="view-row" :href="hrefFor(view.id)" :aria-current="view.id === activeId ? 'page' : undefined" :data-tip="tip(view)" @click="open($event, view.id)">
          <AppIcon v-if="view.id === defaultId" name="star" :size="12" class="lead star" />
          <span v-clip-tip class="name">{{ view.name }}</span>
          <AppIcon v-if="view.shared" name="users" :size="12" class="shared" />
          <span v-if="view.id === activeId" :style="{ visibility: dirty ? 'visible' : 'hidden' }" class="dirty" role="img" aria-label="changed since saved" />
        </a>
        <button v-if="view.id === activeId" type="button" class="row-action" :aria-label="`Options for view ${view.name}`" aria-haspopup="menu" @click="emit('viewMenu')"><AppIcon name="chevron" :size="12" /></button>
      </li>
    </ul>
  </nav>
</template>

<style scoped>
.nav-sections { display: flex; flex-wrap: wrap; gap: 4px; }
.nav-item { display: inline-flex; align-items: center; gap: 6px; min-height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; white-space: nowrap; }
.nav-item svg { flex-shrink: 0; }
@media (hover: hover) { .nav-item:hover { color: var(--ink); background: var(--row-hover); } }
.nav-item[aria-current="page"] { color: var(--teal-ink); background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--line); }
.nav-item:focus-visible, .view-row:focus-visible, .row-action:focus-visible { box-shadow: var(--focus-ring); }
.nav-views .eyebrow { margin: 0 0 4px 2px; }
/* One track as wide as the column, so a long view name clips instead of widening the menu.
   In the collapsed header's desktop menu this component's own display would keep the
   list one block (scoped rules beat the menu). There the rows join its columns; the
   sheet keeps the list whole and scrolls. */
.rows { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; margin: 0; padding: 0; list-style: none; }
:global(.display-menu:not(.as-sheet)) .nav-views,
:global(.display-menu:not(.as-sheet)) .rows { display: contents; }
.row { display: flex; align-items: center; gap: 2px; border-radius: 8px; }
.view-row { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; height: 34px; padding: 0 10px; border-radius: 8px; color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; }
@media (hover: hover) { .view-row:hover { color: var(--ink); background: var(--row-hover); } }
.view-row[aria-current="page"] { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.view-row[aria-current="page"]:focus-visible { box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.name { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.lead { flex-shrink: 0; color: var(--ink-3); }
.view-row[aria-current="page"] .lead { color: var(--teal-ink); }
.star { color: var(--secondary-line); }
.shared { flex-shrink: 0; color: var(--ink-3); }
.dirty { flex-shrink: 0; width: 7px; height: 7px; border-radius: 50%; background: var(--teal); box-shadow: 0 0 0 2px var(--chip-teal-bg); }
.row-action { display: grid; place-items: center; flex: none; width: 34px; height: 34px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); }
@media (hover: hover) { .row-action:hover { color: var(--ink); background: var(--row-hover); } }
/* Phones and touch: 44 px rows and targets. */
@media (pointer: coarse), (max-width: 900px) {
  .nav-item { min-height: 44px; }
  .view-row { height: 44px; }
  .row-action { width: 44px; height: 44px; }
}
</style>
