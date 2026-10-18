<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { heldOptions, type FacetOption } from '../../lib/ticketList'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import HarnessMark from './HarnessMark.vue'
import { FACET_TITLE, NO_PROJECT, type FacetChoice, type FacetKey } from './sessionGroups'

// One session filter (AEON-1210, design AEON-1196): the ticket list's facet menu for Project,
// Harness or Computer. Each value is a checkbox with the sessions that carry it. Rows keep
// their places while the menu is open: a count changes in place, a new value joins at the end.
const props = defineProps<{ anchor: HTMLElement | null; facet: FacetKey; choices: FacetChoice[]; selected: string[] }>()
const emit = defineEmits<{ toggle: [value: string]; clear: []; close: [restoreFocus: boolean] }>()
const title = computed(() => FACET_TITLE[props.facet])
const held = ref<FacetOption[]>([])
const searchable = ref(false)
watch(() => props.choices, live => {
  if (!held.value.length) searchable.value = live.length > 8
  held.value = heldOptions(held.value, live)
}, { immediate: true })
const term = ref('')
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return needle ? held.value.filter(option => option.label.toLowerCase().includes(needle)) : held.value
})
// Keep focus on the box just toggled, also when its label was clicked.
function changed(event: Event, value: string) {
  (event.target as HTMLInputElement).focus({ preventScroll: true })
  emit('toggle', value)
}
function move(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'j', 'k'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLInputElement>('input')]
  const index = items.indexOf(document.activeElement as HTMLInputElement)
  event.preventDefault(); event.stopPropagation()
  items[event.key === 'ArrowDown' || event.key === 'j' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)]?.focus()
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="264" :label="`Filter by ${title}`" @close="restore => emit('close', restore)">
    <div class="facet-head">
      <p class="eyebrow">{{ title }}</p>
      <button type="button" class="clear" :style="{ visibility: selected.length ? 'visible' : 'hidden' }" :disabled="!selected.length" @click="emit('clear')">Clear</button>
    </div>
    <input v-if="searchable" v-model="term" class="field facet-search" :placeholder="`Find ${title.toLowerCase()}…`" :aria-label="`Find ${title.toLowerCase()}`" data-autofocus />
    <div class="options" role="group" :aria-label="title" @keydown="move">
      <label v-for="(option, index) in shown" :key="option.value" class="option" :class="{ muted: !option.count && !selected.includes(option.value) }">
        <input class="check-box" type="checkbox" :checked="selected.includes(option.value)" :data-autofocus="!searchable && index === 0 ? '' : undefined" @change="changed($event, option.value)" />
        <HarnessMark v-if="facet === 'harness'" :harness="option.value" :size="14" class="mark" />
        <AppIcon v-else :name="facet === 'computer' ? 'monitor' : option.value === NO_PROJECT ? 'user' : 'folder'" :size="14" class="mark" />
        <span class="label" :title="option.label">{{ option.label }}</span>
        <span class="count mono">{{ option.count }}</span>
      </label>
    </div>
    <p v-if="!shown.length" class="none">{{ held.length ? 'Nothing matches.' : 'No sessions to filter yet.' }}</p>
    <p class="keys" aria-hidden="true"><KeyCap k="Space" /> include or leave out</p>
  </FloatingPanel>
</template>

<style scoped>
.facet-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 26px; padding: 2px 6px 4px 10px; }
.clear { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.clear:hover { background: var(--row-selected); }
.clear:focus-visible { box-shadow: var(--focus-ring); }
.facet-search { height: 30px; margin: 0 0 6px; font-size: 13px; }
.options { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; }
.option { display: flex; align-items: center; gap: 10px; min-height: 32px; padding: 0 8px 0 10px; border-radius: 8px; font-size: 13.5px; cursor: pointer; }
@media (hover: hover) { .option:hover { background: var(--row-hover); } }
.option:focus-within { background: var(--row-selected); }
.option.muted .label { color: var(--ink-3); }
.mark { flex: none; color: var(--ink-3); }
.label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.count { flex: none; min-width: 18px; text-align: right; font-size: 11.5px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.none { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.keys { display: flex; align-items: center; gap: 5px; margin: 6px 4px 0; padding: 7px 6px 1px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
@media (hover: none) { .keys { display: none; } }
@media (pointer: coarse), (max-width: 720px) { .option { min-height: 44px; } .clear { min-height: 44px; } .facet-search { height: 44px; font-size: 16px; } }
</style>
