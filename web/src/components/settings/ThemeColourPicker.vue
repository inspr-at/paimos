<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { COLOUR_PRESETS, validColour } from '../../lib/themeColours'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ title: string; value: string }>()
const emit = defineEmits<{ change: [value: string]; close: [] }>()
const hex = ref(props.value), dialog = ref<HTMLElement>()
const invalid = computed(() => !validColour(hex.value))
function change(value: string) { hex.value = value; if (validColour(value)) emit('change', value.toLowerCase()) }
function keys(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    if (event.target instanceof HTMLInputElement) event.target.blur()
    else emit('close')
  }
  if (event.key === 'Tab') {
    const controls = Array.from(dialog.value?.querySelectorAll<HTMLElement>('button, input') ?? [])
    const first = controls[0], last = controls[controls.length - 1]
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.value)) { event.preventDefault(); last?.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
  }
}
onMounted(() => dialog.value?.focus())
</script>
<template>
  <Teleport to="body">
    <div class="picker-scrim" @click.self="emit('close')">
      <section ref="dialog" class="theme-picker" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1" @keydown="keys">
        <header><h3>{{ title }}</h3><button type="button" class="btn icon" aria-label="Close colour picker" @click="emit('close')"><AppIcon name="close" :size="16" /></button></header>
        <div class="picker-body">
          <p>Choose a preset or enter your own colour.</p>
          <div class="presets" aria-label="Colour presets"><button v-for="colour in COLOUR_PRESETS" :key="colour" type="button" :aria-label="`Use ${colour}`" :aria-pressed="value.toLowerCase() === colour" :style="{ background: colour }" @click="change(colour)" /></div>
          <div class="custom"><label>Native colour picker<input type="color" :value="value" @input="change(($event.target as HTMLInputElement).value)" /></label><label>Hex colour<input :value="hex" maxlength="7" spellcheck="false" :aria-invalid="invalid" aria-describedby="hex-help" @input="change(($event.target as HTMLInputElement).value)" /></label></div>
          <p id="hex-help" class="hex-help" :class="{ error: invalid }">{{ invalid ? 'Use # followed by six hexadecimal digits.' : 'Changes appear in the preview. Save to keep them.' }}</p>
        </div>
        <footer><button type="button" class="btn" @click="emit('close')">Done</button></footer>
      </section>
    </div>
  </Teleport>
</template>
<style scoped>
.picker-scrim { position: fixed; inset: 0; z-index: 110; background: var(--scrim); display: flex; justify-content: center; align-items: flex-start; padding: max(80px, 10vh) 16px 16px; }
.theme-picker { width: min(100%, 30rem); background: var(--surface-raised); color: var(--ink); border-radius: 16px; box-shadow: var(--shadow-pop); outline: none; }
header { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--line); }
h3 { font-size: 15px; }
.picker-body { padding: 20px; }
.picker-body p { font-size: 13px; color: var(--ink-2); }
.presets { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 10px; margin: 16px 0; }
.presets button { height: 44px; border-radius: 8px; border: 1px solid var(--line-2); }
.presets button[aria-pressed="true"] { outline: 2px solid var(--ink); outline-offset: 2px; }
.custom { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
label { display: grid; gap: 6px; font-size: 12px; color: var(--ink-2); }
input { width: 100%; min-width: 0; height: 44px; }
.hex-help { min-height: 3em; margin-top: 12px; }
.hex-help.error { color: var(--danger); }
footer { display: flex; justify-content: flex-end; padding: 12px 20px; border-top: 1px solid var(--line); }
@media (max-width: 600px) { .picker-scrim { padding: 0; } .theme-picker { width: 100%; height: 100dvh; border-radius: 0; display: flex; flex-direction: column; } .picker-body { flex: 1; overflow: auto; } footer { padding-bottom: max(12px, env(safe-area-inset-bottom)); } }
</style>
