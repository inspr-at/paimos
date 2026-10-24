<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// WorstPanel's shared frame and ticket links, with two neutral outcome lists.
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import { vClipTip } from '../../directives/clipTip'
import type { DeliveryLanguage } from '../../lib/delivery'
import { num } from '../../lib/deliveryNumbers'
import { reviewCounts, reviewRowCounts, reviewText, type ReviewRoundNumbers } from '../../lib/deliveryReviewRounds'
import { reviewRuleText } from '../../lib/reviewRuleText'
import { ticketPath } from '../../lib/work'

const props = defineProps<{ data: ReviewRoundNumbers; anchor: HTMLElement; routeKey: string; lang: DeliveryLanguage }>()
const emit = defineEmits<{ close: [restore: boolean] }>()
const t = computed(() => reviewText(props.lang))
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="640" :tallest="600" :label="t.label" cycle phone-sheet fill-phone :sheet-below="520" below-header class="review-rounds-panel" data-testid="review-rounds-panel" @close="restore => emit('close', restore)">
    <header class="rr-head"><h3>{{ t.label }}</h3><p>{{ reviewCounts(data.current, lang) }}</p></header>
    <!-- The scrolling body owns its touch gestures; header swipes still use the shared sheet dismissal. -->
    <div class="rr-lists" @touchstart.stop @touchend.stop @touchcancel.stop>
      <section :aria-label="t.most">
        <h4>{{ t.most }}</h4>
        <ul class="rr-tickets">
          <li v-for="row in data.most_rounds" :key="row.ticket_node_id">
            <TicketPeekLink class="rr-ticket-link" :ticket-key="row.key" :href="ticketPath(routeKey, row.key)" @click="emit('close', false)">
              <span class="rr-key">{{ row.key }}<AppIcon name="external" :size="12" /></span>
              <span v-clip-tip class="rr-title">{{ row.title }}</span>
              <span class="rr-counts">{{ reviewRowCounts(row, lang) }}</span>
            </TicketPeekLink>
            <div v-if="row.rules.length" class="rr-chips"><span v-for="rule in row.rules" :key="rule.rule" class="rr-chip" tabindex="0" :data-tip="reviewRuleText(rule.rule, lang)">{{ rule.rule }}</span></div>
          </li>
        </ul>
        <p v-if="!data.most_rounds.length" class="rr-empty">{{ t.emptyTickets }}</p>
      </section>
      <section :aria-label="t.rules">
        <h4>{{ t.rules }}</h4>
        <ul class="rr-rules">
          <li v-for="rule in data.rules_cited_most" :key="rule.rule" class="rr-rule-row">
            <span class="rr-chip" tabindex="0" :data-tip="reviewRuleText(rule.rule, lang)">{{ rule.rule }}</span>
            <span class="rr-rule-count">{{ num(rule.count, lang) }}</span>
            <span v-if="rule.count > rule.previous_count" class="rr-rising">{{ t.rising }}</span>
          </li>
        </ul>
        <p v-if="!data.rules_cited_most.length" class="rr-empty">{{ t.emptyRules }}</p>
      </section>
    </div>
    <footer class="rr-actions"><button type="button" class="btn" data-autofocus @click="emit('close', true)">{{ t.close }}<KeyCap k="Esc" /></button></footer>
  </FloatingPanel>
</template>

<style scoped>
.rr-head { flex: none; padding: 12px 16px; border-bottom: 1px solid var(--line); }
.rr-head h3 { margin: 0; font: 650 16px/1.4 var(--font); color: var(--ink); }
.rr-head p { margin: 3px 0 0; font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.rr-lists { flex: 1; display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr); align-content: start; gap: 18px; padding: 14px 16px; overflow-y: auto; min-height: 0; }
.rr-lists section { min-width: 0; }
h4 { margin: 0 0 8px; font: 650 13px/1.5 var(--font); color: var(--ink); }
ul { list-style: none; margin: 0; padding: 0; }
.rr-tickets > li + li { border-top: 1px solid var(--line); }
.rr-ticket-link { display: grid; grid-template-columns: minmax(0, 1fr); gap: 2px; min-height: 44px; padding: 8px 4px; text-decoration: none; border-radius: var(--radius-row); color: var(--ink); }
.rr-ticket-link:hover { background: var(--row-hover); }
.rr-ticket-link:focus-visible, .rr-chip:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; }
.rr-key { display: inline-flex; gap: 5px; align-items: center; font: 600 12px/1.5 var(--mono); color: var(--teal-ink); }
.rr-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; line-height: 1.5; }
.rr-counts { min-width: 13ch; font: 500 12px/1.5 var(--font); font-variant-numeric: tabular-nums; color: var(--ink-2); }
.rr-chips { display: flex; flex-wrap: wrap; gap: 4px; padding: 0 4px 8px; }
.rr-chip { display: inline-flex; align-items: center; justify-content: center; min-width: 3ch; min-height: 24px; padding: 0 6px; border: 1px solid var(--line-2); border-radius: var(--radius-row); background: transparent; color: var(--ink-2); font: 550 12px/1.5 var(--mono); white-space: nowrap; }
.rr-rule-row { display: flex; align-items: center; gap: 8px; min-height: 44px; border-bottom: 1px solid var(--line); }
.rr-rule-count { min-width: 5ch; font: 600 12.5px/1.5 var(--mono); font-variant-numeric: tabular-nums; color: var(--ink); }
.rr-rising { font-size: 12px; color: var(--ink-2); }
.rr-empty { margin: 0; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.rr-actions { flex: none; display: flex; justify-content: flex-end; padding: 10px 16px; border-top: 1px solid var(--line); }
.rr-actions .btn { gap: 8px; min-height: 44px; }
@media (max-width: 520px) {
  .rr-head { padding-top: max(16px, env(safe-area-inset-top)); }
  .rr-lists { grid-template-columns: minmax(0, 1fr); }
  .rr-actions { padding-bottom: max(12px, env(safe-area-inset-bottom)); }
}
</style>
<style>
.floating.review-rounds-panel { display: flex; flex-direction: column; padding: 0; overflow: hidden; }
.floating.review-rounds-panel.bottom { top: 0; bottom: 0; height: auto !important; max-height: 100dvh !important; border-radius: 0; }
</style>
