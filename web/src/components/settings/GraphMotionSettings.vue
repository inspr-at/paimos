<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref } from 'vue'
import { GRAPH_MOTION_KEY, useGraphMotion, type GraphMotionPace } from '../../lib/graphMotion'
import { onPreferenceFailure } from '../../lib/preferences'

const OPTIONS: { pace: GraphMotionPace; label: string }[] = [
  { pace: 'slow', label: 'Slow' },
  { pace: 'default', label: 'Default' },
  { pace: 'lively', label: 'Lively' },
  { pace: 'off', label: 'Off' },
]
const { choice, setPace } = useGraphMotion()
const failed = ref(false)
const group = ref<HTMLElement>()
onBeforeUnmount(onPreferenceFailure(key => { if (key === GRAPH_MOTION_KEY) failed.value = true }))
function choose(pace: GraphMotionPace) { failed.value = false; setPace(pace) }
function keys(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const index = OPTIONS.findIndex(option => option.pace === choice.value.pace)
  const next = OPTIONS[(index + (event.key === 'ArrowRight' ? 1 : -1) + OPTIONS.length) % OPTIONS.length]
  choose(next.pace)
  void nextTick(() => group.value?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
}
function retry() { failed.value = false; setPace(choice.value.pace) }
</script>

<template>
  <div class="graph-motion">
    <div class="motion-row">
      <div class="motion-copy">
        <p id="graph-motion-label" class="motion-label">Graph motion</p>
        <p id="graph-motion-hint" class="hint">One full turn: Slow 3 min, Default 2 min, Lively 1 min. Off holds the graph still. The knowledge graph, ticket graph and project glimpse share this.</p>
      </div>
      <div ref="group" class="seg" role="radiogroup" aria-labelledby="graph-motion-label" aria-describedby="graph-motion-hint" @keydown="keys">
        <button v-for="option in OPTIONS" :key="option.pace" type="button" role="radio" :aria-checked="choice.pace === option.pace" :tabindex="choice.pace === option.pace ? 0 : -1" @click="choose(option.pace)">{{ option.label }}</button>
      </div>
    </div>
    <p v-if="failed" class="save-error" role="alert">Your graph motion setting could not be saved.<button class="btn sm" type="button" @click="retry">Try again</button></p>
  </div>
</template>

<style scoped>
.graph-motion { margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--line); }
.motion-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.motion-copy { min-width: 0; }
.motion-label { padding: 0; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.hint { margin-top: 3px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.seg { flex-shrink: 0; }
.save-error { display: flex; align-items: center; gap: 8px; margin-top: 12px; font-size: 12.5px; color: var(--danger); }
@media (max-width: 600px) {
  .motion-row { align-items: stretch; flex-direction: column; }
  .seg { width: 100%; display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .seg button { height: 40px; padding: 0 6px; }
}
</style>
