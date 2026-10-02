<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { SavedView } from '../../lib/api'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

// The project's saved views as one row of links: the plain list first, then the
// person's own and the shared views in the order they were made. The open view
// carries its menu; a dot says the list has changed since the view was saved.
const props = defineProps<{
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
  open: [id: string | null]
  save: [view: SavedView]
  saveAs: [anchor: HTMLElement]
  reset: []
  rename: [view: SavedView, anchor: HTMLElement]
  duplicate: [view: SavedView]
  setDefault: [view: SavedView | null]
  share: [view: SavedView, shared: boolean]
  copyLink: [view: SavedView]
  remove: [view: SavedView]
}>()
const active = computed(() => props.views.find(view => view.id === props.activeId) ?? null)
const mine = (view: SavedView) => view.owner_principal_id === props.me
const menu = ref<{ view: SavedView; anchor: HTMLElement } | null>(null)
const strip = ref<HTMLElement>()
const edges = ref({ start: false, end: false })
function measure() {
  const el = strip.value
  if (!el) return
  edges.value = { start: el.scrollLeft > 2, end: el.scrollLeft + el.clientWidth < el.scrollWidth - 2 }
}
// The open view scrolls into sight when it is off the edge of a narrow bar.
watch(() => props.activeId, async () => {
  await nextTick()
  strip.value?.querySelector<HTMLElement>('[aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  measure()
}, { immediate: true })
watch(() => props.views.length, () => void nextTick(measure))
function openMenu(view: SavedView, anchor: HTMLElement) { menu.value = menu.value?.view.id === view.id ? null : { view, anchor } }
function closeMenu(restore: boolean) {
  const anchor = menu.value?.anchor
  menu.value = null
  if (restore) anchor?.focus()
}
function act(run: (open: { view: SavedView; anchor: HTMLElement }) => void) {
  const open = menu.value
  if (!open) return
  menu.value = null
  run(open)
  void nextTick(() => { if (document.activeElement === document.body && open.anchor.isConnected) open.anchor.focus() })
}
function click(event: MouseEvent, id: string | null) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.button === 1) return
  event.preventDefault()
  emit('open', id)
}
const list = ref<HTMLElement>()
function move(event: KeyboardEvent) {
  const items = [...(list.value?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? [])]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  let next = -1
  if (event.key === 'ArrowDown' || event.key === 'j') next = Math.min(items.length - 1, index + 1)
  else if (event.key === 'ArrowUp' || event.key === 'k') next = Math.max(0, index - 1)
  if (next >= 0) { event.preventDefault(); event.stopPropagation(); items[next]?.focus() }
}
defineExpose({ openMenuFor: (anchor: HTMLElement) => { if (active.value) openMenu(active.value, anchor) } })
</script>

