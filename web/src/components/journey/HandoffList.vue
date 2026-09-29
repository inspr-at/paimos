<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { BLOCKER_LABEL, type Handoff } from '../../lib/journey'
import { absoluteTime, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// Stage handoffs to a plugin (Pharos deploys and verifies, Janus prepares and
// applies access) as a compact timeline: attempt, operation, state and result.
defineProps<{ handoffs: Handoff[]; now: number; empty: string }>()
const OPERATION: Record<Handoff['operation'], string> = { prepare: 'Prepare access', apply: 'Apply access', deploy: 'Deploy', verify: 'Verify' }
const STATE: Record<Handoff['state'], string> = { requested: 'Requested', active: 'Running', blocked: 'Blocked', succeeded: 'Succeeded', failed: 'Failed', revoked: 'Revoked' }
const tone = (h: Handoff) => h.state === 'succeeded' ? 'ok' : h.state === 'failed' || h.state === 'blocked' || h.state === 'revoked' ? 'bad' : 'run'
const icon = (h: Handoff) => h.state === 'succeeded' ? 'check' : h.state === 'failed' || h.state === 'revoked' ? 'close' : h.state === 'blocked' ? 'alert' : 'clock'
</script>

<template>
  <ol v-if="handoffs.length" class="j-timeline" aria-label="Handoffs">
    <li v-for="handoff in handoffs" :key="handoff.id">
      <span class="dot" :class="tone(handoff)" aria-hidden="true"><AppIcon :name="icon(handoff)" :size="11" /></span>
      <span class="what">
        <span>{{ OPERATION[handoff.operation] }} · <b>{{ handoff.plugin_id === 'pharos' ? 'Pharos' : handoff.plugin_id === 'janus' ? 'Janus' : handoff.plugin_id }}</b> · attempt {{ handoff.attempt }}</span>
        <small>{{ STATE[handoff.state] }}<template v-if="handoff.result?.blocker_code"> · {{ BLOCKER_LABEL[handoff.result.blocker_code] ?? handoff.result.blocker_code.replace(/_/g, ' ') }}</template><template v-if="handoff.state === 'requested' || handoff.state === 'active'"> · expires {{ relativeTime(handoff.expires_at, { now }) }}</template></small>
      </span>
      <time v-if="handoff.result" :datetime="handoff.result.completed_at" :data-tip="absoluteTime(handoff.result.completed_at)">{{ relativeTime(handoff.result.completed_at, { now }) }}</time>
      <span v-else class="when">open</span>
    </li>
  </ol>
  <p v-else class="j-note">{{ empty }}</p>
</template>

<style scoped>
/* Deploy and Access share this list; provider names must fit beside the time. */
.j-timeline, .what { grid-template-columns: minmax(0, 1fr); min-width: 0; }
.j-timeline > li { min-width: 0; }
.what { overflow-wrap: anywhere; }
</style>
