<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import type { BizIconName } from './BizIcon.vue'
export interface PickOption { value: string; label: string; hint?: string; badge?: string; icon?: BizIconName; disabled?: boolean; note?: string }
</script>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import AppIcon from './BizIcon.vue'
import FloatingPanel from './BusinessPopover.vue'

// A searchable choice popover: type to filter (or search the server), arrows to
// move, Enter to choose. An optional last row creates what was typed.
const props = withDefaults(defineProps<{
  anchor: HTMLElement | null; title: string; options?: PickOption[]; search?: (term: string) => Promise<PickOption[]>
  current?: string; placeholder?: string; createLabel?: (term: string) => string; width?: number; empty?: string; to?: string
}>(), { options: () => [], current: '', placeholder: 'Type to filter…', width: 300, empty: 'Nothing matches.' })
const emit = defineEmits<{ choose: [option: PickOption]; create: [term: string]; close: [restoreFocus: boolean] }>()
const listId = useId()
const term = ref('')
const active = ref(0)
const remote = ref<PickOption[] | null>(null)
const searching = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let generation = 0

const shown = computed(() => {
  if (props.search && term.value.trim()) return remote.value ?? []
  const needle = term.value.trim().toLowerCase()
  return needle ? props.options.filter(o => `${o.badge ?? ''} ${o.label} ${o.hint ?? ''}`.toLowerCase().includes(needle)) : props.options
})
const canCreate = computed(() => !!props.createLabel && term.value.trim().length > 0 && !shown.value.some(o => o.label.toLowerCase() === term.value.trim().toLowerCase()))
const count = computed(() => shown.value.length + (canCreate.value ? 1 : 0))
watch(term, value => {
  active.value = 0
  if (!props.search) return
  clearTimeout(timer)
  if (!value.trim()) { remote.value = null; searching.value = false; return }
  searching.value = true
  const request = ++generation
  timer = setTimeout(async () => {
    try { const found = await props.search!(value.trim()); if (request === generation) remote.value = found }
    catch { if (request === generation) remote.value = [] }
    finally { if (request === generation) searching.value = false }
  }, 150)
})
onBeforeUnmount(() => { clearTimeout(timer); generation++ })
function pick(index: number) {
  if (index < shown.value.length) { const option = shown.value[index]; if (!option.disabled) emit('choose', option) }
  else if (canCreate.value) emit('create', term.value.trim())
}
function keys(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault(); event.stopPropagation()
    if (!count.value) return
    active.value = (active.value + (event.key === 'ArrowDown' ? 1 : -1) + count.value) % count.value
    document.getElementById(`${listId}-${active.value}`)?.scrollIntoView({ block: 'nearest' })
  } else if (event.key === 'Enter') {
    event.preventDefault(); event.stopPropagation()
    if (count.value) pick(active.value)
  }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="width" :label="title" :to="to" @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">{{ title }}</p>
    <input
      v-model="term" class="field picker-search" role="combobox" aria-autocomplete="list" aria-expanded="true" :aria-controls="listId"
      :aria-activedescendant="count ? `${listId}-${active}` : undefined" :placeholder="placeholder" :aria-label="title" data-autofocus autocomplete="off" spellcheck="false" @keydown="keys"
    />
    <ul :id="listId" class="picker-list" role="listbox" :aria-label="title">
      <li
        v-for="(option, index) in shown" :id="`${listId}-${index}`" :key="option.value" role="option" class="picker-option"
        :class="{ active: index === active, disabled: option.disabled }" :aria-selected="index === active" :aria-disabled="option.disabled || undefined"
        :tabindex="index === active ? 0 : -1" :data-clip-tip="[option.label, option.note, option.hint].filter(Boolean).join('\n')" @keydown.enter.prevent="pick(index)"
        @pointerenter="active = index" @click="pick(index)"
      >
        <AppIcon v-if="option.icon" :name="option.icon" :size="14" class="opt-icon" />
        <span v-if="option.badge" class="opt-badge">{{ option.badge }}</span>
        <span class="opt-text"><span class="opt-label">{{ option.label }}</span><span v-if="option.note" class="opt-note">{{ option.note }}</span></span>
        <span v-if="option.hint" class="opt-hint">{{ option.hint }}</span>
        <AppIcon v-if="option.value === current" name="check" :size="14" class="tick" />
      </li>
      <li v-if="canCreate" :id="`${listId}-${shown.length}`" role="option" class="picker-option create" :class="{ active: active === shown.length }" :aria-selected="active === shown.length" @pointerenter="active = shown.length" @click="pick(shown.length)">
        <AppIcon name="plus" :size="14" class="opt-icon" /><span class="opt-label">{{ createLabel!(term.trim()) }}</span>
      </li>
    </ul>
    <p v-if="searching && !shown.length" class="none" role="status">Searching…</p>
    <p v-else-if="!count" class="none">{{ search && !term.trim() ? 'Type to search.' : empty }}</p>
  </FloatingPanel>
</template>

<style scoped>
.menu-title { padding: 6px 10px 4px; }
.picker-search { height: 32px; margin: 0 0 6px; font-size: 13px; }
.picker-list { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; margin: 0; padding: 0; list-style: none; }
.picker-option { display: flex; align-items: center; gap: 9px; min-height: 34px; padding: 4px 10px; border-radius: 8px; color: var(--ink); font-size: 13.5px; cursor: pointer; }
.picker-option.active { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.picker-option.disabled { cursor: not-allowed; color: var(--ink-3); }
.picker-option.create { color: var(--teal-ink); font-weight: 600; }
.opt-icon { color: var(--ink-3); }
.create .opt-icon { color: var(--teal); }
.opt-badge { flex-shrink: 0; display: inline-flex; align-items: center; height: 20px; padding: 0 7px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 10.5px/1 var(--mono); font-variant-ligatures: none; }
.opt-text { display: grid; flex: 1; min-width: 0; }
.opt-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.opt-note { font-size: 11.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.opt-hint { flex-shrink: 0; font-size: 11.5px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.picker-option.active .opt-hint, .picker-option.active .opt-note { color: var(--ink-2); }
.tick { color: var(--teal); }
.none { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
</style>
