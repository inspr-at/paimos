<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Stat, StatKey } from '../../lib/releaseStats'
import AppIcon from '../AppIcon.vue'
import StatViz from './StatViz.vue'

// One card that cycles through the release stats (AEON-488). It moves on every
// 7 s, the current dot filling as it waits. Hover or focus inside pauses it; an
// arrow takes over for good; the pause button stops and resumes it. With reduced
// motion it never moves on its own. A screen reader hears a change only when the
// card is not rotating by itself. Arrows show on hover and focus, and always on
// touch screens and phones, at 44 px there.
const props = defineProps<{ stats: Stat[]; compact?: boolean }>()
const ROTATE_MS = 7000

const index = ref(0)
const stat = computed(() => props.stats[Math.min(index.value, props.stats.length - 1)])
const reduce = window.matchMedia('(prefers-reduced-motion: reduce)')
const reducedMotion = ref(reduce.matches)
const stopped = ref(reducedMotion.value)
const paused = computed(() => stopped.value || reducedMotion.value)
const hovered = ref(false)
const focused = ref(false)
const elapsed = ref(0)
const progress = computed(() => paused.value ? 1 : Math.min(1, elapsed.value / ROTATE_MS))
const card = ref<HTMLElement>()
watch(() => props.stats.length, n => { if (index.value >= n) index.value = 0 })

let last = Date.now()
let timer: ReturnType<typeof setInterval> | undefined
function syncReducedMotion() {
  // Reading matches can precede (or consume) Chromium's change detection.
  // Keep the control and live-region state in sync with every live read.
  reducedMotion.value = reduce.matches
  if (reducedMotion.value) { stopped.value = true; elapsed.value = 0 }
}
function tick() {
  syncReducedMotion()
  const now = Date.now(), step = Math.min(250, Math.max(0, now - last))
  last = now
  if (paused.value || hovered.value || focused.value || document.hidden || props.stats.length < 2) return
  elapsed.value += step
  if (elapsed.value >= ROTATE_MS) { index.value = (index.value + 1) % props.stats.length; elapsed.value = 0 }
}
onMounted(() => { reduce.addEventListener('change', syncReducedMotion); syncReducedMotion(); last = Date.now(); timer = setInterval(tick, 100) })
onBeforeUnmount(() => { clearInterval(timer); reduce.removeEventListener('change', syncReducedMotion) })

// An arrow is a person choosing: the card stops moving by itself.
function go(delta: number) {
  const n = props.stats.length
  index.value = (index.value + delta + n) % n
  stopped.value = true
  elapsed.value = 0
}
function toggle() { syncReducedMotion(); stopped.value = reducedMotion.value || !stopped.value; elapsed.value = 0 }
function focusOut(event: FocusEvent) { if (!card.value?.contains(event.relatedTarget as Node | null)) focused.value = false }

// The stat's leading icon, on the 24 grid of the design.
const ICONS: Record<StatKey, string[]> = {
  perweek: ['M4 16a8 8 0 1 1 16 0', 'M12 16l4.5-5'],
  features: ['M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z', 'M19 16l.7 1.8 1.8.7-1.8.7L19 21l-.7-1.8-1.8-.7 1.8-.7z'],
  since: ['M12 3.5a8.5 8.5 0 1 1 0 17 8.5 8.5 0 0 1 0-17z', 'M12 7.5V12l3 2'],
  median: ['M5 5v14M19 5v14', 'M8.5 12h7', 'M10.5 9.5 8.5 12l2 2.5M13.5 9.5l2 2.5-2 2.5'],
  week: ['M6 5h12a2.5 2.5 0 0 1 2.5 2.5V18a2.5 2.5 0 0 1-2.5 2.5H6A2.5 2.5 0 0 1 3.5 18V7.5A2.5 2.5 0 0 1 6 5z', 'M3.5 10h17M8 3v4M16 3v4'],
  streak: ['M10 13.5a4 4 0 0 0 5.7.3l2.8-2.8a4 4 0 0 0-5.7-5.7l-1.2 1.2', 'M14 10.5a4 4 0 0 0-5.7-.3l-2.8 2.8a4 4 0 0 0 5.7 5.7l1.2-1.2'],
  busiest: ['M3 19.5 9.5 8l4 6.5 2.5-3.5 5 8.5z', 'M9.5 8V3.5l3.5 1.5-3.5 1.5'],
}
</script>

