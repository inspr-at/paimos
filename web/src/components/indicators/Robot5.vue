<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { AgentState } from '../../lib/agentSignals'
import AgentStateMark from './AgentStateMark.vue'
import RobotExpression from './parts/RobotExpression.vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

// Preserve LA1's artwork and working motion; SC1 supplies state colours. LiveBot encodes the
// original principal id and stack index in seed as `${id}:${index}`.
const props = withDefaults(defineProps<{
  state: AgentState; size?: number; pulse: number; seed: string; lead: boolean
}>(), { size: 26 })
const identity = computed(() => {
  const split = props.seed.lastIndexOf(':')
  const indexed = split >= 0 && /^\d+$/.test(props.seed.slice(split + 1))
  return { id: indexed ? props.seed.slice(0, split) : props.seed, index: indexed ? Number(props.seed.slice(split + 1)) : 0 }
})
// The robot is drawn a little larger than its disk, so its lit antenna peeks over the rim.
const art = computed(() => Math.round(props.size * 1.1))
const style = computed(() => ({
  '--size': `${props.size}px`,
  // Head centred a touch below the disk's centre (the head's centre is 58% down the art).
  '--art-top': `${(props.size / 2 + props.size * .06 - art.value * .579).toFixed(1)}px`,
  // Negative delays start each bot part-way through its loops.
  '--lag': `${-(identity.value.index * 0.53 + (identity.value.id ? (parseInt(identity.value.id.slice(0, 2), 16) || 0) % 7 : 0) * 0.31).toFixed(2)}s`,
}))
const glint = ref(0)
let serial = 0
let lastPulse = props.pulse
let clear: ReturnType<typeof setTimeout> | undefined
// Watching without immediate prevents a replay when an existing agent mounts.
watch(() => props.pulse, value => {
  if (!Number.isFinite(value) || value <= lastPulse) return
  lastPulse = value
  if (props.state !== 'working') return
  clearTimeout(clear)
  glint.value = ++serial
  clear = setTimeout(() => { glint.value = 0 }, 600)
})
watch(() => props.state, state => {
  if (state !== 'working') { clearTimeout(clear); glint.value = 0 }
})
onBeforeUnmount(() => clearTimeout(clear))
</script>

<template>
  <span class="agent-indicator-art indicator robot5" :class="[state, { lead }]" :style="style" aria-hidden="true">
    <span v-if="lead && state === 'working'" class="halo" />
    <span class="disk" />
    <svg class="bot" viewBox="0 0 24 24" :width="art" :height="art" focusable="false">
      <g class="bob">
        <path class="stalk" d="M12 7.6V3.9" />
        <circle class="tip-glow" cx="12" cy="2.5" r="1.7" />
        <circle class="tip" cx="12" cy="2.5" r="1.65" />
        <rect class="ear" x="2.3" y="11.7" width="2.3" height="4.8" rx="1.15" />
        <rect class="ear" x="19.4" y="11.7" width="2.3" height="4.8" rx="1.15" />
        <rect class="head" x="4.4" y="7.6" width="15.2" height="12.6" rx="4.8" />
        <g v-if="state === 'working'" class="look">
          <g class="blink">
            <rect class="eye" x="8.2" y="11.9" width="2.4" height="3.3" rx="1.2" />
            <rect class="eye" x="13.4" y="11.9" width="2.4" height="3.3" rx="1.2" />
          </g>
        </g>
        <path v-if="state === 'working'" class="happy" d="M8.1 14.2q1.3-1.9 2.6 0M13.3 14.2q1.3-1.9 2.6 0" />
        <circle v-if="state === 'working'" class="cheek" cx="7.3" cy="16.6" r="1.05" />
        <circle v-if="state === 'working'" class="cheek" cx="16.7" cy="16.6" r="1.05" />
        <RobotExpression v-if="state !== 'working'" :state="state" :cx="12" :eye-y="13.5" :mouth-y="17.1" :spread="2.6" />
        <path v-if="state === 'working'" class="smile" d="M10.5 17.1q1.5 1 3 0" />
      </g>
    </svg>
    <AgentStateMark class="state-mark" :state="state" :size="12" />
    <svg v-if="glint && state === 'working'" class="event-mark" viewBox="0 0 32 32" focusable="false">
      <path :key="glint" class="glint" d="m26 1 1.5 3.5L31 6l-3.5 1.5L26 11l-1.5-3.5L21 6l3.5-1.5Z" />
    </svg>
    <template v-if="lead && state === 'working'">
      <svg class="spark s1" viewBox="0 0 10 10" focusable="false"><path d="M5 0l1.2 3.8L10 5 6.2 6.2 5 10 3.8 6.2 0 5l3.8-1.2z" /></svg>
      <svg class="spark s2" viewBox="0 0 10 10" focusable="false"><path d="M5 0l1.2 3.8L10 5 6.2 6.2 5 10 3.8 6.2 0 5l3.8-1.2z" /></svg>
    </template>
  </span>
</template>

