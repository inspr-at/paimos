<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { computed } from 'vue'
import type { AgentState } from '../../lib/agentSignals'
import { useAgentAppearance } from '../../lib/agentAppearance'
import type { SessionView } from '../../stores/agents'
import AgentStateMark from '../indicators/AgentStateMark.vue'
import { currentStep } from './activity'

// One compact line instead of a chip per session: how many are live, the states
// as jumps into the table below, and the one session in trouble with Open. Names
// live in the table; the line only counts.
const props = defineProps<{ views: SessionView[]; now: number; loaded: boolean }>()
const emit = defineEmits<{ open: [id: string]; jump: [state: AgentState] }>()
const { appearance } = useAgentAppearance()
const live = computed(() => props.views.filter(v => !v.session.stopped_at && v.session.phase !== 'stopped'))
const count = (groups: string[]) => live.value.filter(v => groups.includes(v.status.group)).length
const states = computed(() => ([
  { state: 'working', label: 'working', n: count(['working']), tip: 'Show working sessions' },
  { state: 'waiting', label: 'waiting', n: count(['needs']), tip: 'Show sessions waiting on a tool or a reply' },
  { state: 'throttled', label: 'throttled', n: count(['throttled']), tip: 'Show throttled sessions' },
] as { state: AgentState; label: string; n: number; tip: string }[]).filter(s => s.n))
const trouble = computed(() => live.value.filter(v => v.status.group === 'problem' || v.status.group === 'unresponsive'))
// How long it has been in this state, as people say it: "12 min", "3 h".
function since(view: SessionView) {
  const minutes = Math.max(1, Math.round((props.now - Date.parse(view.session.heartbeat_at ?? view.session.created_at)) / 60_000))
  return minutes < 60 ? `${minutes} min` : minutes < 48 * 60 ? `${Math.round(minutes / 60)} h` : `${Math.round(minutes / 1440)} d`
}
const what = (view: SessionView) => (view.status.reasons?.[0]?.detail || view.status.label).replace(/\.$/, '')
</script>

<template>
  <div class="live-line" aria-label="Live sessions" role="group">
    <span v-if="!loaded" class="skeleton line-skeleton" />
    <template v-else-if="live.length">
      <span class="live-total">{{ live.length }} live</span>
      <button v-for="s in states" :key="s.state" type="button" class="state-count" :style="appearance(s.state)" :data-tip="s.tip" :aria-label="`${s.n} ${s.label}. ${s.tip}`" @click="emit('jump', s.state)">
        <AgentStateMark :state="s.state" :size="12" /><b>{{ s.n }}</b>{{ s.label }}
      </button>
      <span v-if="trouble.length === 1" class="problem-chip" :style="appearance(trouble[0].status.state)" :data-tip="[trouble[0].name, trouble[0].ticket?.key, currentStep(trouble[0])].filter(Boolean).join(' · ')">
        <AgentStateMark :state="trouble[0].status.state" :size="12" />
        <span v-clip-tip class="who">{{ trouble[0].name }}</span><span class="what">{{ what(trouble[0]) }} · {{ since(trouble[0]) }}</span>
        <button type="button" class="btn open" :aria-label="`Open ${trouble[0].name}`" @click="emit('open', trouble[0].session.id)">Open</button>
      </span>
      <span v-else-if="trouble.length > 1" class="problem-chip" :style="appearance('problem')">
        <AgentStateMark state="problem" :size="12" />
        <span class="who">{{ trouble.length }} in trouble</span>
        <button type="button" class="btn open" @click="emit('jump', trouble[0].status.state)">Show</button>
      </span>
    </template>
  </div>
</template>

<style scoped>
.live-line { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 2px; margin-top: 10px; min-height: 32px; min-width: 0; }
.line-skeleton { display: inline-block; width: 260px; height: 14px; }
.live-total { margin-right: 8px; color: var(--ink); font-size: 14px; font-weight: 650; font-variant-numeric: tabular-nums; white-space: nowrap; }
.state-count { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 9px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 13px; font-weight: 550; font-variant-numeric: tabular-nums; white-space: nowrap; }
.state-count b { color: var(--ink); font-weight: 650; }
@media (hover: hover) { .state-count:hover { background: var(--row-hover); color: var(--ink); } }
.state-count:focus-visible { box-shadow: var(--focus-ring); }
/* The one session in trouble: a full soft tint and a hairline ring, never an edge bar. */
.problem-chip { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; height: 32px; margin-left: 10px; padding: 0 4px 0 11px; border-radius: 999px; background: color-mix(in srgb, var(--agent-state-color) 8%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--agent-state-color) 30%, transparent); color: var(--ink); font-size: 13px; }
.problem-chip .who { min-width: 0; overflow: hidden; text-overflow: ellipsis; font-weight: 650; white-space: nowrap; }
.problem-chip .what { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.problem-chip .open { flex: none; height: 24px; padding: 0 10px; font-size: 12px; }
@media (max-width: 720px) {
  .live-line { gap: 0; margin-top: 8px; }
  /* While loading, hold the phone shape of a loaded line (the count, then one
     row of 44px state buttons), so the header below does not jump. */
  .live-line:has(> .line-skeleton) { min-height: 65px; align-content: flex-start; }
  .live-total { width: 100%; margin: 0 0 2px; }
  .state-count { height: 44px; padding: 0 10px 0 0; }
  .state-count + .state-count { padding-left: 8px; }
  .problem-chip { width: 100%; height: 44px; margin: 6px 0 0; padding: 0 4px 0 12px; }
  .problem-chip .who { flex: 1; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; overflow-wrap: anywhere; line-height: 16px; }
  .problem-chip .what { flex: 1; }
  .problem-chip .open { height: 34px; padding: 0 14px; }
}
</style>
