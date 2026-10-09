<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
export interface MenuOption { value: string; label: string; hint?: string }
</script>
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { computed, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import PersonAvatar from './PersonAvatar.vue'
import PriorityIcon from './PriorityIcon.vue'
import ReadName from '../ReadName.vue'

// A small choice menu (priority, assignee, type) with arrows, digits and, for
// longer lists, a filter field. The empty value means "none".
const props = defineProps<{ anchor: HTMLElement | null; title: string; subject: string; kind: 'priority' | 'assignee' | 'type'; options: MenuOption[]; current: string; searchable?: boolean }>()
const emit = defineEmits<{ choose: [value: string]; close: [restoreFocus: boolean] }>()
const list = ref<HTMLElement>()
const term = ref('')
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return needle ? props.options.filter(option => option.label.toLowerCase().includes(needle)) : props.options
})
function move(event: KeyboardEvent) {
  const items = [...(list.value?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]') ?? [])]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  let next = -1
  if (event.key === 'ArrowDown') next = index === -1 ? 0 : Math.min(items.length - 1, index + 1)
  else if (event.key === 'ArrowUp') next = Math.max(0, index - 1)
  else if (!props.searchable && (event.key === 'j' || event.key === 'k')) next = event.key === 'j' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)
  else if (!props.searchable && /^[1-9]$/.test(event.key) && Number(event.key) <= items.length) { event.preventDefault(); items[Number(event.key) - 1].click(); return }
  else if (event.key === 'Enter' && document.activeElement?.tagName === 'INPUT' && items[0]) { event.preventDefault(); items[0].click(); return }
  if (next >= 0) { event.preventDefault(); event.stopPropagation(); items[next]?.focus() }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="232" :label="`${title} of ${subject}`" @close="restore => emit('close', restore)">
    <div @keydown="move">
      <p class="menu-title eyebrow">{{ title }}</p>
      <input v-if="searchable" v-model="term" class="field menu-search" :placeholder="`Find ${title.toLowerCase()}…`" :aria-label="`Find ${title.toLowerCase()}`" data-autofocus />
      <div class="menu" :style="{ gridTemplateRows: `repeat(${Math.max(1, shown.length)}, auto)` }">
        <div ref="list" class="option-list" role="menu" :aria-label="`${title} of ${subject}`">
        <div v-for="(option, index) in shown" :key="option.value" class="option-row" role="presentation">
          <button
            type="button" role="menuitemradio" class="menu-item" :aria-checked="option.value === current"
            :data-autofocus="!searchable && option.value === current ? '' : undefined" @click="emit('choose', option.value)"
          >
            <template v-if="kind === 'priority'"><PriorityIcon v-if="option.value" :priority="option.value" /><span v-else class="none-mark" /></template>
            <template v-else-if="kind === 'assignee'"><PersonAvatar v-if="option.value" :id="option.value" :name="option.label" :size="18" /><AppIcon v-else name="user" :size="14" class="faint-icon" /></template>
            <AppIcon v-else :name="option.value === 'epic' ? 'epic' : option.value === 'task' ? 'task' : 'ticket'" :size="14" :class="['kind-icon', option.value]" />
            <span v-clip-tip class="label">{{ option.label }}</span>
            <span v-if="option.hint" class="hint">{{ option.hint }}</span>
            <AppIcon v-if="option.value === current" name="check" :size="14" class="tick" />
            <span v-else-if="!searchable && index < 9" class="digit keycap" aria-hidden="true">{{ index + 1 }}</span>
          </button>
        </div>
        </div>
        <ReadName v-for="(option, index) in shown" :key="option.value" :text="option.label" :style="{ gridColumn: 2, gridRow: index + 1 }" />
        <p v-if="!shown.length" class="none">Nobody matches.</p>
      </div>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.menu-title { padding: 6px 10px 4px; }
.menu-search { height: 30px; margin: 0 0 6px; font-size: 13px; }
.menu { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 1px 0; }
.option-list { display: grid; grid-column: 1; grid-row: 1 / -1; grid-template-rows: subgrid; min-width: 0; }
.option-row { display: grid; grid-template-columns: minmax(0, 1fr); }
.menu-item { display: flex; align-items: center; min-width: 0; gap: 10px; height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
@media (hover: hover) { .menu-item:hover { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item:active { background: var(--row-selected); }
.label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hint { font-size: 11.5px; color: var(--ink-3); }
.tick { color: var(--teal); }
.digit { opacity: 0; }
.menu-item:hover .digit, .menu-item:focus-visible .digit { opacity: 1; }
.none-mark { width: 14px; height: 2px; border-radius: 2px; background: var(--line-2); }
.faint-icon, .kind-icon { color: var(--ink-3); }
.kind-icon.epic { color: var(--kind-parent); }
.none { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
@media (pointer: coarse) { .menu-item { height: 44px; } }

@media (max-width: 720px) {
  .menu-item { height: 52px; }
  .label { line-height: 18px; }
  .label { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; overflow-wrap: anywhere; }
}
</style>
