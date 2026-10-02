<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { adjacentTier, TIER_NAME, tierOptions, tierPrice } from '../../lib/serviceTier'
import { useServiceTiers } from '../../stores/serviceTiers'
import AppIcon from '../AppIcon.vue'
import TierGlyph from './TierGlyph.vue'
const props = defineProps<{ session: HarnessSession; name: string; phone?: boolean }>()
const tiers = useServiceTiers()
const state = computed(() => tiers.state(props.session)), report = computed(() => tiers.report(props.session))
const canOpen = computed(() => (!tiers.unavailable(props.session) || tiers.canAsk(props.session)) && !state.value.pending && !tiers.busy[props.session.id])
const active = computed(() => tierOptions(report.value).find(t => t.tier === state.value.active_tier))
const tip = computed(() => {
  const s = props.session, current = state.value.active_tier
  const first = `Service tier: ${current ? TIER_NAME[current] : 'not reported'} · ${tierPrice(active.value)}`
  if (s.stopped_at || s.archived_at || s.phase === 'stopped') return `${first}\nThis session ran at ${current ? TIER_NAME[current] : 'an unreported tier'}.`
  if (s.management_mode === 'unmanaged') return `${first}\nReported by ${report.value?.harness_version || s.harness} — ${report.value?.change_instructions || 'change it in its terminal'}.`
  if (state.value.pending) return `${first}\nSwitching to ${TIER_NAME[state.value.pending.value]} · waiting for the daemon.`
  return `${first}\n${canOpen.value ? (tiers.canAsk(s) ? 'Ask for another tier' : 'Change… · ← → one step') : tiers.unavailable(s)}`
})
const neighbour = (direction: -1 | 1) => adjacentTier(state.value.active_tier, report.value, direction)
function open(event: MouseEvent) { tiers.open(props.session, props.name, event.currentTarget as HTMLElement) }
async function step(direction: -1 | 1) {
  const to = neighbour(direction)
  if (to && canOpen.value && !tiers.canAsk(props.session)) await tiers.change(props.session, props.name, to.tier, { withUndo: true })
}
function keys(event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || !['ArrowLeft', 'ArrowRight'].includes(event.key)) return
  event.preventDefault(); event.stopPropagation(); void step(event.key === 'ArrowLeft' ? -1 : 1)
}
</script>
<template>
  <span class="tier-cell" :class="{ phone }" @click.stop>
    <button v-for="direction in [-1, 1] as const" :key="direction" type="button" class="tier-step" :class="direction < 0 ? 'down' : 'up'" tabindex="-1" :disabled="!canOpen || tiers.canAsk(session) || !neighbour(direction)" :aria-label="`${direction < 0 ? 'One tier down' : 'One tier up'}: ${neighbour(direction)?.name || 'not available'} · ${tierPrice(neighbour(direction))}`" :data-tip="`${neighbour(direction)?.name || ''} · ${tierPrice(neighbour(direction))}`" @click="step(direction)"><AppIcon :name="direction < 0 ? 'minus' : 'plus'" :size="12" /></button>
    <!-- Preserve the actual focus node while a request is pending. aria-disabled
         blocks activation without dropping focus or changing its box. -->
    <component :is="canOpen || state.pending || tiers.busy[session.id] ? 'button' : 'span'" class="tier-mark" :type="canOpen || state.pending ? 'button' : undefined" :role="canOpen || state.pending ? undefined : 'img'" :aria-label="tip" :aria-disabled="!canOpen || undefined" :aria-haspopup="canOpen ? 'dialog' : undefined" :aria-expanded="tiers.dialog?.anchor?.dataset.tier === session.id || undefined" :data-tier="session.id" :data-tip="tip" @click="canOpen && open($event)" @keydown="keys">
      <TierGlyph :active="state.active_tier" :report="report" :pending="state.pending?.value" />
      <span v-if="phone && state.active_tier && state.active_tier !== 'default'" class="price">{{ active?.price_multiplier ? `×${active.price_multiplier}` : 'price unknown' }}</span>
    </component>
  </span>
</template>
<style scoped>
.tier-cell{display:grid;grid-template-columns:22px 28px 22px;gap:4px;align-items:center;justify-items:center;flex:none}.tier-mark{grid-column:2;grid-row:1;display:inline-flex;align-items:center;justify-content:center;width:28px;height:26px;padding:0;border:0;border-radius:7px;background:transparent;color:inherit;cursor:default}button.tier-mark:not([aria-disabled=true]){cursor:pointer}button.tier-mark:not([aria-disabled=true]):hover{background:var(--row-hover)}.tier-mark:focus-visible,.tier-step:focus-visible{box-shadow:var(--focus-ring)}.tier-step{grid-row:1;display:grid;place-items:center;width:22px;height:22px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--ink-3);opacity:0;cursor:pointer}.down{grid-column:1}.up{grid-column:3}.tier-step:disabled{visibility:hidden}.tier-cell:hover .tier-step,.tier-cell:focus-within .tier-step{opacity:1}.tier-step:hover{color:var(--teal-ink);background:var(--row-selected)}.phone{display:inline-flex}.phone .tier-step{display:none}.phone .tier-mark{width:auto;gap:5px;min-width:28px}.price{font-size:11px;color:var(--ink-3);white-space:nowrap}
@media(hover:none){.tier-step{visibility:hidden}}@media(prefers-reduced-motion:no-preference){.tier-step{transition:opacity .12s ease}}
</style>
