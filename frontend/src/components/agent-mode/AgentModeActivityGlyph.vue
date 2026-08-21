<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — activity glyph. Each activity kind has a DISTINCT STATIC SHAPE
  so the state reads with animation disabled; motion is a pure
  enhancement behind prefers-reduced-motion: no-preference.
-->
<script setup lang="ts">
import type { ActivityKind } from '@/services/agentMode'
import AppIcon from '@/components/AppIcon.vue'

withDefaults(defineProps<{ kind: ActivityKind; size?: 'sm' | 'md' }>(), { size: 'sm' })
</script>

<template>
  <span class="am-glyph" :class="[`am-glyph--${kind}`, `am-glyph--${size}`]" aria-hidden="true">
    <template v-if="kind === 'working'">
      <i></i><i></i><i></i>
    </template>
    <template v-else-if="kind === 'testing' || kind === 'verifying'">
      <i class="am-glyph-ring"></i>
    </template>
    <template v-else-if="kind === 'deploying'">
      <i></i><i></i><i></i><b>›</b>
    </template>
    <AppIcon v-else-if="kind === 'waiting'" name="hourglass" :size="size === 'md' ? 16 : 12" />
    <AppIcon v-else-if="kind === 'blocked'" name="octagon-alert" :size="size === 'md' ? 16 : 12" />
    <i v-else class="am-glyph-dash"></i>
  </span>
</template>

<style scoped>
.am-glyph {
  position: relative;
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  gap: 2px;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  color: var(--am-glyph-color, currentColor);
}
.am-glyph--md { width: 28px; height: 28px; gap: 3px; }

/* working: three bars of different heights (static shape is recognisable) */
.am-glyph--working { color: var(--am-green); }
.am-glyph--working > i { display: block; width: 2px; height: 8px; border-radius: 2px; background: currentColor; }
.am-glyph--working > i:nth-child(2) { height: 13px; }
.am-glyph--working > i:nth-child(3) { height: 6px; }
.am-glyph--md.am-glyph--working > i { width: 3px; height: 12px; }
.am-glyph--md.am-glyph--working > i:nth-child(2) { height: 20px; }
.am-glyph--md.am-glyph--working > i:nth-child(3) { height: 9px; }

/* testing / verifying: open ring with a dot */
.am-glyph--testing,
.am-glyph--verifying { color: var(--am-blue); }
.am-glyph--verifying { color: var(--am-purple); }
.am-glyph-ring {
  display: block;
  width: 100%;
  height: 100%;
  border: 2px solid color-mix(in srgb, currentColor 28%, transparent);
  border-top-color: currentColor;
  border-right-color: currentColor;
  border-radius: 50%;
}
.am-glyph-ring::after {
  content: '';
  position: absolute;
  inset: 0;
  margin: auto;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: currentColor;
}

/* deploying: three dots and a chevron */
.am-glyph--deploying { color: var(--am-purple); }
.am-glyph--deploying > i { display: block; width: 4px; height: 4px; border-radius: 50%; background: currentColor; }
.am-glyph--deploying > b { font-weight: 500; font-size: 14px; line-height: 1; margin-left: 1px; }

/* waiting: hourglass inside a soft amber disc */
.am-glyph--waiting {
  color: var(--am-amber);
  background: color-mix(in srgb, var(--am-amber) 14%, transparent);
  border: 1px solid color-mix(in srgb, var(--am-amber) 40%, transparent);
}

/* blocked: octagon inside a soft red disc */
.am-glyph--blocked {
  color: var(--am-red);
  background: color-mix(in srgb, var(--am-red) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--am-red) 42%, transparent);
}

/* idle / unknown: dashed hollow circle */
.am-glyph--idle,
.am-glyph--unknown { color: var(--am-muted); }
.am-glyph-dash { display: block; width: 100%; height: 100%; border: 1.5px dashed currentColor; border-radius: 50%; }

@media (prefers-reduced-motion: no-preference) {
  .am-glyph--working > i { animation: am-bars 1.1s ease-in-out infinite; transform-origin: center; }
  .am-glyph--working > i:nth-child(2) { animation-delay: -0.25s; }
  .am-glyph--working > i:nth-child(3) { animation-delay: -0.5s; }
  .am-glyph-ring { animation: am-ring 1.8s linear infinite; }
  @keyframes am-bars { 0%, 100% { transform: scaleY(0.6); } 50% { transform: scaleY(1.05); } }
  @keyframes am-ring { to { transform: rotate(360deg); } }
}
</style>