<style scoped>
.robot5 {
  --face: color-mix(in srgb, var(--glow) 9%, var(--surface-raised)); --rim: var(--glow); --eye: var(--ink);
  --disk: color-mix(in srgb, var(--glow) 12%, var(--surface-raised));
  --glow: var(--signal); --spark: #d69b31; --blush: oklch(.8 .09 20 / .55);
  position: relative; display: inline-grid; place-items: center; flex-shrink: 0; width: var(--size); height: var(--size);
}
.disk {
  position: absolute; inset: 0; border-radius: 50%;
  background: var(--disk); box-shadow: 0 0 0 1.5px var(--surface-raised), inset 0 0 0 1px var(--glow), 0 2px 6px -2px color-mix(in srgb, var(--glow) 25%, transparent);
}
.bot { position: absolute; top: var(--art-top); left: 50%; translate: -50% 0; display: block; overflow: visible; }
.stalk { fill: none; stroke: var(--rim); stroke-width: 1.5; stroke-linecap: round; }
.tip { fill: var(--glow); }
.tip-glow { fill: var(--glow); opacity: 0; }
.ear { fill: var(--rim); opacity: .75; }
.head { fill: var(--face); stroke: var(--rim); stroke-width: 1.5; }
.eye { fill: var(--eye); }
.cheek { fill: var(--blush); }
.smile { fill: none; stroke: var(--eye); stroke-width: 1.2; stroke-linecap: round; }
.happy { fill: none; stroke: var(--eye); stroke-width: 1.35; stroke-linecap: round; opacity: 0; }
.halo {
  position: absolute; inset: -7px; border-radius: 50%; pointer-events: none;
  background: radial-gradient(circle, color-mix(in oklab, var(--glow) 50%, transparent) 36%, transparent 70%); opacity: .6;
}
.spark { position: absolute; z-index: 1; width: 7px; height: 7px; fill: var(--spark); opacity: 0; pointer-events: none; }
.s1 { top: -6px; right: -2px; }
.s2 { top: -1px; left: -6px; width: 5px; height: 5px; }

.robot5.stale { --face: var(--surface-raised); --rim: var(--ink-3); --eye: var(--ink-3); --disk: var(--surface-sunken); --glow: var(--signal); --blush: transparent; }
.robot5.stale { filter: grayscale(1); }
.robot5.stale * { animation: none !important; }
.state-mark { position: absolute; top: -3px; right: -3px; }

@media (prefers-reduced-motion: no-preference) {
  :global(.live-bot.hovering .robot5.working .bob) { animation: bot-bob 1.9s ease-in-out infinite; animation-delay: var(--lag); }
  .working .blink { transform-box: fill-box; transform-origin: center; animation: bot-blink 4.6s linear infinite; animation-delay: var(--lag); }
  .working .look { animation: bot-look 7.4s ease-in-out infinite; animation-delay: var(--lag); }
  .working .tip { animation: bot-tip 1.9s ease-in-out infinite; animation-delay: var(--lag); }
  .working .tip-glow { transform-box: fill-box; transform-origin: center; animation: bot-ping 1.9s cubic-bezier(.2, .7, .2, 1) infinite; animation-delay: var(--lag); }
  .working .halo { animation: bot-breathe 3.2s ease-in-out infinite; animation-delay: var(--lag); }
  .working .spark { animation: bot-spark 3.2s ease-out infinite; animation-delay: var(--lag); }
  .working .s2 { animation-delay: calc(var(--lag) - 1.6s); }
  :global(.live-bot.hovering .robot5.working:not(.lead) .bob) { animation-duration: 2.3s; }
  .eye, .happy { transition: opacity .12s ease; }
}
.event-mark { position: absolute; inset: 0; width: 100%; height: 100%; overflow: visible; pointer-events: none; }
.glint { fill: #c9a24a; stroke: var(--surface-raised); stroke-width: .65; transform-origin: 26px 6px; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) { .glint { animation-name: event-glint; } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 25%, 50% { opacity: 1; } }
@keyframes event-glint { 0% { opacity: 0; transform: scale(.65); } 25% { opacity: 1; transform: scale(1); } 100% { opacity: 0; transform: scale(.85); } }
@keyframes bot-bob { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-1.1px); } }
@keyframes bot-blink { 0%, 90%, 100% { transform: scaleY(1); } 93% { transform: scaleY(.12); } 96% { transform: scaleY(1); } }
@keyframes bot-look { 0%, 34%, 100% { transform: translateX(0); } 40%, 56% { transform: translateX(.8px); } 62%, 80% { transform: translateX(-.6px); } 86% { transform: translateX(0); } }
@keyframes bot-tip { 0%, 100% { opacity: .78; } 50% { opacity: 1; } }
@keyframes bot-ping { 0% { transform: scale(1); opacity: .55; } 70%, 100% { transform: scale(2.6); opacity: 0; } }
@keyframes bot-breathe { 0%, 100% { transform: scale(.84); opacity: .4; } 50% { transform: scale(1.1); opacity: .95; } }
@keyframes bot-spark { 0% { transform: translateY(2px) scale(.2) rotate(0deg); opacity: 0; } 12% { opacity: 1; transform: translateY(0) scale(1) rotate(20deg); } 30% { opacity: 0; transform: translateY(-3px) scale(.5) rotate(60deg); } 100% { opacity: 0; } }
</style>

<style>
/* Pointed at, the robots smile back: their eyes turn into happy arcs. */
.live-chip:hover .robot5.working .eye, .live-chip:focus-visible .robot5.working .eye, .robot5.working:hover .eye { opacity: 0; }
.live-chip:hover .robot5.working .happy, .live-chip:focus-visible .robot5.working .happy, .robot5.working:hover .happy { opacity: 1; }
/* Paused while off screen (the chip sets .asleep). */
.asleep .robot5, .asleep .robot5 * { animation-play-state: paused !important; }
</style>
