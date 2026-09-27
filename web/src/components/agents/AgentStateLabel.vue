<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { STATE_LABEL, type AgentState } from '../../lib/agentSignals'
import { useAgentAppearance } from '../../lib/agentAppearance'
import AgentStateMark from '../indicators/AgentStateMark.vue'
defineProps<{ state: AgentState; detail?: string }>()
const { appearance } = useAgentAppearance()
</script>
<template>
  <span class="agent-state-label" :data-state="state" :style="appearance(state)" :aria-label="detail ? `${STATE_LABEL[state]}: ${detail}` : STATE_LABEL[state]">
    <AgentStateMark :state="state" /><span class="state-word">{{ STATE_LABEL[state] }}<small v-if="detail" class="state-detail">{{ detail }}</small></span>
  </span>
</template>
<style scoped>
.agent-state-label { display: inline-flex; align-items: center; gap: 5px; color: var(--agent-state-color); font-size: 12px; font-weight: 600; line-height: 1.3; }
.state-word { display: grid; gap: 2px; }
.state-detail { font-size: 11px; font-weight: 450; color: var(--ink-2); }
</style>
