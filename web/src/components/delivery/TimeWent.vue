<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow Replay and Compare: where the time went (AEON-994 draft 5, package 6).
// Per run, the critical path split into working, waiting, doing it again and incident +
// recovery, the hand-overs, and the biggest waits and repeats.
import AppIcon from '../AppIcon.vue'
import type { Went } from '../../lib/deliveryFlowModes'
import type { FlowText } from '../../lib/deliveryFlowText'

defineProps<{ runs: Went[]; text: FlowText }>()
const icon = { wait: 'clock', rework: 'refresh', inc: 'alert', work: 'check' } as const
</script>

<template>
  <section class="fl-went" :aria-label="text.went" data-testid="flow-went">
    <h3>{{ text.went }}</h3>
    <div v-for="run in runs" :key="run.key" class="went">
      <div class="went-head"><b>{{ run.title }}</b><span class="mu">{{ run.note }}</span></div>
      <div class="went-bar" aria-hidden="true"><i v-for="part in run.parts" :key="part.kind" :class="`c-${part.kind}`" :style="{ width: `${part.share * 100}%` }" /></div>
      <div class="went-parts"><span v-for="part in run.parts" :key="part.kind" :class="`k-${part.kind}`"><i :class="`c-${part.kind}`" aria-hidden="true" />{{ part.label }}</span></div>
      <ul v-if="run.items.length" class="went-list">
        <li v-for="item in run.items" :key="item.key" :class="item.kind" :title="`${item.minutes} ${item.label}${item.note ? ` (${item.note})` : ''}`">
          <AppIcon :name="icon[item.kind]" :size="12" /><b>{{ item.minutes }}</b><span class="txt">{{ item.label }}<span v-if="item.note" class="mu"> ({{ item.note }})</span></span>
        </li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.fl-went { margin-top: 18px; }
h3 { margin: 0 0 8px; font: 650 14px/1.3 var(--font); color: var(--ink); }
.went { padding: 12px 0; border-top: 1px solid var(--line); }
.went-head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; font-size: 13px; }
.went-head b { font-weight: 650; color: var(--ink); }
.mu { font-size: 12px; color: var(--ink-3); }
.went-bar { display: flex; gap: 2px; height: 12px; margin-top: 8px; border-radius: 6px; overflow: hidden; }
.went-bar i, .went-parts i { display: block; height: 100%; }
.went-parts i { display: inline-block; width: 10px; height: 10px; margin-right: 6px; border-radius: 3px; vertical-align: -1px; }
.c-work { background: color-mix(in srgb, var(--teal) 45%, transparent); }
.c-rework { background: color-mix(in srgb, var(--danger) 40%, transparent); }
.c-wait { background: color-mix(in srgb, var(--gold) 50%, transparent); }
.c-inc { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--danger) 55%, transparent) 0 4px, color-mix(in srgb, var(--danger) 25%, transparent) 4px 8px); }
.went-parts { display: flex; flex-wrap: wrap; gap: 4px 18px; margin-top: 6px; font-size: 12.5px; color: var(--ink-2); }
.went-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 4px 24px; margin: 8px 0 0; padding: 0; list-style: none; font-size: 12.5px; color: var(--ink-2); }
.went-list li { display: flex; align-items: center; gap: 6px; min-width: 0; }
.went-list li svg { flex: none; }
.went-list li.wait svg { color: var(--queue-wait-ink); }
.went-list li.rework svg, .went-list li.inc svg { color: var(--danger); }
.went-list b { flex: none; color: var(--ink); font-weight: 650; }
.txt { min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
@container delivery (max-width: 640px) {
  .went-list { grid-template-columns: minmax(0, 1fr); }
  .went-parts { gap: 4px 12px; }
  .txt { white-space: normal; }
}
</style>
