<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import HarnessMark from '../agents/HarnessMark.vue'
const props = defineProps<{ model?: string | null; effort?: string | null }>()
const levels = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']
const level = computed(() => levels.indexOf(props.effort ?? ''))
// Names come from the model registry; retain the model version in the short label.
const harness = computed(() => /gpt|codex/i.test(props.model ?? '') ? 'codex' : /claude|opus|sonnet|fable/i.test(props.model ?? '') ? 'claude' : /grok/i.test(props.model ?? '') ? 'grok' : '')
const label = computed(() => (props.model ?? '').replace(/^gpt-(\d+(?:\.\d+)?)-(\w+)$/, '$2 $1').replace(/^claude-/, '').replace(/^(\w+)-(\d+(?:\.\d+)?)$/, '$1 $2').replace(/^\w/, letter => letter.toUpperCase()))
</script>
<template>
  <span v-if="model" class="queue-model" :data-tip="`${model}${effort ? ` · effort ${effort}` : ''}`">
    <HarnessMark v-if="harness" :harness="harness" :size="12" />
    <svg v-if="level >= 0" class="effort" width="6.5" height="12" viewBox="0 0 6.5 12" role="img" :aria-label="`Effort ${effort}, ${level} of 5`">
      <rect v-for="i in 5" :key="i" x=".25" :y="10.25 - (i - 1) * 2.5" width="6" height="1.5" rx=".4" :class="{ on: i <= level }" />
    </svg><span>{{ label }}</span>
  </span>
</template>
<style scoped>
.queue-model { display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-2); font-size: 12.5px; white-space: nowrap; }
.queue-model > span { overflow: hidden; text-overflow: ellipsis; }
.effort { flex: none; overflow: visible; }
rect { fill: none; stroke: var(--ink-3); stroke-width: .5; }
rect.on { fill: var(--teal); stroke: var(--teal); }
</style>
