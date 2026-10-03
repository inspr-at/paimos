<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
export interface Choice { value: string; label: string; hint?: string; detail?: string }
</script>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

// A searchable single choice in a popover (time zone, language): type to narrow,
// arrows to move, Enter to choose, Esc to close.
const props = withDefaults(defineProps<{ anchor: HTMLElement | null; label: string; choices: Choice[]; current: string; match?: (choice: Choice, needle: string) => boolean; limit?: number; placeholder?: string }>(), { match: undefined, limit: 80, placeholder: 'Search…' })
const emit = defineEmits<{ choose: [value: string]; close: [restoreFocus: boolean] }>()
const term = ref('')
const list = ref<HTMLElement>()
const hits = computed(() => {
  const needle = term.value.trim().toLowerCase()
  if (!needle) return props.choices
  return props.choices.filter(choice => props.match ? props.match(choice, needle) : `${choice.label} ${choice.hint ?? ''} ${choice.detail ?? ''}`.toLowerCase().includes(needle))
})
const shown = computed(() => hits.value.slice(0, props.limit))
const more = computed(() => hits.value.length - shown.value.length)
function move(event: KeyboardEvent) {
  const items = [...(list.value?.querySelectorAll<HTMLButtonElement>('button') ?? [])]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault(); event.stopPropagation()
    const next = event.key === 'ArrowDown' ? Math.min(items.length - 1, index + 1) : index <= 0 ? -1 : index - 1
    if (next === -1) list.value?.parentElement?.querySelector<HTMLInputElement>('input')?.focus()
    else items[next]?.focus()
  } else if (event.key === 'Enter' && document.activeElement?.tagName === 'INPUT' && items[0]) { event.preventDefault(); items[0].click() }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="320" :tallest="380" :label="label" @close="restore => emit('close', restore)">
    <div @keydown="move">
      <label class="search-field find">
        <AppIcon name="search" :size="13" />
        <input v-model="term" class="field" type="search" :placeholder="placeholder" :aria-label="`Search ${label.toLowerCase()}`" data-autofocus autocomplete="off" spellcheck="false" />
      </label>
      <div ref="list" class="menu" role="listbox" :aria-label="label">
        <button
          v-for="choice in shown" :key="choice.value" type="button" role="option" class="choice" :aria-selected="choice.value === current"
          @click="emit('choose', choice.value)"
        >
          <span class="text"><span v-clip-tip class="label">{{ choice.label }}</span><span v-if="choice.detail" v-clip-tip class="detail">{{ choice.detail }}</span></span>
          <span v-if="choice.hint" class="hint">{{ choice.hint }}</span>
          <AppIcon :style="{ visibility: choice.value === current ? 'visible' : 'hidden' }" name="check" :size="14" class="tick" />
        </button>
      </div>
      <p v-if="!shown.length" class="none">Nothing matches “{{ term.trim() }}”.</p>
      <p v-else-if="more" class="none">{{ more }} more · keep typing to narrow</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.find { margin: 2px 2px 6px; }
.find .field { height: 34px; font-size: 13px; }
.menu { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; }
.choice { display: flex; align-items: center; gap: 10px; min-height: 40px; padding: 4px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; font-weight: 600; text-align: left; }
@media (hover: hover) { .choice:hover { background: var(--row-hover); } }
.choice:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.choice[aria-selected="true"] { font-weight: 600; }
.text { display: grid; flex: 1; min-width: 0; }
.label { overflow-wrap: anywhere; }
.detail { font-size: 11.5px; font-weight: 400; color: var(--ink-3); overflow-wrap: anywhere; }
.hint { flex-shrink: 0; font: 500 11px/1 var(--mono); color: var(--ink-2); font-variant-numeric: tabular-nums; }
.tick { flex-shrink: 0; color: var(--teal); }
.none { padding: 8px 10px; font-size: 12.5px; color: var(--ink-3); }
</style>
