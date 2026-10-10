<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { isWorkIcon, searchWorkIcons, workIconLabel } from '../../lib/workIcons'
import AppIcon, { type IconName } from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

// The icon of a work level (AEON-1142): the trigger shows the chosen icon; the
// popover is a searchable grid with Default first. Its size never changes: the
// grid scrolls, and the name of the icon under the pointer or focus sits in a
// reserved line below it. Typing finds icons by name or search word; ArrowDown
// enters the grid, arrows move in it, Enter or Space chooses. Esc leaves the
// search field first, then closes and returns to the trigger. Blank means the
// level's default icon (`fallback`).
const props = defineProps<{ modelValue: string; label: string; fallback: IconName }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
interface Option { value: string; icon: IconName; label: string }
const open = ref(false)
const query = ref('')
const trigger = ref<HTMLButtonElement>()
const field = ref<HTMLInputElement>()
const grid = ref<HTMLElement>()
const chosenCell = ref<HTMLElement>()
const keepChosen = (el: unknown) => { chosenCell.value = (el as HTMLElement | null) ?? undefined }
const current = computed(() => isWorkIcon(props.modelValue) ? props.modelValue : '')
const defaultOption = computed<Option>(() => ({ value: '', icon: props.fallback, label: `Default (${workIconLabel(props.fallback)})` }))
const options = computed<Option[]>(() => {
  const found = searchWorkIcons(query.value).map(icon => ({ value: icon.name as string, icon: icon.name, label: icon.label }))
  // Default stays first while browsing, and when the search names it or its icon.
  const keepDefault = !query.value.trim() || 'default'.startsWith(query.value.trim().toLowerCase()) || found.some(option => option.value === props.fallback)
  return keepDefault ? [defaultOption.value, ...found] : found
})
// The roving tab stop: the chosen option when it is listed, else the first.
const stop = computed(() => options.value.some(option => option.value === current.value) ? current.value : options.value[0]?.value)
const pointed = ref<Option>()
const focused = ref<Option>()
const caption = computed(() => (pointed.value ?? focused.value ?? options.value.find(option => option.value === current.value) ?? defaultOption.value).label)
const shown = computed(() => current.value ? current.value as IconName : props.fallback)
const shownLabel = computed(() => current.value ? workIconLabel(current.value) : 'Default')
// Search fields bring up the keyboard on touch screens: there, focus the chosen icon.
const typing = typeof matchMedia === 'function' && matchMedia('(pointer: fine)').matches

watch(open, async isOpen => {
  query.value = ''; pointed.value = undefined; focused.value = undefined
  if (!isOpen) return
  await nextTick()
  // Show the chosen icon without moving the page: scroll only the grid.
  const box = grid.value, cell = chosenCell.value
  if (box && cell) box.scrollTop = cell.offsetTop - (box.clientHeight - cell.offsetHeight) / 2
})
function close(restoreFocus: boolean) {
  open.value = false
  if (restoreFocus) void nextTick(() => trigger.value?.focus())
}
function choose(value: string) {
  if (value !== props.modelValue) emit('update:modelValue', value)
  close(true)
}
const cells = () => [...(grid.value?.querySelectorAll<HTMLButtonElement>('[role=option]') ?? [])]
function enterGrid(event: KeyboardEvent) {
  if (event.key !== 'ArrowDown') return
  const items = cells()
  if (!items.length) return
  event.preventDefault()
  ;(items.find(item => item.getAttribute('aria-selected') === 'true') ?? items[0]).focus()
}
// Grid columns follow the panel width (fewer on narrow phones): count the first row.
function columns(items: HTMLElement[]) {
  const top = items[0]?.offsetTop
  const count = items.findIndex(item => item.offsetTop !== top)
  return count < 0 ? items.length : count
}
function move(event: KeyboardEvent) {
  const items = cells(), at = items.indexOf(document.activeElement as HTMLButtonElement)
  if (at < 0) return
  if (event.key.length === 1 && event.key !== ' ' && !event.metaKey && !event.ctrlKey && !event.altKey) {
    // Typing in the grid continues the search; the character lands in the field.
    field.value?.focus()
    return
  }
  const cols = columns(items), last = items.length - 1
  const targets: Record<string, number> = { ArrowRight: Math.min(last, at + 1), ArrowLeft: Math.max(0, at - 1), ArrowDown: Math.min(last, at + cols), ArrowUp: at - cols, Home: 0, End: last }
  const next = targets[event.key]
  if (next === undefined) return
  event.preventDefault(); event.stopPropagation()
  if (next < 0) field.value?.focus()
  else items[next].focus()
}
</script>

