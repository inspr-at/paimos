<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Numbers · Simple (AEON-994 draft 5, package 3; the default level): one summary
// card, then the ten numbers in three plain sections along the path, then the
// technical words explained simply. Everything sits below the page head's
// controls and grows downward.
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import SimpleTile from './SimpleTile.vue'
import type { DeliveryLanguage } from '../../lib/delivery'
import type { DeliveryMetrics, WindowDays } from '../../lib/deliveryNumbers'
import { simpleNumbersOf } from '../../lib/deliverySimple'
import { simpleText } from '../../lib/deliverySimpleText'

const props = defineProps<{ data: DeliveryMetrics | null; window: WindowDays; state: 'loading' | 'error' | 'ready'; noData: boolean; lang: DeliveryLanguage }>()
const text = computed(() => simpleText(props.lang))
const page = computed(() => simpleNumbersOf(props.state === 'ready' ? props.data : null, props.window, props.state, props.noData, props.lang))
const LEVELS = ['on', 'close', 'far', 'none'] as const
const ICON = { on: 'v-on', close: 'v-close', far: 'v-far', none: 'v-none' } as const
</script>

<template>
  <div class="simple" data-testid="delivery-simple">
    <div v-if="page.summary.kind === 'loading'" class="s-sum" aria-busy="true">
      <span class="sk" style="width: 60%; height: 22px" /><span class="sk" style="width: 80%; height: 14px" /><span class="sk" style="width: 70%; height: 14px" />
    </div>
    <div v-else-if="page.summary.kind === 'nodata'" class="s-sum" data-testid="delivery-summary">
      <p class="big">{{ page.summary.title }}</p>
      <p class="line">{{ page.summary.body }}</p>
    </div>
    <div v-else-if="page.summary.kind === 'ready'" class="s-sum" data-testid="delivery-summary">
      <p class="big">{{ page.summary.big }}</p>
      <p v-if="page.summary.gaps.length" class="line">
        <template v-for="(gap, index) in page.summary.gaps" :key="gap.label">{{ index ? ' ' : '' }}{{ gap.label }}: <b>{{ gap.name }}</b> ({{ gap.gap }}).</template>
      </p>
      <p class="line">{{ page.summary.week }}</p>
      <p class="line">{{ page.summary.charts }}</p>
      <div class="s-legend" role="list" :aria-label="text.legend">
        <span v-for="level in LEVELS" :key="level" role="listitem" :class="`v-${level}`"><AppIcon :name="ICON[level]" :size="14" />{{ level === 'none' ? text.none : text[level] }}</span>
      </div>
    </div>

    <section v-for="(section, index) in page.sections" :key="index" class="s-sec" :aria-labelledby="`s-sec-${index}`">
      <div class="s-sec-head">
        <h3 :id="`s-sec-${index}`">{{ section.title }}</h3>
        <span v-if="section.count" class="cnt">{{ section.count }}</span>
        <p>{{ section.sub }}</p>
      </div>
      <div class="s-grid" role="list">
        <SimpleTile v-for="tile in section.tiles" :key="tile.key" :tile="tile" :state="state" :text="text" />
      </div>
    </section>

    <div class="defs">
      <h3>{{ text.defsTitle }}</h3>
      <p v-for="[term, meaning] in text.defs" :key="term"><b>{{ term }}.</b> {{ meaning }}</p>
    </div>
  </div>
</template>

<style scoped>
.simple { margin-top: 14px; }
.s-sum { display: grid; gap: 6px; padding: 16px 18px 14px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.s-sum p { margin: 0; }
.s-sum .big { font: 650 18px/1.35 var(--font); letter-spacing: -.01em; color: var(--ink); }
.s-sum .line { font-size: 13.5px; line-height: 1.5; color: var(--ink-2); }
.s-sum .line b { color: var(--ink); font-weight: 600; }
.s-legend { display: flex; flex-wrap: wrap; gap: 6px 18px; margin-top: 4px; font-size: 12px; color: var(--ink-2); }
.s-legend span { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.v-on svg { color: var(--ok); }
.v-close svg { color: var(--gold); }
.v-far svg { color: var(--danger); }
.v-none svg { color: var(--ink-3); }
.s-sec { margin-top: 24px; }
.s-sec-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 12px; }
.s-sec-head h3 { margin: 0; font: 650 15px/1.3 var(--font); color: var(--ink); }
.s-sec-head .cnt { font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.s-sec-head p { flex-basis: 100%; margin: 0; font-size: 12.5px; color: var(--ink-2); }
.s-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-top: 10px; }
.defs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 28px; margin-top: 22px; padding-top: 14px; border-top: 1px solid var(--line); }
.defs h3 { grid-column: 1 / -1; margin: 0; font: 650 13.5px/1.3 var(--font); color: var(--ink); }
.defs p { margin: 0; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.defs b { font-weight: 600; color: var(--ink); }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@container delivery (max-width: 1100px) { .s-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@container delivery (max-width: 640px) {
  .s-grid { grid-template-columns: minmax(0, 1fr); gap: 8px; }
  .defs { grid-template-columns: minmax(0, 1fr); }
}
</style>
