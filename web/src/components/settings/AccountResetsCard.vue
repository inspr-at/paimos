<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1037: Settings › Accounts › one account · the Resets card (AEON-1030 draft 6).
// Shown only while the vendor reports spendable resets; the parent owns the write.
import { computed } from 'vue'
import { resetPlanLine, resetsSummary, type ResetCredits, type ResetPlan, type ResetPolicy } from '../../lib/accountResets'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
const props = defineProps<{ accountId: string; harness: string; provider?: string; credits: ResetCredits; policy: ResetPolicy; plan: ResetPlan | null; now: number; canSet: boolean; busy: boolean }>()
const emit = defineEmits<{ set: [policy: ResetPolicy] }>()
const on = computed(() => props.policy === 'auto_before_expiry')
const summary = computed(() => resetsSummary(props.credits, props.now))
const line = computed(() => resetPlanLine(props.policy, props.plan, props.now))
function change(event: Event) {
  const input = event.target as HTMLInputElement, wanted = input.checked
  // The switch shows the saved policy until the parent confirms the write.
  input.checked = on.value
  if (props.canSet && !props.busy) emit('set', wanted ? 'auto_before_expiry' : 'suggest')
}
</script>
<template>
  <div class="rs-card" data-account-resets :aria-busy="busy">
    <div class="rs-head">
      <span class="rs-mark"><HarnessMark :harness="harness" :provider="provider" :size="15" /></span>
      <div class="rs-titles"><p class="rs-t">Resets</p><p class="rs-s" data-reset-summary>{{ summary }}</p></div>
      <label class="switch rs-sw"><input type="checkbox" role="switch" :checked="on" :disabled="!canSet || busy" :aria-describedby="`rs-hint-${accountId}`" @change="change" /><span>Don’t let resets expire</span></label>
    </div>
    <p :id="`rs-hint-${accountId}`" class="rs-hint">If a reset would go unused, PAIMOS uses it in time and speeds up the pace so it counts.</p>
    <p class="rs-stats" data-reset-plan><AppIcon name="gauge" :size="13" /><span><b v-if="line.lead">{{ line.lead }}</b>{{ line.rest }}</span></p>
    <p v-if="!canSet" class="rs-who">Only the account owner can change this.</p>
  </div>
</template>
<style scoped>
.rs-card { container-type: inline-size; padding: 14px 16px; border-radius: 14px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.rs-head { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 10px; }
.rs-mark { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 9px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); }
.rs-t { color: var(--ink); font-size: 13.5px; font-weight: 650; }
.rs-s { color: var(--ink-3); font-size: 12.5px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.rs-sw { min-height: 44px; font-size: 12.5px; color: var(--ink); }
/* Phone sheets give every input 44 px; the switch keeps its track, the label carries the touch target. */
.rs-card .rs-sw input[type="checkbox"] { min-height: 0; height: 20px; }
.rs-sw input:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
.rs-sw input:disabled { cursor: default; opacity: .55; }
.rs-hint { margin-top: 8px; color: var(--ink-2); font-size: 12.5px; line-height: 1.45; }
.rs-stats { display: flex; align-items: flex-start; gap: 6px; margin-top: 6px; color: var(--ink-3); font-size: 12px; line-height: 1.45; }
.rs-stats svg { flex: none; margin-top: 2px; }
.rs-stats b { color: var(--ink); font-weight: 600; }
.rs-who { margin-top: 6px; color: var(--ink-3); font-size: 12px; }
@container (max-width: 420px) {
  .rs-head { grid-template-columns: auto minmax(0, 1fr); }
  .rs-sw { grid-column: 1 / -1; justify-content: flex-start; white-space: normal; }
}
</style>