<template>
  <nav class="view-bar" aria-label="Saved views">
    <div ref="strip" class="strip" :class="{ 'fade-start': edges.start, 'fade-end': edges.end }" @scroll.passive="measure">
      <a class="view-tab plain" :href="hrefFor(null)" :aria-current="activeId ? undefined : 'page'" @click="click($event, null)">
        <AppIcon name="list" :size="13" class="lead" /><span class="name">All tickets</span>
      </a>
      <span v-for="view in views" :key="view.id" class="tab" :class="{ active: view.id === activeId }">
        <a
          class="view-tab" :href="hrefFor(view.id)" :aria-current="view.id === activeId ? 'page' : undefined"
          :data-tip="view.shared && !mine(view) ? 'Shared with the project' : view.shared ? 'Yours, shared with the project' : undefined"
          @click="click($event, view.id)" @contextmenu.prevent="openMenu(view, $event.currentTarget as HTMLElement)"
        >
          <AppIcon v-if="view.id === defaultId" name="star" :size="12" class="lead star" />
          <span class="name">{{ view.name }}</span>
          <AppIcon v-if="view.shared" name="users" :size="12" class="shared" />
          <span :style="{ visibility: view.id === activeId && dirty ? 'visible' : 'hidden' }" class="dirty" role="img" aria-label="changed since saved" />
        </a>
        <button
          :style="{ visibility: view.id === activeId ? 'visible' : 'hidden' }" :tabindex="view.id === activeId ? 0 : -1" type="button" class="tab-menu" :aria-label="`Options for view ${view.name}`" aria-haspopup="menu" :aria-expanded="menu?.view.id === view.id"
          @click="openMenu(view, $event.currentTarget as HTMLElement)"
        ><AppIcon name="chevron" :size="12" /></button>
      </span>
    </div>
    <div v-if="active && dirty" class="changes">
      <button type="button" class="btn sm ghost reset" data-tip="Back to the view as saved" @click="emit('reset')">Reset</button>
      <button v-if="mine(active)" type="button" class="btn sm save" aria-label="Save changes to the view" @click="emit('save', active)"><AppIcon name="check" :size="13" /><span class="save-label">Save</span></button>
      <button v-else type="button" class="btn sm save" aria-label="Save as new view" @click="emit('saveAs', $event.currentTarget as HTMLElement)"><AppIcon name="bookmark" :size="13" /><span class="save-label">Save as new</span></button>
    </div>
    <div v-else-if="!active && canSaveNew" class="changes">
      <button type="button" class="btn sm save" aria-label="Save view" data-tip="Keep this list as a view of the project" @click="emit('saveAs', $event.currentTarget as HTMLElement)"><AppIcon name="bookmark" :size="13" /><span class="save-label">Save view</span></button>
    </div>

    <FloatingPanel v-if="menu" :anchor="menu.anchor" :width="244" :label="`View ${menu.view.name}`" @close="closeMenu">
      <div ref="list" class="menu" role="menu" :aria-label="`View ${menu.view.name}`" @keydown="move">
        <template v-if="menu.view.id === activeId && dirty">
          <button v-if="mine(menu.view)" type="button" role="menuitem" class="menu-item" data-autofocus @click="act(m => emit('save', m.view))"><AppIcon name="check" :size="14" />Save changes</button>
          <button type="button" role="menuitem" class="menu-item" @click="act(m => emit('saveAs', m.anchor))"><AppIcon name="bookmark" :size="14" />Save as new view</button>
          <button type="button" role="menuitem" class="menu-item" @click="act(() => emit('reset'))"><AppIcon name="rollback" :size="14" />Reset changes</button>
          <span class="divider" role="separator" />
        </template>
        <button v-if="mine(menu.view)" type="button" role="menuitem" class="menu-item" :data-autofocus="dirty ? undefined : ''" @click="act(m => emit('rename', m.view, m.anchor))"><AppIcon name="edit" :size="14" />Rename</button>
        <button type="button" role="menuitem" class="menu-item" @click="act(m => emit('duplicate', m.view))"><AppIcon name="copy" :size="14" />{{ mine(menu.view) ? 'Duplicate' : 'Copy to my views' }}</button>
        <button v-if="menu.view.id !== defaultId" type="button" role="menuitem" class="menu-item" @click="act(m => emit('setDefault', m.view))"><AppIcon name="star" :size="14" />Open the project with it</button>
        <button v-else type="button" role="menuitem" class="menu-item" @click="act(() => emit('setDefault', null))"><AppIcon name="star" :size="14" />Stop opening with it</button>
        <button v-if="mine(menu.view)" type="button" role="menuitem" class="menu-item" @click="act(m => emit('share', m.view, !m.view.shared))"><AppIcon name="users" :size="14" />{{ menu.view.shared ? 'Make it private' : 'Share with the project' }}</button>
        <button type="button" role="menuitem" class="menu-item" @click="act(m => emit('copyLink', m.view))"><AppIcon name="link" :size="14" />Copy link</button>
        <template v-if="mine(menu.view)">
          <span class="divider" role="separator" />
          <button type="button" role="menuitem" class="menu-item danger" @click="act(m => emit('remove', m.view))"><AppIcon name="trash" :size="14" />Delete view</button>
        </template>
      </div>
    </FloatingPanel>
  </nav>
</template>

