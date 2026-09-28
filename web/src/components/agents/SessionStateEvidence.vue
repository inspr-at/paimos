<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { SessionView } from '../../stores/agents'
import { useAgentAppearance } from '../../lib/agentAppearance'
import { duration } from '../../lib/agentState'
import { attentionReasonText, heartbeatEvidence } from '../../lib/agentSignals'
import { absoluteTime } from '../../lib/work'
const props = defineProps<{ view: SessionView; now: number }>()
const { choice } = useAgentAppearance()
const inbox = computed(() => (props.view.session.attention_reasons ?? []).filter(r => !r.blocking || r.scope === 'shared').map(r => ({ ...attentionReasonText(r), location: r.location })))
const beat = computed(() => heartbeatEvidence(props.view.session, props.now))
</script>

<template>
  <section v-if="view.status.reasons?.length" class="state-evidence" aria-label="Session state evidence">
    <ul>
      <li v-for="(reason, index) in view.status.reasons" :key="reason.code">
        <strong v-if="index > 0">{{ reason.detail }}</strong><span>{{ reason.next }}</span>
        <RouterLink v-if="reason.code.includes('approval')" to="/agents#needs-title">Permission requests</RouterLink>
        <a v-else-if="view.run" href="#telemetry-title">Current run</a>
        <a v-else href="#setup-title">Session setup</a>
      </li>
    </ul>
    <p v-if="beat.hasHeartbeat">Last heartbeat: <time :datetime="view.session.heartbeat_at!">{{ absoluteTime(view.session.heartbeat_at!) }}</time> · {{ duration(beat.age) }} ago.</p>
    <p v-else>No valid heartbeat received. Registered {{ absoluteTime(view.session.created_at) }}<template v-if="Number.isFinite(beat.age)"> · {{ duration(beat.age) }} ago</template>.</p>
    <p v-if="view.status.reasons.some(reason => reason.code === 'heartbeat')">Heartbeat warnings start at {{ choice.yellowMinutes }}m; no heartbeat is flagged at {{ choice.redMinutes }}m. Host: {{ view.session.host || 'not reported' }}. Reported state: {{ view.session.phase }} / {{ view.session.activity }}.</p>
  </section>
  <section v-if="inbox.length" class="state-evidence" aria-label="Inbox attention">
    <ul>
      <li v-for="reason in inbox" :key="reason.code"><strong>{{ reason.detail }}</strong><span>{{ reason.next }}</span>
        <RouterLink v-if="reason.location === 'approvals'" to="/agents#needs-title">Permission requests</RouterLink>
        <a v-else href="#messages-title">Shared inbox messages</a>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.state-evidence { padding: 12px; border: 1px solid var(--line); border-radius: 10px; font-size: 12px; color: var(--ink-2); line-height: 1.5; overflow-wrap: anywhere; }
ul { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
li { display: grid; gap: 4px; }
strong { color: var(--ink); font-weight: 550; }
p { margin: 8px 0 0; }
</style>
