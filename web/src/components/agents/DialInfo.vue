<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1036: the one info area under "● 11 running (1 waiting) ▾", full width between
// the dial's divider and "Each harness may use". Three calm tiles, read-only: what
// runs and why work waits (demand against the dial), the accounts per harness with
// the existing Verify action, and the checks before every start.
import { computed } from 'vue'
import { useDialVerify } from '../../lib/useDialVerify'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import VerificationButton from './VerificationButton.vue'

export interface InfoHarness { key: string; label: string; places: number | null; words: string }
const props = defineProps<{ id: string; total: number; running: number; waiting: number | null; now: string; waits: string; accountsLine: string; harnesses: InfoHarness[] }>()
const { problems, verify } = useDialVerify()
const need = computed(() => props.running + (props.waiting ?? 0))
const checks = computed(() => [`Fewer than ${props.total} running`, 'The harness is not off or at its limit', 'A free place on an account', 'Under today’s limit'])
</script>

<template>
  <div :id="id" class="wh" role="region" aria-label="What happens">
    <div class="wts">
      <section class="wt wt-run">
        <h3 class="wt-h"><span class="wt-ic"><AppIcon name="agent" :size="15" /></span>Running and waiting</h3>
        <p class="wt-big"><span><b>{{ running }}</b> running</span><span v-if="waiting !== null" :class="{ 'wt-wait': waiting > 0 }"><b>{{ waiting }}</b> waiting</span></p>
        <p class="wt-l">
          <template v-if="!waiting">{{ waiting === null ? '' : 'Nothing waiting · ' }}the dial allows {{ total }}</template>
          <template v-else-if="need > total"><b>{{ need }} needed</b> · the dial allows {{ total }}</template>
          <template v-else><b>{{ need }} needed now</b> · {{ running }} running + {{ waiting }} waiting · the dial allows {{ total }}</template>
        </p>
        <p class="wt-s f-now">{{ now }}</p>
        <p v-if="waits" class="wt-s f-wait">{{ waits }}</p>
      </section>
      <section class="wt wt-acc">
        <h3 class="wt-h"><span class="wt-ic"><AppIcon name="monitor" :size="15" /></span>Accounts</h3>
        <p class="wt-l">{{ accountsLine }}</p>
        <ul class="wa">
          <li v-for="h in harnesses" :key="h.key" :data-info-harness="h.key">
            <HarnessMark :harness="h.key" :size="14" /><span class="wa-n">{{ h.label }}</span><span class="wa-c">{{ h.places ?? '' }}</span>
            <span class="wa-s" aria-live="polite">{{ problems[h.key]?.feedback || problems[h.key]?.text || h.words }}</span>
            <VerificationButton v-if="problems[h.key]?.mayVerify || problems[h.key]?.busy" class="sm" :busy="problems[h.key]!.busy" @click="verify(problems[h.key]!)" />
          </li>
        </ul>
      </section>
      <section class="wt wt-chk">
        <h3 class="wt-h"><span class="wt-ic"><AppIcon name="shield" :size="15" /></span>Before every start</h3>
        <ul class="wc"><li v-for="c in checks" :key="c"><AppIcon name="check" :size="13" /><span>{{ c }}</span></li></ul>
        <p class="wt-s">Lowering the dial or turning a harness off never stops anyone; running agents finish.</p>
      </section>
    </div>
  </div>
</template>

<style scoped>
p { margin: 0; }
/* Expanded: full width between the dial's divider and "Each harness may use"; it only moves what is below. */
.wh { margin: -2px 0 16px; padding-bottom: 16px; border-bottom: 1px solid var(--line); }
.wts { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.15fr) minmax(0, 1fr); gap: 12px; }
.wt { min-width: 0; padding: 14px 16px; border-radius: 14px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(32, 60, 61, .04); }
.wt-h { display: flex; align-items: center; gap: 9px; margin: 0 0 10px; color: var(--ink); font: 650 13px/1.3 var(--font); }
.wt-ic { display: grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 9px; background: var(--aqua-3); color: var(--teal-ink); }
.wt-big { display: flex; flex-wrap: wrap; gap: 4px 18px; color: var(--ink-2); font-size: 13.5px; }
.wt-big b { margin-right: 4px; color: var(--ink); font: 650 22px/1.1 var(--font); font-variant-numeric: tabular-nums; }
.wt-wait b { color: var(--warn-ink); }
.wt-l { margin-top: 8px; color: var(--ink-2); font-size: 13px; line-height: 1.45; font-variant-numeric: tabular-nums; }
.wt-l b { color: var(--ink); font-weight: 650; }
.wt-s { margin-top: 6px; color: var(--ink-3); font-size: 12.5px; line-height: 1.45; text-wrap: pretty; }
.wa, .wc { display: grid; gap: 2px; margin: 8px 0 0; padding: 0; list-style: none; }
.wa li { display: grid; grid-template-columns: 16px auto 22px minmax(0, 1fr) auto; align-items: start; gap: 8px; min-height: 30px; color: var(--ink-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.wa :deep(.mark) { margin-top: 8px; }
.wa .wa-n, .wa .wa-c, .wa .wa-s { padding-block: 5px; line-height: 20px; }
.wa .wa-n { color: var(--ink); font-weight: 600; }
.wa .wa-c { color: var(--ink); font-weight: 650; text-align: right; }
.wa .wa-s { min-width: 0; color: var(--ink-3); text-wrap: pretty; }
.wa :deep(.btn) { min-height: 28px; margin-top: 1px; }
.wc li { display: flex; align-items: flex-start; gap: 8px; min-height: 24px; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.wc li svg { flex: none; margin-top: 3px; color: var(--ok); }
@container working (max-width: 1100px) {
  .wts { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
  .wt-chk { grid-column: 1 / -1; }
}
@container working (max-width: 760px) {
  .wts { grid-template-columns: minmax(0, 1fr); }
  .wa li { grid-template-columns: 16px auto 22px minmax(0, 1fr); }
  .wa :deep(.btn) { grid-column: 2 / -1; justify-self: start; min-height: 44px; }
}
</style>