<style scoped>
.view-bar { display: flex; align-items: center; gap: 10px; min-width: 0; padding: 2px 0 6px; }
.strip { display: flex; align-items: center; gap: 4px; flex: 1 1 auto; min-width: 0; overflow-x: auto; overscroll-behavior-x: contain; scrollbar-width: none; padding: 3px 2px; }
.strip::-webkit-scrollbar { display: none; }
/* Views past the edge fade out, so a long row reads as scrollable, not cut. */
.strip.fade-end { -webkit-mask-image: linear-gradient(to left, transparent, #000 28px); mask-image: linear-gradient(to left, transparent, #000 28px); }
.strip.fade-start { -webkit-mask-image: linear-gradient(to right, transparent, #000 28px); mask-image: linear-gradient(to right, transparent, #000 28px); }
.strip.fade-start.fade-end { -webkit-mask-image: linear-gradient(to right, transparent, #000 28px, #000 calc(100% - 28px), transparent); mask-image: linear-gradient(to right, transparent, #000 28px, #000 calc(100% - 28px), transparent); }
.tab { display: inline-flex; align-items: center; flex-shrink: 0; border-radius: 999px; }
.view-tab { display: inline-flex; align-items: center; gap: 6px; flex-shrink: 0; max-width: 260px; height: 28px; padding: 0 4px 0 12px; border-radius: 999px; color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; white-space: nowrap; }
.view-tab .name { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.view-tab:hover { color: var(--ink); background: var(--row-hover); }
.view-tab:focus-visible, .tab-menu:focus-visible { box-shadow: var(--focus-ring); }
.view-tab[aria-current="page"] { color: var(--teal-ink); font-weight: 600; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.view-tab[aria-current="page"]:focus-visible { box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.tab.active { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.tab.active .view-tab { background: transparent; box-shadow: none; padding-right: 4px; }
.tab-menu { display: grid; place-items: center; width: 24px; height: 24px; margin-right: 3px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--teal-ink); }
.tab-menu:hover, .tab-menu[aria-expanded="true"] { background: rgba(14, 111, 108, .12); }
.lead { flex-shrink: 0; color: var(--ink-3); }
.view-tab[aria-current="page"] .lead { color: var(--teal-ink); }
.star { color: var(--gold); }
.view-tab[aria-current="page"] .star { color: var(--gold); }
.shared { flex-shrink: 0; color: var(--ink-3); }
.dirty { flex-shrink: 0; width: 7px; height: 7px; border-radius: 50%; background: var(--teal); box-shadow: 0 0 0 2px var(--chip-teal-bg); }
.changes { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.changes .btn { gap: 6px; }
.save { color: var(--teal-ink); font-weight: 600; }
.reset { color: var(--ink-2); }
.menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item svg { flex-shrink: 0; color: var(--ink-3); }
@media (hover: hover) { .menu-item:hover { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item.danger, .menu-item.danger svg { color: var(--danger); }
.divider { height: 1px; margin: 4px 6px; background: var(--line); }
/* Phones: every control is 40 px with a 44 px reach (a 2 px ring that still
   hits the control), 6 px apart, and the bar keeps clear of the toolbar below.
   The strip's own 3 px padding holds the ring, so its scroll box never clips it. */
@media (max-width: 600px) {
  .view-bar { gap: 6px; padding: 0 0 8px; }
  .strip { gap: 6px; }
  .view-tab { position: relative; height: 40px; padding: 0 4px 0 14px; font-size: 14px; }
  .view-tab::after { content: ''; position: absolute; inset: -2px 0; border-radius: inherit; }
  .tab.active .view-tab { padding-right: 4px; }
  .tab-menu { position: relative; width: 40px; height: 40px; margin-right: 0; }
  .tab-menu::after { content: ''; position: absolute; inset: -2px; border-radius: 50%; }
  .changes .reset { display: none; }
  .changes .save { position: relative; width: 40px; height: 40px; padding: 0; justify-content: center; }
  .changes .save::after { content: ''; position: absolute; inset: -2px; border-radius: inherit; }
  .save-label { display: none; }
}
</style>