<template>
  <section
    ref="card" class="stat-card" :class="{ compact }" aria-roledescription="carousel" aria-label="Release stats"
    @mouseenter="hovered = true" @mouseleave="hovered = false" @focusin="focused = true" @focusout="focusOut"
  >
    <div v-if="stat" class="slide" role="group" aria-roledescription="slide" :aria-label="`${index + 1} of ${stats.length}: ${stat.label}`" :aria-live="paused ? 'polite' : 'off'">
      <!-- The full series measures this shared cell once per width. A shorter
           stat never shrinks the overview and moves the release controls. -->
      <div v-for="sample in stats" :key="`size-${sample.key}`" class="slide-size" aria-hidden="true" inert>
        <p class="size-label"><span class="size-icon" />{{ sample.label }}</p>
        <p class="size-value">{{ sample.value }}</p><p class="size-sub">{{ sample.sub }}</p>
        <div class="size-chips"><span v-for="chip in sample.chips" :key="chip" class="size-stat-chip">{{ chip }}</span></div>
        <StatViz class="size-viz" :viz="sample.viz" />
      </div>
      <div :key="stat.key" class="slide-in">
        <p class="label">
          <span class="icon" aria-hidden="true">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" focusable="false">
              <path v-for="d in ICONS[stat.key]" :key="d" :d="d" />
              <circle v-if="stat.key === 'perweek'" cx="12" cy="16" r="1.4" fill="currentColor" />
            </svg>
          </span>{{ stat.label }}
        </p>
        <p class="value">{{ stat.value }}</p>
        <p class="sub" :data-tip="stat.sub">{{ stat.sub }}</p>
        <div v-if="stat.chips.length" class="chips">
          <span v-for="chip in stat.chips" :key="chip" class="stat-chip">{{ chip }}</span>
        </div>
        <StatViz class="viz" :viz="stat.viz" />
      </div>
    </div>
    <div class="arrows">
      <button type="button" class="nav" aria-label="Previous stat" @click="go(-1)"><AppIcon name="chevron-left" :size="15" /></button>
      <button type="button" class="nav" aria-label="Next stat" @click="go(1)"><AppIcon name="chevron-right" :size="15" /></button>
      <button type="button" class="nav pause" :aria-label="paused ? 'Resume automatic rotation' : 'Pause automatic rotation'" @click="toggle">
        <AppIcon :name="paused ? 'play' : 'pause'" :size="12" />
      </button>
    </div>
    <div class="foot">
      <span class="dots" aria-hidden="true">
        <span v-for="(s, i) in stats" :key="s.key" class="dot" :class="{ on: i === index }"><span class="fill" :style="{ width: i === index ? `${progress * 100}%` : '0%' }" /></span>
      </span>
      <span class="pos">{{ index + 1 }} / {{ stats.length }}</span>
    </div>
  </section>
</template>

<style scoped>
.stat-card {
  position: relative; display: flex; flex-direction: column; min-width: 0; padding: 20px 22px 12px; border-radius: 20px;
  background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3);
  box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -20px var(--card-glow, color-mix(in srgb, var(--primary-line) 35%, transparent));
}
.slide { display:grid; min-width:0; }
.slide-size { visibility:hidden; pointer-events:none; }
.slide-size,.slide-in { grid-area:1/1; }
.slide-in,.slide-size { display:flex; flex-direction:column; gap:6px; min-width:0; }
.label,.size-label { display: flex; align-items: center; gap: 8px; margin: 0; padding-right: 108px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--ink-3); }
.icon,.size-icon { display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; width: 26px; height: 26px; border-radius: 8px; background: color-mix(in srgb, var(--teal) 10%, transparent); color: var(--teal-ink); }
.value,.size-value { margin: 8px 0 0; font: 650 42px/1.05 var(--font); letter-spacing: -.03em; font-variant-numeric: tabular-nums; color: var(--ink); }
.sub,.size-sub { margin: 0; font-size: 14px; line-height: 1.45; color: var(--ink-2); white-space: normal; overflow-wrap: anywhere; }
.chips,.size-chips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 4px; }
.stat-chip,.size-stat-chip { display: inline-flex; align-items: center; min-height: 22px; padding: 0 8px; border-radius: 999px; background: color-mix(in srgb, var(--teal) 5%, transparent); color: var(--teal-ink); font: 600 11px/1.3 var(--font); white-space: nowrap; }
.viz,.size-viz { margin-top: 12px; }
.arrows { position: absolute; top: 12px; right: 12px; display: flex; align-items: center; gap: 4px; }
.nav { display: flex; align-items: center; justify-content: center; flex-shrink: 0; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 10px; background: transparent; color: var(--ink-2); cursor: pointer; }
.nav:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .nav:hover { background: var(--row-hover); color: var(--teal-ink); } }
/* Arrows wait for hover or focus where there is a pointer; touch always shows them, at 44 px. */
@media (hover: hover) and (pointer: fine) {
  .stat-card:not(.compact) .arrows .nav:not(.pause) { opacity: 0; transition: opacity .16s ease; }
  .stat-card:not(.compact):hover .arrows .nav, .stat-card:not(.compact):focus-within .arrows .nav { opacity: 1; }
}
.foot { display: flex; align-items: center; gap: 6px; margin-top: auto; padding-top: 14px; }
.dots { display: flex; align-items: center; gap: 6px; }
.dot { position: relative; display: block; overflow: hidden; width: 6px; height: 6px; border-radius: 999px; background: var(--line-2); transition: width .25s ease; }
.dot.on { width: 22px; background: color-mix(in srgb, var(--teal) 18%, transparent); }
.fill { display: block; height: 100%; border-radius: 999px; background: var(--teal); transition: width .1s linear; }
.pos { margin-left: auto; font: 500 11px/1 var(--mono); font-variant-numeric: tabular-nums; color: var(--ink-3); }
.pause { border-radius:8px; }
@media (hover: none), (pointer: coarse) { .nav { width: 44px; height: 44px; } }
/* Phones: the arrows sit in the label row, always there, 44 px. */
.compact { padding: 16px 10px 8px 16px; }
.compact .label,.compact .size-label { padding-right: 136px; }
.compact .arrows { top: 4px; right: 4px; gap: 0; }
.compact .nav { width: 44px; height: 44px; }
.compact .value,.compact .size-value { font-size: 34px; }
.compact .sub,.compact .size-sub { white-space: normal; }
/* Short screens keep the list in view: a tighter card. */
@media (max-height: 800px) and (min-width: 761px) {
  .stat-card { padding: 16px 20px 10px; }
  .value,.size-value { margin-top: 4px; font-size: 34px; }
  .viz,.size-viz { margin-top: 8px; }
  .foot { padding-top: 8px; }
}
@media (prefers-reduced-motion: no-preference) {
  .slide-in { animation: stat-in .26s cubic-bezier(.2, .75, .25, 1) both; }
}
@media (prefers-reduced-motion: reduce) { .dot, .fill, .nav { transition: none; } }
@keyframes stat-in { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }
</style>
