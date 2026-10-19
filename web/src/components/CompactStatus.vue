<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The list toolbar's status slot on phones and short screens (AEON-1459, design AEON-1279 r2 D3/D5):
// the footer's summary moved here when the footer bar went. The page's state mark and its count;
// short landscape screens add the word; under 380 px it steps aside. The words, the footer's one
// exception and whether updates arrive are in its accessible name and its tip.
import { computed } from 'vue'
import { useAgentAppearance } from '../lib/agentAppearance'
import type { AgentState } from '../lib/agentSignals'
import { footerLive, footerSummary } from '../lib/footerSummary'
import AgentStateMark from './indicators/AgentStateMark.vue'

const props = defineProps<{ count: number; word: string; mark: AgentState }>()
const { appearance } = useAgentAppearance()
const paused = computed(() => !!footerLive.value?.paused)
// The footer names one exception (a problem, a question); the count's own words come first.
const exception = computed(() => (footerSummary.value?.full ?? []).filter(part => part.as === 'exception').map(part => part.text).join(', '))
const label = computed(() => `${[`${props.count} ${props.word}`, exception.value].filter(Boolean).join(', ')}. ${paused.value ? 'Updates paused, retrying' : 'Live'}`)
</script>

<template>
  <span class="compact-status" :class="{ paused }" role="status" :aria-label="label" :data-tip="label" :style="appearance(mark)">
    <AgentStateMark :state="mark" :size="14" aria-hidden="true" /><b aria-hidden="true">{{ count.toLocaleString('en-GB') }}</b><span class="cs-w" aria-hidden="true">&nbsp;{{ word }}</span>
  </span>
</template>

<style scoped>
.compact-status { display: none; align-items: center; gap: 6px; flex: none; height: 44px; padding: 0 8px 0 4px; color: var(--ink-2); font-size: 12.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.compact-status b { color: var(--ink); font-weight: 650; }
.compact-status .cs-w { margin-left: -6px; }
.compact-status.paused { color: var(--ink-3); }
.compact-status.paused :deep(.agent-state-mark) { opacity: .45; }
.cs-w { display: none; }
@media (max-width: 719px), (max-height: 500px) { .compact-status { display: inline-flex; } }
/* Short landscape has the room for the word. */
@media (max-height: 500px) and (min-width: 720px) { .cs-w { display: inline; } }
/* Under 380 px it steps aside: the toolbar's tools keep their 44 px. */
@media (max-width: 379px) { .compact-status { display: none; } }
</style>
