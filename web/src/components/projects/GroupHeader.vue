<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, useId, watch } from 'vue'
import type { GroupDef } from '../../lib/projectGroups'
import AppIcon from '../AppIcon.vue'
import GroupMarker from './GroupMarker.vue'

// A group's header in the list and among the cards: fold it with the chevron
// (or Enter), move it with Alt and the arrow keys or by its grip, and reach
// rename, share, hide and delete under …. Renaming happens in place.
const props = defineProps<{
  group: GroupDef; count: number; collapsed: boolean; variant: 'list' | 'cards'; controls: string
  renaming?: boolean; menuOpen?: boolean; caret?: boolean; draggable?: boolean; validate?: (name: string) => string
}>()
const emit = defineEmits<{ toggle: []; step: [delta: -1 | 1]; menu: [anchor: HTMLElement]; rename: [name: string | null] }>()
const id = useId()
const input = ref<HTMLInputElement>()
const draft = ref('')
const problem = ref('')
const fold = ref<HTMLButtonElement>()
watch(() => props.renaming, async on => {
  if (!on) return
  draft.value = props.group.name
  problem.value = ''
  await nextTick()
  input.value?.focus(); input.value?.select()
}, { immediate: true })
function keys(event: KeyboardEvent) {
  if (event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) { event.preventDefault(); emit('step', event.key === 'ArrowUp' ? -1 : 1) }
}
let settled = false
function commit() {
  if (settled) return
  const error = props.validate?.(draft.value) ?? ''
  if (error && draft.value.trim() !== props.group.name) { problem.value = error; return }
  settled = true
  emit('rename', draft.value.trim() === props.group.name ? null : draft.value.trim())
}
function cancel() { settled = true; emit('rename', null) }
watch(() => props.renaming, on => { if (on) settled = false })
function inputKeys(event: KeyboardEvent) {
  if (event.key === 'Enter') { event.preventDefault(); commit() }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); cancel() }
}
defineExpose({ focus: () => fold.value?.focus() })
</script>

<template>
  <div class="group-head" :class="[variant, { collapsed, caret, open: menuOpen }]" :data-group-drop="group.id" :draggable="draggable && !renaming ? 'true' : undefined" :data-group-handle="draggable ? group.id : undefined">
    <h2 class="group-title">
      <template v-if="renaming">
        <span class="fold static"><AppIcon name="chevron" :size="12" class="chev" /><GroupMarker :group="group" :size="variant === 'cards' ? 9 : 8" /></span>
        <input
          ref="input" v-model="draft" class="field rename" type="text" maxlength="60" :aria-label="`Rename ${group.name}`" :aria-invalid="!!problem" :aria-describedby="problem ? `${id}-problem` : undefined"
          autocomplete="off" spellcheck="false" @keydown="inputKeys" @input="problem = ''" @blur="commit"
        />
        <span v-if="problem" :id="`${id}-problem`" class="problem" role="alert">{{ problem }}</span>
      </template>
      <button
        v-else ref="fold" type="button" class="fold" :aria-expanded="!collapsed" :aria-controls="controls" aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown" :aria-describedby="`${id}-hint`"
        @click="emit('toggle')" @keydown="keys"
      >
        <AppIcon name="chevron" :size="12" class="chev" />
        <GroupMarker :group="group" :size="variant === 'cards' ? 9 : 8" />
        <span v-clip-tip="group.name" class="name">{{ group.name }}</span>
        <span class="count mono" :aria-label="`${count} ${count === 1 ? 'project' : 'projects'}`">{{ count }}</span>
        <span v-if="group.kind === 'shared'" class="shared" data-tip="Shared with the workspace"><AppIcon name="users" :size="13" /><span class="sr-only">, shared with the workspace</span></span>
      </button>
    </h2>
    <span :id="`${id}-hint`" class="sr-only">Alt and the arrow keys move the group.</span>
    <span class="spacer" />
    <span v-if="draggable" class="grip" aria-hidden="true" data-tip="Drag to reorder · Alt and the arrow keys"><AppIcon name="grip" :size="12" /></span>
    <button type="button" class="icon-btn sm flat more" :aria-label="`Actions for group ${group.name}`" aria-haspopup="menu" :aria-expanded="!!menuOpen" data-tip="Group actions" @click="emit('menu', $event.currentTarget as HTMLElement)">
      <AppIcon name="more" :size="15" />
    </button>
  </div>
</template>

<style scoped>
.group-head { position: relative; display: flex; align-items: center; gap: 4px; min-width: 0; }
.group-title { display: flex; align-items: center; gap: 8px; min-width: 0; margin: 0; font: inherit; }
.fold { display: inline-flex; align-items: center; gap: 8px; min-width: 0; height: 30px; margin-left: -8px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: 650 13px/1 var(--font); letter-spacing: -.003em; }
.fold.static { margin-left: -8px; padding-right: 0; }
@media (hover: hover) { button.fold:hover { background: var(--row-hover); } }
button.fold:focus-visible { box-shadow: var(--focus-ring); }
.chev { color: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) { .chev { transition: transform .15s ease; } }
.collapsed .chev { transform: rotate(-90deg); }
/* The fold button sets line-height 1; a truncating label needs room for descenders (g, j, p, q, y). */
.name { min-width: 0; padding-block: 1px; line-height: 1.3; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.count { display: inline-grid; place-items: center; min-width: 20px; height: 18px; padding: 0 6px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 11px; font-weight: 600; }
.shared { display: inline-flex; color: var(--ink-3); }
.rename { width: min(280px, 60vw); height: 30px; font-size: 13px; font-weight: 600; }
.problem { font-size: 12px; color: var(--danger); white-space: nowrap; }
.spacer { flex: 1; }
.more { opacity: 0; color: var(--ink-3); }
.group-head:hover .more, .group-head:focus-within .more, .group-head.open .more { opacity: 1; }
@media (hover: none) { .more { opacity: 1; } }
/* The whole header can be dragged; the grip beside … says so on hover. */
.grip { display: grid; place-items: center; width: 18px; height: 24px; color: var(--ink-3); cursor: grab; opacity: 0; }
.group-head:hover .grip { opacity: 1; }
.group-head[draggable="true"]:active { cursor: grabbing; }
@media (hover: none), (pointer: coarse) { .grip { display: none; } }
/* Where a dragged group lands: a caret in the gap above this header. */
.group-head.caret::before { content: ''; position: absolute; left: 0; right: 0; top: -7px; height: 2px; border-radius: 2px; background: var(--teal); pointer-events: none; }
.list { height: 44px; padding: 0 12px 0 14px; }
.more { width: 30px; height: 30px; }
.cards { height: 40px; padding: 0 4px; }
.cards .fold { height: 34px; font-size: 14.5px; font-weight: 650; }
.cards .count { font-size: 11.5px; }
.count, .shared, .chev, .more { flex-shrink: 0; }
@media (max-width: 720px) {
  .name { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
}
@media (max-width: 760px) { .list, .cards { height: 48px; } .list { padding: 0 4px 0 10px; } .list .fold, .cards .fold { height: 44px; } .more { width: 44px; height: 44px; } .rename { height: 40px; } }
</style>
