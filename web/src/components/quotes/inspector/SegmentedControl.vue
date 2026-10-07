<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts" generic="T extends string">
import QuoteIcon, { type QuoteIconName } from './QuoteIcon.vue'

// One compact choice among a few: a radio group styled as a segmented control.
// Arrow keys move and choose, like native radios; "mixed" (a selection that
// spans different values) shows no segment as chosen. Pressing keeps the
// document selection: the pointer never takes focus out of the text.
export interface Segment<V extends string> { value: V; label: string; icon?: QuoteIconName; glyph?: string; tip?: string; hideLabel?: boolean }
const props = defineProps<{ options: Segment<T>[]; modelValue: T | 'mixed' | null; label: string; disabled?: boolean; compact?: boolean }>()
const emit = defineEmits<{ choose: [value: T] }>()
const tabbable = (index: number) => {
  const chosen = props.options.findIndex(o => o.value === props.modelValue)
  return (chosen === -1 ? index === 0 : index === chosen) ? 0 : -1
}
function keys(event: KeyboardEvent, index: number) {
  const key = event.key
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(key)) return
  event.preventDefault()
  const n = props.options.length
  const next = key === 'Home' ? 0 : key === 'End' ? n - 1 : (index + (key === 'ArrowRight' || key === 'ArrowDown' ? 1 : -1) + n) % n
  emit('choose', props.options[next]!.value)
  const group = (event.currentTarget as HTMLElement).parentElement
  void Promise.resolve().then(() => group?.querySelectorAll<HTMLButtonElement>('[role="radio"]')[next]?.focus())
}
</script>

<template>
  <div class="segmented" :class="{ compact }" role="radiogroup" :aria-label="label" :aria-disabled="disabled || undefined">
    <button
      v-for="(option, index) in options" :key="option.value" type="button" role="radio" class="segment"
      :class="{ glyph: !!option.glyph, 'icon-only': option.hideLabel }" :aria-checked="modelValue === option.value" :tabindex="tabbable(index)" :disabled="disabled"
      :aria-label="option.hideLabel || option.glyph ? option.label : undefined" :data-tip="option.tip ?? (option.hideLabel || option.glyph ? option.label : undefined)"
      @mousedown.prevent @click="emit('choose', option.value)" @keydown="keys($event, index)"
    >
      <span v-if="option.glyph" class="glyph-text" aria-hidden="true">{{ option.glyph }}</span>
      <QuoteIcon v-else-if="option.icon" :name="option.icon" :size="15" />
      <span v-if="!option.hideLabel && !option.glyph" class="segment-label">{{ option.label }}</span>
    </button>
  </div>
</template>

<style scoped>
.segmented { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); gap: 2px; padding: 3px; border-radius: 10px; background: var(--seg-bg); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--shadow-color) 8%, transparent); }
.segment { display: inline-flex; align-items: center; justify-content: center; gap: 5px; min-width: 0; height: 30px; padding: 0 5px; border: 0; border-radius: 7px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.segment-label { overflow: hidden; text-overflow: ellipsis; }
.segment svg { flex-shrink: 0; }
@media (hover: hover) { .segment:hover:not(:disabled) { color: var(--ink); background: var(--row-hover); } }
.segment[aria-checked="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px color-mix(in srgb, var(--shadow-color) 14%, transparent), inset 0 0 0 1px var(--glass-edge); }
.segment:focus-visible { box-shadow: var(--focus-ring); }
.segment:disabled { color: var(--ink-3); cursor: not-allowed; }
.glyph-text { font: 600 14px/1 var(--mono); font-variant-ligatures: none; }
.compact .segment { height: 28px; padding: 0 4px; }
</style>
