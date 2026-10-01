<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { codenameOf } from '../../lib/codenames'
import ReleaseName from '../ReleaseName.vue'

// A release's codename as the hero of a heading (AEON-488): all caps on a
// metallic teal gradient whose highlight band sweeps through once on load, and
// again when the hovered version stamp gives the name back. Three small sparkles
// twinkle around it at staggered times. At rest the text is the gradient's dark
// end, so it meets AA; the band only passes through. Reduced motion holds it
// all still. The version still waits behind the name, as everywhere (AEON-430).
// The root takes no box: the sparkles sit in the heading's padding, so the
// heading must be positioned and padded (see the release sheet and detail).
const props = defineProps<{ version: string; name?: string; quiet?: boolean }>()
defineSlots<{ default?: () => unknown }>()
const label = computed(() => props.name || codenameOf(props.version))
</script>

<template>
  <span class="codename" :class="{ metal: !!label && !quiet }">
    <ReleaseName :version="version" :name="name"><slot>{{ label }}</slot></ReleaseName>
    <template v-if="label && !quiet">
      <svg v-for="s in 3" :key="s" class="sparkle" :class="`s${s}`" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M12 0c.6 5.4 3.9 9.4 12 12-8.1 2.6-11.4 6.6-12 12-.6-5.4-3.9-9.4-12-12 8.1-2.6 11.4-6.6 12-12z" fill="currentColor" />
      </svg>
    </template>
  </span>
</template>

<style scoped>
.codename {
  display: contents;
  --metal-0: #0b5c59; --metal-1: #0e6f6c; --metal-hi: #7fd8cf; --metal-peak: #e9fbf8;
  --metal-lift: rgba(255, 255, 255, .7); --metal-glow: rgba(14, 111, 108, .18);
  --spark: #7fd8cf; --spark-glow: rgba(127, 216, 207, .9);
}
:root[data-theme="dark"] .codename {
  --metal-0: #bff0eb; --metal-1: #a4e5df; --metal-hi: #f3d9a4; --metal-peak: #fffaf0;
  --metal-lift: rgba(0, 0, 0, .35); --metal-glow: rgba(164, 229, 223, .22);
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) .codename {
    --metal-0: #bff0eb; --metal-1: #a4e5df; --metal-hi: #f3d9a4; --metal-peak: #fffaf0;
    --metal-lift: rgba(0, 0, 0, .35); --metal-glow: rgba(164, 229, 223, .22);
  }
}
.metal :deep(.rn-name) {
  white-space: normal; overflow: visible; text-overflow: clip; overflow-wrap: anywhere;
  text-transform: uppercase; letter-spacing: .07em; font-weight: 800;
  background-image: linear-gradient(100deg, var(--metal-0) 0%, var(--metal-1) 38%, var(--metal-hi) 47%, var(--metal-peak) 50%, var(--metal-hi) 53%, var(--metal-1) 62%, var(--metal-0) 100%);
  background-size: 320% 100%; background-position: 0 0;
  -webkit-background-clip: text; background-clip: text; color: transparent; -webkit-text-fill-color: transparent;
  filter: drop-shadow(0 1px 0 var(--metal-lift)) drop-shadow(0 6px 14px var(--metal-glow));
}
/* A search hit inside the name stays readable on its highlight. */
.metal :deep(.rn-name mark) { -webkit-text-fill-color: var(--ink); color: var(--ink); }
.metal :deep(.rn-stamp) { letter-spacing: 0; }
.sparkle { position: absolute; width: var(--sparkle, 18px); height: var(--sparkle, 18px); pointer-events: none; color: var(--spark); filter: drop-shadow(0 0 6px var(--spark-glow)); opacity: .8; }
.s1 { left: 0; top: 0; }
.s2 { right: 2px; top: 4px; width: calc(var(--sparkle, 18px) * .67); height: calc(var(--sparkle, 18px) * .67); color: var(--gold-2); filter: drop-shadow(0 0 6px rgba(232, 192, 122, .9)); }
.s3 { right: calc(18px + 18%); bottom: 0; width: calc(var(--sparkle, 18px) * .56); height: calc(var(--sparkle, 18px) * .56); }
@media (prefers-reduced-motion: no-preference) {
  .metal :deep(.rn-name) { animation: sheen 1.8s cubic-bezier(.4, 0, .2, 1) .5s 1 both; }
  /* A second name, so the sweep runs again when the stamp gives the name back. */
  .metal :deep(.release-name.shown .rn-name) { animation: sheen-again 1.6s cubic-bezier(.4, 0, .2, 1) 1 both; }
  .sparkle { animation: twinkle 2.8s ease-in-out infinite; }
  .s2 { animation-delay: .9s; }
  .s3 { animation-delay: 1.7s; }
}
@keyframes sheen { from { background-position: 100% 0; } to { background-position: 0 0; } }
@keyframes sheen-again { from { background-position: 100% 0; } to { background-position: 0 0; } }
@keyframes twinkle {
  0%, 100% { opacity: .15; transform: scale(.55) rotate(0deg); }
  45% { opacity: 1; transform: scale(1) rotate(20deg); }
  60% { opacity: .9; transform: scale(.92) rotate(25deg); }
}
</style>
