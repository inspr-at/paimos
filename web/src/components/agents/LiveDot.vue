<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { LiveTone } from '../../lib/agentState'
import type { AgentState } from '../../lib/agentSignals'
import AgentStateLabel from './AgentStateLabel.vue'
// Legacy tone adapter for ticket/session chips. Always include a shape and word.
withDefaults(defineProps<{ tone: LiveTone; size?: number }>(), { size: 14 })
const states: Record<LiveTone, AgentState> = { busy: 'working', attention: 'waiting', throttled: 'throttled', problem: 'problem', idle: 'idle', quiet: 'stale', stopped: 'stopped' }
</script>
<template><AgentStateLabel class="live-dot" :state="states[tone]" /></template>
