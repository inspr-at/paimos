<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A session's avatar (AEON-1211, design AEON-1196): the person's live indicator with its state
// badge and nothing else on it. The harness lives in the Execution column, or in the opener mark
// of a compact row. The state word is its tooltip and part of its accessible name, so the state
// never rests on colour or shape alone.
import { computed } from 'vue'
import LiveBot from '../projects/LiveBot.vue'
import { STATE_LABEL } from '../../lib/agentSignals'
import { useAgents, type SessionView } from '../../stores/agents'
const props = withDefaults(defineProps<{ view: SessionView; size?: number; lead?: boolean; tip?: string }>(), { size: 30, tip: '' })
const agents = useAgents()
const word = computed(() => STATE_LABEL[props.view.status.state])
const label = computed(() => `${props.lead ? 'Lead ' : ''}${props.view.name}: ${word.value}`)
// The precise word ("Awaiting approval", "Lost contact") follows the state when it says more.
const tooltip = computed(() => [word.value, props.view.status.label !== word.value ? props.view.status.label : '', props.tip].filter(Boolean).join(' · '))
</script>

<template>
  <span class="ava" role="img" :data-state="view.status.state" :aria-label="label" :data-tip="tooltip">
    <LiveBot :state="view.status.state" :harness="view.session.harness" :size="size" :lead="lead" :event-pulse="agents.eventPulseFor(view.session.id)" :event-caption="view.session.activity_note ? 'New activity' : ''" aria-hidden="true" />
  </span>
</template>

<style scoped>
.ava { position: relative; display: inline-grid; place-items: center; flex: none; width: fit-content; height: fit-content; }
</style>
