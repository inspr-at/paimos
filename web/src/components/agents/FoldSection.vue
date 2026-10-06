<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One fold pattern for the Agents page (AEON-781): chevron first, then the
// title; both toggle. The head always states the state; folding removes only
// the details. The body's row grows 0fr → 1fr below the head, so nothing
// above it moves, and the folded body is inert and hidden after the motion.
import { ref, useId, useSlots } from 'vue'
import AppIcon from '../AppIcon.vue'

defineProps<{ open: boolean; label: string; tip?: string }>()
const emit = defineEmits<{ toggle: [] }>()
const slots = useSlots()
const id = useId()
// Only a person's toggle animates; a fold read from their preferences applies at once.
// The body clips only while it moves, so focus rings inside it are never cut off once open.
const moving = ref(false)
const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
function toggle() {
  moving.value = !reduced()
  emit('toggle')
}
function settle(event: TransitionEvent) {
  if (event.target === event.currentTarget && event.propertyName === 'grid-template-rows') moving.value = false
}
</script>

<template>
  <section class="fs glass-card" :class="{ open, moving }" :aria-label="label">
    <div class="fs-head">
      <button type="button" class="fs-tog" :aria-expanded="open" :aria-controls="`${id}-body`" :aria-label="`${label}: ${open ? 'fold' : 'unfold'}`" :data-tip="tip ?? (open ? 'Fold' : 'Unfold')" @click="toggle"><AppIcon name="chevron-right" :size="16" /></button>
      <button v-if="slots.title" type="button" class="fs-title" :aria-expanded="open" :aria-controls="`${id}-body`" @click="toggle"><slot name="title" /></button>
      <slot name="head" />
    </div>
    <div :id="`${id}-body`" class="fs-body" :inert="!open || undefined" @transitionend="settle" @transitioncancel="settle">
      <div class="fs-inner"><slot /></div>
    </div>
  </section>
</template>

<style scoped>
.fs { min-width: 0; padding: 0; }
.fs-head { display: flex; align-items: center; gap: 6px 12px; min-width: 0; min-height: 54px; padding: 8px 14px 8px 8px; }
.fs-tog { display: grid; place-items: center; flex: none; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); cursor: pointer; }
.fs-tog :deep(svg) { transition: transform .22s ease; }
.fs.open > .fs-head .fs-tog :deep(svg) { transform: rotate(90deg); }
.fs-title { display: inline-flex; align-items: center; gap: 10px; flex: none; min-height: 32px; margin-left: -6px; padding: 0 6px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: 650 15px/1.3 var(--font); letter-spacing: -.005em; text-align: left; white-space: nowrap; cursor: pointer; }
.fs-tog:focus-visible, .fs-title:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) {
  .fs-tog:hover { background: var(--row-hover); color: var(--teal-ink); }
  .fs-title:hover { background: var(--row-hover); }
}
.fs-body { display: grid; grid-template-rows: 0fr; visibility: hidden; }
.fs.open > .fs-body { grid-template-rows: 1fr; visibility: visible; }
.fs.moving > .fs-body { transition: grid-template-rows .26s cubic-bezier(.2, .7, .2, 1), visibility 0s .26s; }
.fs.moving.open > .fs-body { transition: grid-template-rows .26s cubic-bezier(.2, .7, .2, 1), visibility 0s 0s; }
.fs-inner { min-height: 0; min-width: 0; }
.fs:not(.open) > .fs-body > .fs-inner, .fs.moving > .fs-body > .fs-inner { overflow: hidden; }
@media (pointer: coarse) {
  .fs-tog { width: 44px; height: 44px; }
  .fs-title { min-height: 44px; margin-left: -8px; }
}
@media (prefers-reduced-motion: reduce) { .fs.moving > .fs-body, .fs-tog :deep(svg) { transition: none; } }
</style>
