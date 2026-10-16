<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import AppIcon from '../AppIcon.vue'

// An optional date in a field-shaped button. The browser's own date picker
// opens from it (on phones the system wheel), so it works inside the modal
// sheet; a set date can be cleared in place without moving the field.
const props = defineProps<{ modelValue: string; label: string; optional: string; empty: string; clear: string; text: (value: string) => string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const input = ref<HTMLInputElement>(), trigger = ref<HTMLButtonElement>()
function open() {
  const field = input.value
  if (!field || props.disabled) return
  try { field.showPicker() } catch { field.focus(); field.click() }
}
function changed() { emit('update:modelValue', input.value?.value ?? '') }
function cleared() { emit('update:modelValue', ''); trigger.value?.focus({ preventScroll: true }) }
</script>

<template>
  <div class="np-f date">
    <b id="np-date-l">{{ label }} <span class="faint">· {{ optional }}</span></b>
    <span class="date-box">
      <button id="np-date" ref="trigger" type="button" class="pick" :class="{ clearable: !!modelValue }" aria-labelledby="np-date-l np-date" :disabled="disabled" @click="open"><span class="who" :class="{ set: !!modelValue }">{{ modelValue ? text(modelValue) : empty }}</span><AppIcon v-if="!modelValue" name="calendar" :size="14" /></button>
      <button v-if="modelValue" type="button" class="clear" :aria-label="clear" :disabled="disabled" @click="cleared"><AppIcon name="close" :size="12" /></button>
      <input ref="input" class="native" type="date" tabindex="-1" aria-hidden="true" :value="modelValue" :disabled="disabled" @change="changed">
    </span>
  </div>
</template>

<style scoped>
.np-f { display: grid; gap: 6px; min-width: 0; align-content: start; }
.np-f > b { font-size: 13px; font-weight: 600; }
.faint { color: var(--ink-3); font-weight: 400; }
.date-box { position: relative; display: block; min-width: 0; }
.pick { display: flex; align-items: center; gap: 8px; width: 100%; min-width: 0; height: 38px; padding: 0 10px 0 12px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s); background: var(--field-bg); color: var(--ink); box-shadow: var(--field-inset), 0 0 0 1px var(--line); font-size: 13.5px; text-align: left; }
.pick:hover:not(:disabled) { box-shadow: var(--field-inset), 0 0 0 1px var(--glass-rim); }
.pick:disabled { opacity: 1; color: var(--ink-2); background: var(--surface-2); box-shadow: 0 0 0 1px var(--line); }
.pick .who { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); }
.pick .who.set { color: var(--ink); font-variant-numeric: tabular-nums; }
.pick > svg { color: var(--ink-3); }
.pick.clearable { padding-right: 40px; }
/* Clearing sits where the calendar icon was, inside the field: nothing moves. */
.clear { position: absolute; top: 50%; right: 4px; display: grid; place-items: center; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); transform: translateY(-50%); }
.clear:hover:not(:disabled) { background: var(--row-hover); color: var(--ink); }
.clear:focus-visible { box-shadow: var(--focus-ring); }
.native { position: absolute; inset: auto 0 0 0; width: 100%; height: 1px; opacity: 0; pointer-events: none; border: 0; padding: 0; }
@media (pointer: coarse), (max-width: 600px) {
  .pick { height: 44px; }
  .clear { width: 44px; height: 44px; right: 0; }
}
</style>
