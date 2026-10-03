<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { SERVICE_TIERS, offeredTiers, type ServiceTier, type TierReport } from '../../lib/serviceTier'
const props = withDefaults(defineProps<{ active: ServiceTier | null; report?: TierReport; pending?: ServiceTier | null; scale?: number; faint?: boolean; count?: number }>(), { scale: 1 })
const offered = computed(() => offeredTiers(props.report).map(t => t.tier))
const count = computed(() => props.count ?? offered.value.length)
const index = (tier: ServiceTier | null | undefined) => tier ? (props.count ? SERVICE_TIERS.indexOf(tier) : offered.value.indexOf(tier)) : -1
const level = computed(() => index(props.active)), pending = computed(() => index(props.pending))
const width = computed(() => 7 * (count.value - 1) + 6.5)
</script>
<template>
  <svg v-if="count" class="tier-glyph" :class="{ paid: active && active !== 'default', faint }" :width="width * .7 * scale" :height="8.4 * scale" :viewBox="`0 0 ${width} 12`" aria-hidden="true" focusable="false">
    <path v-for="i in count" :key="i" :transform="`translate(${(i - 1) * 7} 0)`" :class="{ on: i - 1 <= level, pending: pending > level && i - 1 > level && i - 1 <= pending, leaving: pending >= 0 && pending < level && i - 1 > pending && i - 1 <= level }" d="M.25 .25H2.45L6.25 6 2.45 11.75H.25L4.05 6Z" />
  </svg>
  <span v-else class="tier-unknown" aria-hidden="true">—</span>
</template>
<style scoped>
.tier-glyph{display:block;flex:none;overflow:visible}.tier-glyph path{fill:color-mix(in srgb,var(--ink) 17%,transparent)}.tier-glyph .on{fill:var(--teal)}.tier-glyph.paid .on{fill:var(--warn-ink)}.tier-glyph.faint path{fill:color-mix(in srgb,var(--ink) 8%,transparent)}.tier-glyph .pending{fill:color-mix(in srgb,var(--warn-ink) 45%,transparent)}.tier-glyph .leaving{opacity:.3}.tier-unknown{font-size:11px;color:var(--ink-3)}
@media(prefers-reduced-motion:no-preference){.tier-glyph .pending{animation:tier-pending 1.1s ease-in-out infinite alternate}@keyframes tier-pending{to{opacity:.35}}}
</style>
