<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow "At this moment" (AEON-994 draft 5, package 6): left, the time and one
// plain sentence, then one line per active lane and one idle line; right, the selected
// step or the incident state. Fixed height (196 px, 300 px stacked on phones), rows of
// fixed height, so moving the time never moves anything around it.
import AppIcon from '../AppIcon.vue'
import type { Moment } from '../../lib/deliveryFlowModes'
import type { FlowText } from '../../lib/deliveryFlowText'

defineProps<{ moment: Moment; text: FlowText; focusKey: string | null }>()
const lineText = (parts: (string | number | false | null | undefined)[]) => parts.filter(Boolean).join(' ')
</script>

<template>
  <div class="fl-moment" role="region" :aria-label="text.moment" data-testid="flow-moment">
    <div class="m-left">
      <p class="m-head" :title="`${moment.head} ${moment.sentence}`" data-testid="flow-moment-head"><b>{{ moment.head }}</b> <span class="m-sum">{{ moment.sentence }}</span></p>
      <ul>
        <li v-for="line in moment.lines" :key="line.key" :class="{ focus: line.key === focusKey }"
          :title="lineText([line.who, line.label, line.expected && `(${text.expected})`, line.tail, line.more > 0 && `+${line.more} ${text.more}`])">
          <b>{{ line.who }}</b>
          <span class="k" :class="line.kind"><AppIcon v-if="line.kind === 'wait'" name="clock" :size="11" /><AppIcon v-else-if="line.kind === 'inc'" name="alert" :size="11" /><AppIcon v-else-if="line.kind === 'done'" name="check" :size="11" />{{ line.label }}</span>
          <span v-if="line.expected" class="mu">{{ ` (${text.expected})` }}</span>
          <span v-if="line.tail" class="mu">{{ ` ${line.tail}` }}</span>
          <span v-if="line.more > 0" class="mu">{{ ` +${line.more} ${text.more}` }}</span>
        </li>
        <li v-if="moment.idle" class="idle" :title="`${text.idleHead} ${moment.idle}`"><b>{{ text.idleHead }}</b><span class="mu">{{ moment.idle }}</span></li>
      </ul>
    </div>
    <div class="m-right" aria-live="polite" data-testid="flow-moment-detail">
      <template v-if="moment.detail">
        <p class="mt" :title="`${moment.detail.title} · ${moment.detail.tag}`"><b>{{ moment.detail.title }}</b><span class="mu"> · {{ moment.detail.tag }}</span></p>
        <dl>
          <template v-for="[term, value] in moment.detail.rows" :key="term"><dt>{{ term }}</dt><dd :title="value">{{ value }}</dd></template>
        </dl>
      </template>
      <template v-else-if="moment.incident">
        <p class="inc-on"><AppIcon name="alert" :size="13" /><span><b>{{ text.incOn }}</b> · {{ moment.incident.title }}</span></p>
        <p class="mu">{{ moment.incident.meta }}</p>
        <p class="mu">{{ text.clickSeg }}</p>
      </template>
      <template v-else>
        <p class="mu calm"><AppIcon name="check" :size="13" />{{ text.noInc }}</p>
        <p class="mu">{{ text.clickSeg }}</p>
      </template>
    </div>
  </div>
</template>

<style scoped>
.fl-moment { display: grid; grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr); gap: 20px; height: 196px; margin-top: 8px; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); overflow: hidden; }
.m-left, .m-right { min-width: 0; overflow: hidden; }
.m-head { height: 38px; margin: 0; font-size: 13px; line-height: 19px; color: var(--ink); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.m-head b { font-weight: 650; }
.m-sum { color: var(--ink-2); }
ul { display: grid; gap: 1px; margin: 6px 0 0; padding: 0; list-style: none; }
li { height: 19px; padding: 0 4px; margin: 0 -4px; border-radius: 4px; font-size: 12px; line-height: 19px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
li b { display: inline-block; min-width: 128px; margin-right: 6px; font-weight: 600; color: var(--ink); }
li.focus { background: var(--row-selected); }
.mu { color: var(--ink-3); }
.k { color: var(--ink); }
.k svg { display: inline-block; vertical-align: -1px; margin-right: 3px; }
.k.wait { color: var(--queue-wait-ink); }
.k.rework, .k.inc { color: var(--danger); }
.k.done { color: var(--ok); }
.mt { margin: 0 0 6px; font-size: 13px; line-height: 1.4; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.mt b { font-weight: 650; color: var(--ink); }
dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 2px 12px; margin: 0; font-size: 12px; }
dt { color: var(--ink-3); }
dd { margin: 0; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-numeric: tabular-nums; }
.m-right p { margin: 0 0 4px; font-size: 12.5px; line-height: 1.5; }
.inc-on { display: flex; align-items: flex-start; gap: 6px; color: var(--danger); }
.inc-on svg { flex: none; margin-top: 3px; }
.inc-on b { font-weight: 650; }
.calm { display: flex; align-items: center; gap: 6px; }
.calm svg { color: var(--ok); }
@container delivery (max-width: 640px) {
  .fl-moment { grid-template-columns: minmax(0, 1fr); height: 300px; gap: 10px; }
  .m-head { height: 57px; -webkit-line-clamp: 3; }
  li b { min-width: 0; }
}
</style>
