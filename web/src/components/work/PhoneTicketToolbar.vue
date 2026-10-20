<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { SavedView } from '../../lib/api'
import type { ListFilters } from '../../lib/ticketList'
import { TICKET_VIEWS, type TicketView } from './projectNavigation'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

const props = defineProps<{
  filters: ListFilters; query: string; view: TicketView; views: SavedView[]; total: number | null
  selectable: boolean; picking: boolean; createLabel: string; attentionHref: string; principalId: string | null
}>()
const emit = defineEmits<{
  search: [value: string]; filter: []; create: []; layout: [value: string]; savedView: [id: string]
  preset: [id: string]; display: [anchor: HTMLElement]; showClosed: [value: boolean]
  select: []; save: [anchor: HTMLElement]; copy: []; export: []
}>()
const input = ref<HTMLInputElement>()
const searchOverlay = ref<HTMLElement>()
const searching = ref(false)
const menu = ref<{ kind: 'views' | 'more'; anchor: HTMLElement } | null>(null)
const activeView = computed(() => props.views.find(view => view.id === props.filters.view))
const preset = computed(() => props.filters.statusScope === 'done' && props.filters.date ? 'done'
  : props.filters.status.length === 1 && props.filters.status[0] === 'qa' ? 'qa'
    : props.principalId && props.filters.assignee.includes(props.principalId) && props.filters.assignee.includes('none') && props.filters.assignee.length === 2 ? 'mine' : 'all')
const presets = [{ id: 'mine', label: 'My open' }, { id: 'all', label: 'All open' }, { id: 'qa', label: 'In QA' }, { id: 'done', label: 'Recently done' }]
const name = computed(() => activeView.value?.name ?? presets.find(item => item.id === preset.value)!.label)
const filterCount = computed(() => (['status', 'priority', 'assignee', 'type', 'tag', 'epic', 'cost', 'release', 'human_check', 'shape', 'depth'] as const)
  .reduce((sum, key) => sum + props.filters[key].length, 0) + (props.filters.date ? 1 : 0))