<template>
  <div class="icon-choice">
    <button
      ref="trigger" type="button" class="field trigger" :aria-label="`${label}: ${shownLabel}`" aria-haspopup="listbox" :aria-expanded="open"
      @click="open = !open" @keydown.down.prevent="open = true" @keydown.up.prevent="open = true"
    >
      <AppIcon :name="shown" :size="16" class="glyph" :class="{ fallback: !current }" />
      <span class="name">{{ shownLabel }}</span>
      <AppIcon name="chevron" :size="14" class="caret" />
    </button>
    <FloatingPanel v-if="open" :anchor="trigger ?? null" :width="384" cycle field-escape :label="`Choose ${label.toLowerCase()}`" @close="close">
      <div class="picker">
        <input
          ref="field" v-model="query" class="field find" type="search" maxlength="60" placeholder="Find an icon" aria-label="Find an icon"
          autocomplete="off" spellcheck="false" :data-autofocus="typing ? '' : undefined" @keydown="enterGrid"
        />
        <div class="well">
          <div v-if="options.length" ref="grid" class="grid" role="listbox" :aria-label="label" @keydown="move" @pointerleave="pointed = undefined">
            <button
              v-for="option in options" :key="option.value" type="button" role="option" class="cell" :class="{ fallback: !option.value }"
              :ref="option.value === current ? keepChosen : undefined" :aria-label="option.label" :aria-selected="option.value === current" :tabindex="option.value === stop ? 0 : -1"
              :data-autofocus="!typing && option.value === stop ? '' : undefined"
              @click="choose(option.value)" @focus="focused = option" @blur="focused = undefined" @pointerenter="pointed = option"
            >
              <AppIcon :name="option.icon" :size="18" />
            </button>
          </div>
          <p v-else class="none" role="status">No icon matches “{{ query.trim() }}”. Try a word like goal, bug or team.</p>
        </div>
        <p class="caption" aria-hidden="true">{{ caption }}</p>
      </div>
    </FloatingPanel>
  </div>
</template>

<style scoped>
.icon-choice { min-width: 0; }
.trigger { display: flex; align-items: center; gap: 8px; width: 100%; height: 36px; padding: 0 10px; font: inherit; font-size: 13.5px; text-align: left; white-space: nowrap; cursor: pointer; }
.trigger:disabled { cursor: default; opacity: .6; }
.name { flex: 1; min-width: 0; }
.glyph { flex-shrink: 0; color: var(--ink); }
.glyph.fallback { color: var(--ink-3); }
.caret { flex-shrink: 0; color: var(--ink-3); }
.picker { display: flex; flex-direction: column; gap: 6px; }
.find { height: 36px; }
/* Five whole rows of 44 px targets between hairlines; more rows scroll inside, the panel keeps its size. */
.well { box-sizing: content-box; height: 220px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
.grid { position: relative; display: grid; grid-template-columns: repeat(auto-fill, 44px); grid-auto-rows: 44px; align-content: start; justify-content: center; height: 100%; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.cell { display: grid; place-items: center; width: 44px; height: 44px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink); cursor: pointer; }
.cell.fallback { color: var(--ink-3); }
@media (hover: hover) { .cell:hover { background: var(--row-hover); } }
.cell[aria-selected=true] { background: var(--row-selected); color: var(--teal-ink); }
.cell:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); outline: none; }
.none { margin: 0; padding: 14px 8px; font-size: 13px; color: var(--ink-3); text-align: center; }
.caption { height: 20px; margin: 0; padding: 0 4px; overflow: hidden; font-size: 12.5px; font-weight: 600; line-height: 20px; color: var(--ink-2); white-space: nowrap; }
@media (pointer: coarse), (max-width: 720px) { .trigger { height: 44px; } .find { height: 44px; } }
</style>
