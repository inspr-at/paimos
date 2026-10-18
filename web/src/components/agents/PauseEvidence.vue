<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { clockTime, levelName, liveSession } from '../../lib/agentPause'
import { useSession } from '../../stores/session'
const viewer = useSession()
const props = defineProps<{ session: HarnessSession; now: number }>()
const pause = computed(() => props.session.pause), handover = computed(() => pause.value?.handover)
const countdown = computed(() => Math.max(0, Math.ceil((Date.parse(pause.value?.deadline_at || '') - props.now) / 60_000)))
</script>
<template>
  <section v-if="pause && pause.state !== 'cancelled' && pause.state !== 'resumed'" class="pause-evidence" aria-label="Pause and handover">
    <template v-if="liveSession(session)"><strong>{{ pause.stop_requested ? 'Stop requested · awaiting exit report' : `${levelName(pause.level)} requested` }}</strong><p v-if="pause.deliver === false && pause.starts_at">Starts {{ clockTime(pause.starts_at) }}</p><p v-else>By {{ clockTime(pause.deadline_at) }} · {{ countdown }} min left<template v-if="countdown === 0"> · deadline reached; exit unconfirmed</template></p><p v-if="pause.handover_point">Handover point: {{ pause.handover_point }}</p></template>
    <template v-else><strong>{{ pause.state === 'resume_requested' ? 'Resume requested · awaiting continuation' : pause.state === 'paused' ? 'Paused' : 'Ended' }}</strong><p>{{ levelName(pause.level) }} · {{ pause.requested_by_principal_id === viewer.identity?.principal.id ? 'by you' : 'by another controller' }} · {{ clockTime(pause.paused_at || pause.requested_at) }}<template v-if="pause.reason"> · {{ pause.reason }}</template></p></template>
    <p v-if="pause.note">Note for the handover: {{ pause.note }}</p>
    <template v-if="handover"><p>{{ handover.state }}</p><h3>Next steps</h3><ol><li v-for="step in handover.next_steps" :key="step">{{ step }}</li></ol><template v-if="handover.open_questions.length"><h3>Open questions</h3><ul><li v-for="question in handover.open_questions" :key="question">{{ question }}</li></ul></template><p>Work in progress: {{ handover.worktree_state }}<template v-if="handover.commit_sha"> · <code>{{ handover.commit_sha }}</code></template></p></template>
  </section>
</template>
<style scoped>
.pause-evidence{padding:14px 0;border-block:1px solid var(--line);display:grid;gap:8px;font-size:13px}.pause-evidence p{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}.pause-evidence h3{font-size:12px;font-weight:600}.pause-evidence ol,.pause-evidence ul{margin:0;padding-left:20px}.pause-evidence li{overflow-wrap:anywhere}
</style>
