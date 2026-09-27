<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import LiveBot from '../projects/LiveBot.vue'
import type { SessionView } from '../../stores/agents'
import { useAgents } from '../../stores/agents'
withDefaults(defineProps<{ view: SessionView; size?: number }>(), { size: 34 })
const agents = useAgents()
</script>

<template>
  <span class="agent-glyph"><LiveBot :state="view.status.state" :harness="view.session.harness" :size="size" :event-pulse="agents.eventPulseFor(view.session.id)" :event-caption="view.session.activity_note ? 'New activity' : ''" /></span>
</template>

<style scoped>
.agent-glyph { display: inline-grid; place-items: center; flex: none; width: fit-content; height: fit-content; }
</style>
