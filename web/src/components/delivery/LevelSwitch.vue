<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Simple · Expert (AEON-994). Simple is the default; the choice is the person's own.
import type { Level } from '../../lib/deliveryNumbers'

defineProps<{ modelValue: Level; label: string; names: Record<Level, string> }>()
const emit = defineEmits<{ 'update:modelValue': [level: Level] }>()
const LEVELS: Level[] = ['simple', 'expert']
function move(event: KeyboardEvent, current: Level) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const level = event.key === 'Home' ? 'simple' : event.key === 'End' ? 'expert' : current === 'simple' ? 'expert' : 'simple'
  emit('update:modelValue', level)
  const group = event.currentTarget as HTMLElement
  requestAnimationFrame(() => group.querySelector<HTMLButtonElement>(`[data-level="${level}"]`)?.focus())
}
</script>

<template>
  <span class="seg level" role="radiogroup" :aria-label="label" @keydown="move($event, modelValue)">
    <button v-for="level in LEVELS" :key="level" type="button" role="radio" :data-level="level" :aria-checked="modelValue === level"
      :tabindex="modelValue === level ? 0 : -1" @click="emit('update:modelValue', level)">{{ names[level] }}</button>
  </span>
</template>

<style scoped>
.level button { min-width: 74px; }
@container delivery (max-width: 640px) {
  .level { flex: 1; }
  .level button { flex: 1; height: 44px; }
}
</style>
