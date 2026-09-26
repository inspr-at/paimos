<!-- Frozen visual reference from aee24ca0f92a938ba91e0f8de171070e59d001ef:PlayfulBot.vue. Keep artwork unchanged. -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { LiveBotState } from '../../src/lib/liveAgents'
import { avatarColor, type AvatarColor } from '../../src/lib/avatar'

// LA1 artwork and animation restored from 28d2425 (AEON-184). LiveBot owns
// the viewer preference; this renderer blinks, glances along its screen,
// its antenna glows and, when it leads, a soft halo breathes behind it and a
// spark or two twinkles. Every motion is a CSS transform or opacity, so the
// compositor carries it; `index` staggers several bots so they never move in
// lockstep; reduced motion leaves a still bot with its antenna lit.
// Each agent keeps its avatar hue (the server's palette rule) as its face tint.
const props = withDefaults(defineProps<{ id?: string; size?: number; index?: number; lead?: boolean; state?: LiveBotState; hovering?: boolean }>(), { id: '', size: 26, index: 0, lead: true, state: 'working', hovering: false })
const HUES: Record<AvatarColor, number> = { slate: 255, sage: 150, moss: 125, ocean: 222, steel: 238, denim: 258, iris: 290, plum: 330, rose: 12, clay: 45, sand: 82, teal: 188 }
// The robot is drawn a little larger than its disk, so its lit antenna peeks over the rim.
const art = computed(() => Math.round(props.size * 1.1))
const style = computed(() => ({
  '--size': `${props.size}px`,
  // Head centred a touch below the disk's centre (the head's centre is 58% down the art).
  '--art-top': `${(props.size / 2 + props.size * .06 - art.value * .579).toFixed(1)}px`,
  '--h': String(props.id ? HUES[avatarColor(props.id)] : HUES.teal),
  // Negative delays start each bot part-way through its loops.
  '--lag': `${-(props.index * 0.53 + (props.id ? (parseInt(props.id.slice(0, 2), 16) || 0) % 7 : 0) * 0.31).toFixed(2)}s`,
}))
</script>

<template>
  <span class="playful-bot" :class="[state, { lead, hovering }]" :style="style" aria-hidden="true">
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
        <g class="look">
          <g class="blink">
            <rect class="eye" x="8.2" y="11.9" width="2.4" height="3.3" rx="1.2" />
            <rect class="eye" x="13.4" y="11.9" width="2.4" height="3.3" rx="1.2" />
          </g>
        </g>
        <path class="happy" d="M8.1 14.2q1.3-1.9 2.6 0M13.3 14.2q1.3-1.9 2.6 0" />
        <circle class="cheek" cx="7.3" cy="16.6" r="1.05" />
        <circle class="cheek" cx="16.7" cy="16.6" r="1.05" />
        <path class="smile" d="M10.5 17.1q1.5 1 3 0" />
      </g>
    </svg>
    <svg v-if="state === 'waiting'" class="waiting-clock" viewBox="0 0 12 12" focusable="false">
      <circle cx="6" cy="6" r="5" /><path d="M6 3V6l2 1.5" />
    </svg>
    <template v-if="lead && state === 'working'">
      <svg class="spark s1" viewBox="0 0 10 10" focusable="false"><path d="M5 0l1.2 3.8L10 5 6.2 6.2 5 10 3.8 6.2 0 5l3.8-1.2z" /></svg>
      <svg class="spark s2" viewBox="0 0 10 10" focusable="false"><path d="M5 0l1.2 3.8L10 5 6.2 6.2 5 10 3.8 6.2 0 5l3.8-1.2z" /></svg>
    </template>
  </span>
</template>

