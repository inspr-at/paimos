<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { vClipTip } from '../../directives/clipTip'

// One ticket per chosen open point. The parent box is ticked, empty or mixed;
// the count reads "3 of 4". While the draft loads, four rows of the final
// height stand in, so a draft with four points arrives without moving anything.
const props = defineProps<{ points: string[]; chosen: number[]; loading: boolean; disabled: boolean; label: string; all: string; count: (n: number, of: number) => string }>()
const emit = defineEmits<{ 'update:chosen': [indices: number[]] }>()
const SKELETON = ['74%', '66%', '52%', '70%']
const picked = computed(() => new Set(props.chosen))
const every = computed(() => props.points.length > 0 && props.chosen.length === props.points.length)
const some = computed(() => props.chosen.length > 0 && !every.value)
function toggleAll() { emit('update:chosen', every.value ? [] : props.points.map((_, index) => index)) }
function toggle(index: number) {
  emit('update:chosen', picked.value.has(index) ? props.chosen.filter(i => i !== index) : [...props.chosen, index].sort((a, b) => a - b))
}
</script>

<template>
  <div class="np-f np-tk" :class="{ locked: disabled }">
    <span class="np-lab"><b>{{ label }}</b><span class="cnt" aria-live="polite">{{ loading ? '' : count(chosen.length, points.length) }}</span></span>
    <label class="tk-all"><input id="np-tk-all" type="checkbox" class="check-box" :checked="loading || every" :indeterminate="!loading && some" :disabled="loading || disabled" @change="toggleAll"><span>{{ all }}</span></label>
    <ul class="tk-list">
      <template v-if="loading"><li v-for="width in SKELETON" :key="width" class="tk sk"><span class="sk-box" /><span class="skeleton" :style="{ width }" /></li></template>
      <template v-else><li v-for="(point, index) in points" :key="index"><label class="tk"><input type="checkbox" class="check-box" :checked="picked.has(index)" :disabled="disabled" @change="toggle(index)"><span v-clip-tip>{{ point }}</span></label></li></template>
    </ul>
  </div>
</template>

<style scoped>
.np-f { display: grid; gap: 6px; min-width: 0; align-content: start; }
.np-lab { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.np-lab > b { font-size: 13px; font-weight: 600; }
.cnt { font-size: 12px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.tk-all, .tk { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 28px; cursor: pointer; }
.tk-all span { font-size: 13.5px; color: var(--ink); }
.tk-list { list-style: none; margin: 0; padding: 0 0 0 26px; display: grid; }
.tk span { min-width: 0; font-size: 13px; line-height: 1.4; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tk:has(.check-box:not(:checked)) span { color: var(--ink-3); }
.tk.sk { cursor: default; }
.sk-box { width: 16px; height: 16px; flex: none; border-radius: 4px; background: var(--skeleton); }
.tk.sk .skeleton { flex: none; }
.locked .check-box:disabled { opacity: .55; }
.locked .tk-all span, .locked .tk span { color: var(--ink-3); }
@media (pointer: coarse), (max-width: 600px) {
  .tk-all, .tk { min-height: 44px; }
  .tk span { white-space: normal; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
}
</style>
