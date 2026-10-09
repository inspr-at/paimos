<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { cooperative, liveSession, pausedSession, pausingSession } from '../../lib/agentPause'
import { useAgentPause } from '../../stores/agentPause'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ session: HarnessSession; compact?: boolean }>()
const pause = useAgentPause()
const running = computed(() => liveSession(props.session)), stopped = computed(() => pausedSession(props.session))
</script>
<template>
  <span v-if="pause.permitted(session)" class="pause-controls">
    <button v-if="running && cooperative(session) && !pausingSession(session) && pause.eligible(session, 'pause')" class="btn sm ghost" :class="{ 'pause-shortcut': compact, 'icon-only': compact }" type="button" :aria-label="compact ? `Pause ${session.display_label || session.agent?.name || session.host}` : undefined" @click.stop="pause.open('pause', [session], $event.currentTarget as HTMLElement)"><AppIcon name="pause" :size="14" /><span v-if="!compact">Pause…</span></button>
    <button v-if="running && !compact" class="btn sm ghost danger" type="button" @click.stop="pause.open('stop', [session], $event.currentTarget as HTMLElement)"><AppIcon name="halt" :size="14" /><span>Stop now…</span></button>
    <button v-if="stopped" class="btn sm ghost" :class="{ 'icon-only': compact }" type="button" :aria-label="compact ? `Resume ${session.display_label || session.agent?.name || session.host}` : undefined" @click.stop="pause.open('resume', [session], $event.currentTarget as HTMLElement)"><AppIcon name="play" :size="14" /><span v-if="!compact">Resume</span></button>
  </span>
</template>
<style scoped>
/* Only the compact buttons are icon-only. `:has(>svg:only-child)` also matched an icon with bare label text,
   because text nodes do not count as children, and squeezed "Stop now…" into 28 px (AEON-1062). */
.pause-controls{display:inline-flex;gap:4px;align-items:center;flex:none}.danger{color:var(--danger)}.pause-controls .btn{white-space:nowrap}.pause-controls .icon-only{width:28px;padding:0}
@media(max-width:600px){.pause-controls .btn{min-height:44px}.pause-controls .icon-only{width:44px}}
</style>
