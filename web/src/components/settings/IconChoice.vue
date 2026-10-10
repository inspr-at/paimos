<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { listKeyTarget, WORK_ICONS, workIconChoice, workIconName } from '../../lib/workVocabulary'
import AppIcon, { type IconName } from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

// The icon of a work level: the trigger shows the chosen icon, the popover lists
// every icon beside its name. Arrows move, Enter or Space chooses, Esc closes and
// returns to the trigger. Blank means the level's default icon (`fallback`).
const props = defineProps<{ modelValue: string; label: string; fallback: IconName }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const open = ref(false)
const trigger = ref<HTMLButtonElement>()
const current = computed(() => (WORK_ICONS as readonly string[]).includes(props.modelValue) ? props.modelValue : '')
const options = computed(() => [
  { value: '', icon: props.fallback, label: 'Default', hint: workIconName(props.fallback) },
  ...WORK_ICONS.map(icon => ({ value: icon as string, icon: icon as IconName, label: workIconName(icon), hint: '' })),
])
const shown = computed(() => workIconChoice(current.value, props.fallback))
const shownLabel = computed(() => current.value ? workIconName(current.value) : 'Default')
function close(restoreFocus: boolean) {
  open.value = false
  if (restoreFocus) void nextTick(() => trigger.value?.focus())
}
function choose(value: string) {
  if (value !== props.modelValue) emit('update:modelValue', value)
  close(true)
}
function move(event: KeyboardEvent) {
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role=option]')]
  const next = listKeyTarget(event.key, items.indexOf(document.activeElement as HTMLButtonElement), items.length)
  if (next === undefined) return
  event.preventDefault(); event.stopPropagation()
  items[next].focus()
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
    <FloatingPanel v-if="open" :anchor="trigger ?? null" :width="224" :label="`Choose ${label.toLowerCase()}`" @close="close">
      <div class="options" role="listbox" :aria-label="label" @keydown="move">
        <button
          v-for="option in options" :key="option.value" type="button" role="option" class="option" :aria-selected="option.value === current"
          :data-autofocus="option.value === current ? '' : undefined" @click="choose(option.value)"
        >
          <AppIcon :name="option.icon" :size="16" class="glyph" :class="{ fallback: !option.value }" />
          <span class="text">{{ option.label }}</span>
          <span v-if="option.hint" class="hint">{{ option.hint }}</span>
          <AppIcon :style="{ visibility: option.value === current ? 'visible' : 'hidden' }" name="check" :size="14" class="tick" />
        </button>
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
.option { display: flex; align-items: center; gap: 10px; width: 100%; height: 40px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; font-weight: 600; text-align: left; }
@media (hover: hover) { .option:hover { background: var(--row-hover); } }
.option:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); outline: none; }
.text { flex: 1; min-width: 0; }
.hint { flex-shrink: 0; font-size: 11.5px; font-weight: 400; color: var(--ink-3); }
.tick { flex-shrink: 0; color: var(--teal); }
@media (pointer: coarse), (max-width: 720px) { .trigger { height: 44px; } .option { height: 44px; } }
</style>