<style scoped>
.playful-bot {
  --face: oklch(.975 .025 var(--h)); --rim: oklch(.47 .07 var(--h)); --eye: oklch(.3 .05 var(--h));
  --disk: radial-gradient(circle at 32% 26%, #fff, oklch(.93 .045 var(--h)) 70%);
  --glow: #0e6f6c; --spark: #d69b31; --blush: oklch(.8 .09 20 / .55);
  position: relative; display: inline-grid; place-items: center; flex-shrink: 0; width: var(--size); height: var(--size);
}
.disk {
  position: absolute; inset: 0; border-radius: 50%;
  background: var(--disk); box-shadow: 0 0 0 1.5px var(--surface-raised), inset 0 0 0 1px color-mix(in oklab, var(--glow) 30%, transparent), 0 2px 6px -2px rgba(14, 111, 108, .35);
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

.playful-bot.waiting { --glow: var(--warn); }
.playful-bot.stale { --face: var(--surface-raised); --rim: var(--ink-3); --eye: var(--ink-3); --disk: var(--surface-sunken); --glow: var(--ink-3); --blush: transparent; }
.playful-bot.stale * { animation: none !important; }
.waiting-clock { position: absolute; top: -4px; right: -4px; width: 12px; height: 12px; fill: var(--surface-raised); stroke: var(--warn); stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
.waiting-clock path { fill: none; }

@media (prefers-reduced-motion: no-preference) {
  .hovering:not(.stale) .bob { animation: bot-bob 1.9s ease-in-out infinite; animation-delay: var(--lag); }
  .blink { transform-box: fill-box; transform-origin: center; animation: bot-blink 4.6s linear infinite; animation-delay: var(--lag); }
  .look { animation: bot-look 7.4s ease-in-out infinite; animation-delay: var(--lag); }
  .tip { animation: bot-tip 1.9s ease-in-out infinite; animation-delay: var(--lag); }
  .tip-glow { transform-box: fill-box; transform-origin: center; animation: bot-ping 1.9s cubic-bezier(.2, .7, .2, 1) infinite; animation-delay: var(--lag); }
  .halo { animation: bot-breathe 3.2s ease-in-out infinite; animation-delay: var(--lag); }
  .spark { animation: bot-spark 3.2s ease-out infinite; animation-delay: var(--lag); }
  .s2 { animation-delay: calc(var(--lag) - 1.6s); }
  .playful-bot.hovering:not(.lead) .bob { animation-duration: 2.3s; }
  .eye, .happy { transition: opacity .12s ease; }
}
@keyframes bot-bob { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-1.1px); } }
@keyframes bot-blink { 0%, 90%, 100% { transform: scaleY(1); } 93% { transform: scaleY(.12); } 96% { transform: scaleY(1); } }
@keyframes bot-look { 0%, 34%, 100% { transform: translateX(0); } 40%, 56% { transform: translateX(.8px); } 62%, 80% { transform: translateX(-.6px); } 86% { transform: translateX(0); } }
@keyframes bot-tip { 0%, 100% { opacity: .78; } 50% { opacity: 1; } }
@keyframes bot-ping { 0% { transform: scale(1); opacity: .55; } 70%, 100% { transform: scale(2.6); opacity: 0; } }
@keyframes bot-breathe { 0%, 100% { transform: scale(.84); opacity: .4; } 50% { transform: scale(1.1); opacity: .95; } }
@keyframes bot-spark { 0% { transform: translateY(2px) scale(.2) rotate(0deg); opacity: 0; } 12% { opacity: 1; transform: translateY(0) scale(1) rotate(20deg); } 30% { opacity: 0; transform: translateY(-3px) scale(.5) rotate(60deg); } 100% { opacity: 0; } }
</style>

<style>
/* Dark: a lit face on a deep disk, the antenna in aqua. (Unscoped: these read
   the theme on <html> and the chip around the robot.) */
:root[data-theme="dark"] .playful-bot:not(.stale) {
  --face: oklch(.4 .04 var(--h)); --rim: oklch(.88 .06 var(--h)); --eye: oklch(.96 .03 var(--h));
  --disk: radial-gradient(circle at 32% 26%, oklch(.42 .05 var(--h)), oklch(.3 .04 var(--h)) 72%);
  --glow: #a4e5df; --spark: #e8c07a; --blush: oklch(.72 .1 20 / .45);
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) .playful-bot:not(.stale) {
    --face: oklch(.4 .04 var(--h)); --rim: oklch(.88 .06 var(--h)); --eye: oklch(.96 .03 var(--h));
    --disk: radial-gradient(circle at 32% 26%, oklch(.42 .05 var(--h)), oklch(.3 .04 var(--h)) 72%);
    --glow: #a4e5df; --spark: #e8c07a; --blush: oklch(.72 .1 20 / .45);
  }
}
:root[data-theme="dark"] .playful-bot.waiting { --glow: var(--warn); }
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) .playful-bot.waiting { --glow: var(--warn); } }
/* Pointed at, the robots smile back: their eyes turn into happy arcs. */
.live-chip:hover .playful-bot:not(.stale) .eye, .live-chip:focus-visible .playful-bot:not(.stale) .eye, .playful-bot:not(.stale):hover .eye { opacity: 0; }
.live-chip:hover .playful-bot:not(.stale) .happy, .live-chip:focus-visible .playful-bot:not(.stale) .happy, .playful-bot:not(.stale):hover .happy { opacity: 1; }
/* Paused while off screen (the chip sets .asleep). */
.asleep .playful-bot, .asleep .playful-bot * { animation-play-state: paused !important; }
</style>
