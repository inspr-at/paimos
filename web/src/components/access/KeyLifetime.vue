<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { keyExpiryAfter } from '../../lib/access'
import { absoluteTime } from '../../lib/work'

const props = defineProps<{ modelValue: number; disabled?: boolean; current?: string | null }>()
const emit = defineEmits<{ 'update:modelValue': [days: number] }>()
const lifetimes = computed(() => [
  ...(props.current === undefined ? [] : [{ days: -1, label: 'Keep current' }]),
  { days: 30, label: '30 days' }, { days: 90, label: '90 days' }, { days: 365, label: '365 days' }, { days: 0, label: 'Never' },
])
const expires = computed(() => props.modelValue === -1 ? props.current : keyExpiryAfter(props.modelValue))
function choose(event: KeyboardEvent) {
  const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0
  if (!step && event.key !== 'Home' && event.key !== 'End') return
  event.preventDefault()
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? lifetimes.value.length - 1 : (lifetimes.value.findIndex(l => l.days === props.modelValue) + step + lifetimes.value.length) % lifetimes.value.length
  emit('update:modelValue', lifetimes.value[index]!.days)
  ;(event.currentTarget as HTMLElement).parentElement?.querySelectorAll<HTMLButtonElement>('[role=radio]')[index]?.focus()
}
</script>

<template>
  <fieldset class="lifetimes" :disabled="disabled">
    <legend>Expires after</legend>
    <div class="seg" :class="{ editing: current !== undefined }" role="radiogroup" aria-label="Key expires after">
      <button v-for="l in lifetimes" :key="l.days" type="button" role="radio" :aria-checked="modelValue === l.days" :tabindex="modelValue === l.days ? 0 : -1" :disabled="disabled" @keydown="choose" @click="emit('update:modelValue', l.days)">{{ l.label }}</button>
    </div>
    <p class="expiry-note">{{ modelValue === -1 ? 'Current expiry: ' : '' }}{{ expires ? `Expires ${absoluteTime(expires)}.` : 'Works until revoked or rotated.' }} {{ current === undefined ? 'Keys do not rotate automatically.' : 'Changing expiry keeps the same key.' }}</p>
  </fieldset>
</template>

<style scoped>
.lifetimes { display: grid; gap: 8px; margin: 0; padding: 0; border: 0; min-width: 0; }
legend { margin-bottom: 8px; padding: 0; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.seg { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); }
.seg.editing { grid-template-columns: repeat(5, minmax(0, 1fr)); }
.seg button { height: 44px; padding-inline: 4px; font-size: 12px; line-height: 1.3; }
.expiry-note { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
@media (max-width: 600px) { .seg button { min-height: 44px; } .seg.editing { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
</style>
