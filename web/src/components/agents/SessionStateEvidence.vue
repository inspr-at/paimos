<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { SessionView } from '../../stores/agents'
import { useAgentAppearance } from '../../lib/agentAppearance'
import { duration } from '../../lib/agentState'
import { heartbeatEvidence } from '../../lib/agentSignals'
import { absoluteTime } from '../../lib/work'
const props = defineProps<{ view: SessionView; now: number }>()
const { choice } = useAgentAppearance()
const beat = computed(() => heartbeatEvidence(props.view.session, props.now))
// The heartbeat line matters only when the state is about reporting or failure;
// otherwise the panel's "heartbeat just now" already says it.
const beatMatters = computed(() => ['problem', 'awaiting', 'unresponsive', 'stale', 'idle'].includes(props.view.status.state))
</script>

<template>
  <section v-if="view.status.reasons?.length" class="state-evidence" aria-label="Session state evidence">
    <ul>
      <li v-for="(reason, index) in view.status.reasons" :key="reason.code">
        <strong v-if="index > 0">{{ reason.detail }}</strong><span>{{ reason.next }}</span>
        <RouterLink v-if="reason.code.includes('approval')" to="/agents#needs-title">Permission requests</RouterLink>
      </li>
    </ul>
    <template v-if="beatMatters">
    <p v-if="beat.hasHeartbeat">Last heartbeat: <time :datetime="view.session.heartbeat_at!">{{ absoluteTime(view.session.heartbeat_at!) }}</time> · {{ duration(beat.age) }} ago.</p>
    <p v-else>No valid heartbeat received. Registered {{ absoluteTime(view.session.created_at) }}<template v-if="Number.isFinite(beat.age)"> · {{ duration(beat.age) }} ago</template>.</p>
    <p v-if="view.status.reasons.some(reason => reason.code === 'heartbeat')">Heartbeat warnings start at {{ choice.yellowMinutes }}m; no heartbeat is flagged at {{ choice.redMinutes }}m. Host: {{ view.session.host || 'not reported' }}. Reported state: {{ view.session.phase }} / {{ view.session.activity }}.</p>
    </template>
  </section>
</template>

<style scoped>
/* Quiet supporting text under the current step; no box, no link to a section below. */
.state-evidence { display: grid; gap: 4px; margin-top: 4px; font-size: 12.5px; color: var(--ink-2); line-height: 1.5; overflow-wrap: anywhere; }
ul { list-style: none; padding: 0; margin: 0; display: grid; gap: 4px; }
li { display: block; }
li > * + * { margin-left: 4px; }
strong { color: var(--ink); font-weight: 550; }
a { color: var(--teal-ink); font-weight: 550; }
p { margin: 0; color: var(--ink-3); font-size: 12px; }
</style>
