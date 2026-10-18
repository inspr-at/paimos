<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
export interface FacetChoice { value: string; label: string; count?: number; state?: string }
</script>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { vClipTip } from '../../directives/clipTip'
import type { QuoteStatus } from '../../lib/quotes/list'
import AppIcon from './BizIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import QuoteStatusIcon from '../quotes/QuoteStatusIcon.vue'

// A toolbar filter: the button shows its count once anything is chosen; the
// popover lists checkboxes (with a find field for longer lists).
const props = defineProps<{ label: string; options: FacetChoice[]; selected: string[] }>()
const emit = defineEmits<{ toggle: [value: string]; clear: [] }>()
const anchor = ref<HTMLElement | null>(null)
const button = ref<HTMLButtonElement>()
const term = ref('')
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return needle ? props.options.filter(o => o.label.toLowerCase().includes(needle)) : props.options
})
function toggleOpen(event: MouseEvent) { anchor.value = anchor.value ? null : event.currentTarget as HTMLElement; term.value = '' }
function close(restore: boolean) { anchor.value = null; if (restore) button.value?.focus() }
function keys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'j', 'k'].includes(event.key) || (event.target as HTMLElement).tagName === 'INPUT' && (event.key === 'j' || event.key === 'k')) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLInputElement>('input[type="checkbox"]')]
  const index = items.indexOf(document.activeElement as HTMLInputElement)
  event.preventDefault(); event.stopPropagation()
  items[event.key === 'ArrowDown' || event.key === 'j' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)]?.focus()
}
</script>

<template>
  <button ref="button" type="button" class="btn sm facet-btn" :class="{ on: selected.length }" aria-haspopup="dialog" :aria-expanded="!!anchor" @click="toggleOpen">
    {{ label }}<span class="facet-end"><span v-if="selected.length" class="facet-count">{{ selected.length }}</span><AppIcon v-else name="chevron" :size="12" class="facet-chevron" /></span>
  </button>
  <FloatingPanel v-if="anchor" :anchor="anchor" :width="260" :label="`Filter by ${label.toLowerCase()}`" @close="close">
    <div @keydown="keys">
      <div class="facet-head">
        <p class="eyebrow">{{ label }}</p>
        <button type="button" class="clear" :style="{ visibility: selected.length ? undefined : 'hidden' }" :disabled="!selected.length" @click="emit('clear')">Clear</button>
      </div>
      <input v-if="options.length > 8" v-model="term" class="field facet-search" :placeholder="`Find ${label.toLowerCase()}…`" :aria-label="`Find ${label.toLowerCase()}`" data-autofocus />
      <div class="options" role="group" :aria-label="label">
        <label v-for="option in shown" :key="option.value" class="option" :class="{ muted: !option.count && !selected.includes(option.value) }">
          <input class="check-box" type="checkbox" :checked="selected.includes(option.value)" @change="emit('toggle', option.value)" />
          <QuoteStatusIcon v-if="option.state" :status="option.state as QuoteStatus" :label="false" />
          <span v-clip-tip class="option-label">{{ option.label }}</span>
          <span class="count mono">{{ option.count ?? '' }}</span>
        </label>
      </div>
      <p v-if="!shown.length" class="none">Nothing matches.</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.facet-btn { gap: 6px; padding: 0 9px 0 12px; font-weight: 600; color: var(--ink-2); }
.facet-btn:hover, .facet-btn[aria-expanded="true"] { color: var(--ink); }
.facet-btn.on { color: var(--teal-ink); }
.facet-end { display: inline-grid; place-items: center; width: 18px; }
.facet-chevron { color: var(--ink-3); }
.facet-count { display: inline-grid; place-items: center; min-width: 17px; height: 17px; padding: 0 5px; border-radius: 999px; background: linear-gradient(180deg, var(--primary-hi), var(--primary)); color: var(--primary-on); font-size: 10.5px; font-weight: 700; }
.facet-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 26px; padding: 2px 6px 4px 10px; }
.clear { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.clear:hover { background: var(--row-selected); }
.facet-search { height: 30px; margin: 0 0 6px; font-size: 13px; }
.options { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; }
.option { display: flex; align-items: center; gap: 10px; min-height: 32px; padding: 0 10px; border-radius: 8px; font-size: 13.5px; cursor: pointer; }
@media (hover: hover) { .option:hover { background: var(--row-hover); } }
.option:focus-within { background: var(--row-selected); }
.option.muted .option-label { color: var(--ink-3); }
.option-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.count { font-size: 11.5px; color: var(--ink-3); }
.none { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
@media (pointer: coarse), (max-width: 720px) { .option { min-height: 44px; } }
</style>