function close(restore = false) {
  const anchor = menu.value?.anchor
  menu.value = null
  if (restore) anchor?.focus({ preventScroll: true })
}
function open(kind: 'views' | 'more', anchor: HTMLElement) {
  menu.value = menu.value?.kind === kind ? null : { kind, anchor }
}
function act(run: (anchor: HTMLElement) => void) {
  const anchor = menu.value?.anchor
  close(true)
  if (anchor) run(anchor)
}
async function focusSearch() {
  close()
  searching.value = true
  await nextTick()
  input.value?.focus({ preventScroll: true })
  input.value?.select()
}
function closeSearch() {
  searching.value = false
  void nextTick(() => searchButton.value?.focus({ preventScroll: true }))
}
const searchButton = ref<HTMLButtonElement>()
function searchEscape(event: KeyboardEvent) {
  event.preventDefault()
  if (event.target === input.value) { input.value?.blur(); searchOverlay.value?.focus({ preventScroll: true }) }
  else closeSearch()
}
function menuKeys(event: KeyboardEvent) {
  if (event.ctrlKey || event.metaKey || event.altKey || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('button:not(:disabled), a[href]')]
  const index = items.indexOf(document.activeElement as HTMLElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
  event.preventDefault(); event.stopPropagation(); items[next]?.focus()
}
watch(() => [props.attentionHref, props.principalId], () => { close(); searching.value = false })
defineExpose({ focusSearch, input, closeOverlays: () => { close(); searching.value = false } })
</script>

<template>
  <div class="phone-ticket-tools">
    <button type="button" class="phone-view" :aria-label="`View: ${name}. Change the view`" aria-haspopup="menu" :aria-expanded="menu?.kind === 'views'" @click="open('views', $event.currentTarget as HTMLElement)">
      <AppIcon :name="TICKET_VIEWS.find(item => item.id === view)?.icon ?? 'list'" :size="16" /><span v-clip-tip="name" class="view-name">{{ name }}</span><AppIcon name="chevron" :size="12" />
    </button>
    <span class="grow" />
    <span class="phone-count" role="status" :aria-label="total === null ? 'Counting tickets' : `${total}\u00a0tickets`">{{ total === null ? '' : `${total}\u00a0tickets` }}</span>
    <button ref="searchButton" type="button" class="phone-action" :class="{ on: !!query }" aria-label="Search tickets" :aria-expanded="searching" @click="focusSearch"><AppIcon name="search" :size="18" /></button>
    <button type="button" class="phone-action phone-filter" :class="{ on: filterCount > 0 }" :aria-label="filterCount ? `Filter: ${filterCount} applied` : 'Filter tickets'" aria-haspopup="dialog" @click="close(); emit('filter')"><AppIcon name="filter" :size="16" /><span v-if="filterCount" class="filter-count" aria-hidden="true">{{ filterCount }}</span></button>
    <button type="button" class="phone-action phone-new" :aria-label="createLabel" @click="emit('create')"><AppIcon name="plus" :size="18" /></button>
    <button type="button" class="phone-action" aria-label="More view actions" aria-haspopup="menu" :aria-expanded="menu?.kind === 'more'" @click="open('more', $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="18" /></button>
    <div v-if="searching" ref="searchOverlay" class="phone-search" role="search" tabindex="-1" @keydown.esc.stop="searchEscape">
      <label class="search-field"><AppIcon name="search" :size="16" /><input ref="input" class="field" type="search" :value="query" placeholder="Search tickets" aria-label="Search tickets in this project" autocomplete="off" spellcheck="false" @input="emit('search', ($event.target as HTMLInputElement).value)" /></label>
      <button type="button" @click="closeSearch">Cancel</button>
    </div>
    <FloatingPanel v-if="menu" :anchor="menu.anchor" :label="menu.kind === 'views' ? 'Ticket views' : 'More view actions'" :width="320" :tallest="680" align="end" phone-sheet :sheet-below="359" @close="close">
      <div class="phone-view-menu" role="menu" :aria-label="menu.kind === 'views' ? 'Ticket views' : 'More view actions'" @keydown="menuKeys">
        <template v-if="menu.kind === 'views'">
          <p class="menu-heading">Saved views</p>
          <button v-for="item in presets" :key="item.id" type="button" role="menuitemradio" :aria-checked="!activeView && preset === item.id" @click="act(() => emit('preset', item.id))"><span class="check-slot"><AppIcon v-if="!activeView && preset === item.id" name="check" :size="16" /></span><span>{{ item.label }}</span></button>
          <button v-for="item in views" :key="item.id" type="button" role="menuitemradio" :aria-checked="activeView?.id === item.id" @click="act(() => emit('savedView', item.id))"><span class="check-slot"><AppIcon v-if="activeView?.id === item.id" name="check" :size="16" /></span><span v-clip-tip="item.name" class="saved-name">{{ item.name }}</span></button>
          <p class="menu-heading divided">Layout</p>
          <button v-for="item in TICKET_VIEWS" :key="item.id" type="button" role="menuitemradio" :aria-checked="view === item.id" @click="act(() => emit('layout', item.id))"><AppIcon :name="item.icon" :size="16" /><span>{{ item.label }}</span><AppIcon v-if="view === item.id" name="check" :size="14" class="chosen" /></button>
          <RouterLink :to="attentionHref" role="menuitem" class="divided" @click="close()"><AppIcon name="flag" :size="16" /><span>Needs attention</span></RouterLink>
        </template>
        <template v-else>
          <button type="button" role="menuitem" @click="act(anchor => emit('display', anchor))"><AppIcon name="layers" :size="16" /><span>Display…</span></button>
          <button type="button" role="menuitemcheckbox" :aria-checked="!filters.showClosed" @click="act(() => emit('showClosed', !filters.showClosed))"><AppIcon name="eye-off" :size="16" /><span>Hide done</span><AppIcon v-if="!filters.showClosed" name="check" :size="14" class="chosen" /></button>
          <button type="button" role="menuitem" :disabled="!selectable" @click="act(() => emit('select'))"><AppIcon name="circle-check" :size="16" /><span>{{ picking ? 'Cancel selection' : 'Select tickets' }}</span></button>
          <button type="button" role="menuitem" @click="act(anchor => emit('save', anchor))"><AppIcon name="bookmark" :size="16" /><span>Save view as…</span></button>
          <button type="button" role="menuitem" @click="act(() => emit('copy'))"><AppIcon name="link" :size="16" /><span>Copy link</span></button>
          <button type="button" role="menuitem" @click="act(() => emit('export'))"><AppIcon name="download" :size="16" /><span>Export CSV</span></button>
        </template>
      </div>
    </FloatingPanel>
  </div>
</template>

<style scoped>
.phone-ticket-tools { position: relative; display: flex; align-items: center; width: 100%; min-width: 0; height: 44px; gap: 0; background: var(--chrome); }
button, a { -webkit-tap-highlight-color: transparent; }
.phone-action, .phone-view { display: inline-flex; align-items: center; justify-content: center; flex: none; min-width: 44px; height: 44px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); }
.phone-action { width: 44px; }
.phone-view { flex: 1 1 0; min-width: 44px; gap: 6px; padding: 0 6px; justify-content: flex-start; color: var(--ink); font-size: 15px; font-weight: 600; }
.phone-view svg { flex: none; }
.view-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.grow { flex: 0; }
.phone-count { display: none; flex: none; width: 24ch; text-align: right; color: var(--ink-2); font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
@media (min-width: 720px) and (max-height: 500px) { .phone-count { display: block; margin-right: 8px; } }
.phone-action.on { color: var(--teal-ink); }
.phone-filter { position: relative; }
.filter-count { position: absolute; top: 2px; right: 1px; display: grid; place-items: center; min-width: 16px; height: 16px; padding: 0 3px; border-radius: 50%; background: var(--teal); color: var(--primary-on); font-size: 10px; font-weight: 700; }
.phone-new { position: relative; color: var(--primary-on); isolation: isolate; }
.phone-new::before { position: absolute; z-index: -1; inset: 5px; content: ''; border-radius: 50%; background: var(--primary); box-shadow: inset 0 0 0 1px var(--primary-line); }
.phone-action:hover, .phone-view:hover, .phone-view[aria-expanded="true"], .phone-action[aria-expanded="true"] { background: var(--row-hover); }
button:focus-visible, a:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.phone-search { position: absolute; z-index: 1; inset: 0; display: flex; align-items: center; gap: 6px; background: var(--chrome); }
.phone-search .search-field { flex: 1; min-width: 0; }
.phone-search input { width: 100%; height: 44px; font-size: 16px; }
.phone-search > button { flex: none; min-width: 64px; height: 44px; padding: 0 8px; border: 0; background: transparent; color: var(--teal-ink); font-size: 14px; font-weight: 600; }
.phone-view-menu { display: grid; }
.menu-heading { padding: 6px 10px; margin: 0; color: var(--ink-3); font-size: 12px; font-weight: 600; }
.phone-view-menu :is(button, a) { display: flex; align-items: center; gap: 10px; min-width: 44px; min-height: 44px; padding: 0 10px; border: 0; background: transparent; color: var(--ink); text-decoration: none; font-size: 14px; text-align: left; }
.phone-view-menu :is(button, a):hover { background: var(--row-hover); }
.phone-view-menu button:disabled { color: var(--ink-3); }
.phone-view-menu svg, .check-slot { flex: none; width: 16px; }
.check-slot { display: inline-grid; place-items: center; }
.chosen { margin-left: auto; }
.saved-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.phone-view-menu .divided { border-top: 1px solid var(--line); }
</style>
