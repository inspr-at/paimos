<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { QueuedTicket } from '../../lib/workQueue'
import { absoluteTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import QueueModel from './QueueModel.vue'
defineProps<{ entry: QueuedTicket; manual?: boolean }>()
</script>
<template>
  <div class="queue-details">
    <p class="hc-head" :class="{ wait: entry.waiting_reason }"><AppIcon :name="entry.waiting_reason ? 'clock' : 'queue'" :size="14" /><span>{{ entry.waiting_reason ? 'Waiting' : 'Queued' }} · #{{ entry.position }}</span></p>
    <p v-if="entry.waiting_reason" class="hc-reason">{{ entry.waiting_reason }}</p>
    <dl class="hc-grid">
      <dt>Queued</dt><dd>by <span :class="{ 'by-agent': entry.by.kind === 'agent' }">{{ entry.by.name }}</span> · {{ absoluteTime(entry.at) }}</dd>
      <dt>Position</dt><dd>#{{ entry.position }} {{ entry.target_agent_id ? `for ${entry.expected_agent?.name ?? 'the chosen agent'}` : manual ? '· manual order' : '· priority order' }}</dd>
      <dt>Expected</dt><dd><QueueModel :model="entry.expected_model" :effort="entry.expected_effort" /><span class="hc-agent">{{ entry.expected_agent?.name ?? 'The next free agent' }}</span></dd>
      <dt>Starts</dt><dd>{{ entry.expected_start ? `~${absoluteTime(entry.expected_start)}` : entry.state === 'blocked' ? 'Once unblocked' : 'When capacity is available' }}</dd>
    </dl>
    <p class="hc-foot">{{ entry.target_agent_id ? 'Start now chose this agent; it skips the shared line.' : 'Priority first, then first come; manual moves win until reset.' }}</p>
  </div>
</template>
<style scoped>
.hc-head { display: flex; align-items: center; gap: 8px; color: var(--ink); font-size: 13px; font-weight: 650; }
.hc-head > svg { color: var(--teal-ink); }
.hc-head.wait, .hc-head.wait > svg { color: var(--queue-wait-ink); }
.hc-reason { margin-top: 7px; padding: 6px 9px; border-radius: 8px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font-size: 12px; }
.hc-grid { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 5px 12px; margin: 9px 0 0; }
.hc-grid dt { font: 500 10px/18px var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); }
.hc-grid dd { min-width: 0; margin: 0; color: var(--ink-2); }
.hc-agent { margin-left: 6px; color: var(--ink-3); }
.hc-foot { margin-top: 9px; padding-top: 8px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
.by-agent { padding: 2px 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 10.5px/18px var(--mono); }
</style>
