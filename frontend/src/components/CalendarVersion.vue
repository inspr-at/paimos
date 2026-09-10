<!--
  SPDX-License-Identifier: AGPL-3.0-only
  Copyright (C) 2026 Markus Barta

  CalendarVersion — renders a version label with the INSPR calendar v2 display
  weights (PAI-989, INSPR-400). The weights, tinted segments and mix are read at
  build time from src/brand/calendar-version-display.json, an in-repo copy of the
  doctrine data file pinned by scripts/check-calendar-version-display.sh.

  Only an `inspr-calendar-v2` coordinate is weighted, and only when the release
  record says so (the scheme comes from scripts/release/version-scheme.json via
  __APP_VERSION_SCHEME__, never from the string's shape). Everything else renders
  plain. The element's text content and data-version stay the canonical label.
-->
<script setup lang="ts">
import { computed } from 'vue'

import display from '@/brand/calendar-version-display.json'

const SEGMENTS = ['v', 'yy', 'mm', 'dd', 'hh', 'mi', 'ss', 'tail'] as const
type Segment = (typeof SEGMENTS)[number]
const CALENDAR_V2 = /^[1-9]\d{11}\.0\.0$/

/** Paimos' Schmuckfarbe: the teal selection ink. */
const TINT = 'var(--paimos-teal, #0e6f6c)'

const props = withDefaults(
  defineProps<{
    version: string
    scheme?: string
    prefix?: string
  }>(),
  { scheme: () => __APP_VERSION_SCHEME__, prefix: 'v' },
)

const plain = computed(() => `${props.prefix}${props.version}`)
const weighted = computed(
  () =>
    display.scheme === props.scheme &&
    props.scheme === 'inspr-calendar-v2' &&
    CALENDAR_V2.test(props.version),
)
const parts = computed<Record<Segment, string>>(() => {
  const d = props.version
  return {
    v: props.prefix,
    yy: d.slice(0, 2),
    mm: d.slice(2, 4),
    dd: d.slice(4, 6),
    hh: d.slice(6, 8),
    mi: d.slice(8, 10),
    ss: d.slice(10, 12),
    tail: d.slice(12),
  }
})
const properties = display.css.properties as Record<Segment | 'tint' | 'mix', string>
const weights = display.weights as Record<Segment, number>
const style = computed(() => {
  const vars: Record<string, string> = {}
  for (const segment of SEGMENTS) vars[properties[segment]] = String(weights[segment])
  vars[properties.tint] = TINT
  vars[properties.mix] = `${Math.round(display.tint.mix * 100)}%`
  return vars
})
const tinted = new Set(display.tint.segments)
const segments = SEGMENTS
</script>

<template>
  <span v-if="weighted" class="cv2" :data-version="plain" :style="style"
    ><b
      v-for="segment in segments"
      :key="segment"
      :class="[segment, { tinted: tinted.has(segment) }]"
      >{{ parts[segment] }}</b
    ></span
  >
  <template v-else>{{ plain }}</template>
</template>

<style scoped>
.cv2 {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.cv2 > b {
  font-weight: inherit;
}
.cv2 .v {
  opacity: var(--o-v);
}
.cv2 .yy {
  opacity: var(--o-yy);
}
.cv2 .mm {
  opacity: var(--o-mm);
}
.cv2 .dd {
  opacity: var(--o-dd);
}
.cv2 .hh {
  opacity: var(--o-hh);
}
.cv2 .mi {
  opacity: var(--o-mi);
}
.cv2 .ss {
  opacity: var(--o-ss);
}
.cv2 .tail {
  opacity: var(--o-tail);
}
.cv2 .tinted {
  color: color-mix(in oklab, currentColor, var(--cv2-tint) var(--cv2-mix));
}
</style>
