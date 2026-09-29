<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { listeningState } from './listening'

// A quiet delivery-path cue (AEON-280). Compact shows the word only; the detail
// ("last pulled 12m ago") moves to the tooltip. Colour is never the only signal:
// the glyph and the word both change.
const props = defineProps<{ session: HarnessSession; now: number; compact?: boolean }>()
const state = computed(() => listeningState(props.session, props.now))
const text = computed(() => state.value ? (state.value.detail && !props.compact ? `${state.value.label} · ${state.value.detail}` : state.value.label) : '')
</script>

<template>
  <span v-if="state" class="listening" :class="{ on: state.listening, compact }" :data-listening="state.listening ? 'yes' : 'no'" :data-tip="state.tip" :aria-label="`${state.label}${state.detail ? `, ${state.detail}` : ''}. ${state.tip}`" role="img">
    <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
      <circle cx="8" cy="8" r="1.3" fill="currentColor" stroke="none" />
      <path d="M5.3 5.3a3.8 3.8 0 0 0 0 5.4M10.7 5.3a3.8 3.8 0 0 1 0 5.4" />
      <path v-if="state.listening" d="M3.1 3.1a6.9 6.9 0 0 0 0 9.8M12.9 3.1a6.9 6.9 0 0 1 0 9.8" />
      <path v-else d="m2.6 2.6 10.8 10.8" />
    </svg>
    <span class="listening-text">{{ text }}</span>
  </span>
</template>

<style scoped>
.listening { display: inline-flex; align-items: center; gap: 5px; min-width: 0; max-width: 100%; font-size: 12px; line-height: 1.3; color: var(--ink-3); white-space: nowrap; }
.listening svg { flex: none; }
.listening.on { color: var(--ink-2); }
.listening.on svg { color: var(--teal-ink); }
.listening-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.listening.compact { font-size: 11.5px; }
</style>
