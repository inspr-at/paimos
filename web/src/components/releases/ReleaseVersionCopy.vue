<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watchEffect } from 'vue'
import { disposeVersion, renderVersion } from '../../vendor/calendar-version-display/version.js'
import display from '../../vendor/calendar-version-display/display.json'
import { CALENDAR_DISPLAY_SCHEME, copyRenderedVersion } from '../../lib/version-copy'
import { attachVersionCrossfade } from '../../lib/version-reveal'
import { dark } from '../../lib/theme'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ value: string }>()
const canonical = computed(() => props.value.replace(/^v/, ''))
const trigger = ref<HTMLButtonElement>()
const pretty = ref<HTMLElement>()
const full = ref<HTMLElement>()
const state = ref<'' | 'copied' | 'failed'>('')
let off: (() => void) | undefined
let timer: ReturnType<typeof setTimeout> | undefined
let serial = 0
watchEffect(() => {
  // The renderer accepts a resolved brand colour. Follow the product theme so
  // its auto tint remains legible when the theme changes while the sheet is open.
  void dark.value
  off?.(); off = undefined
  if (!pretty.value || !full.value || !trigger.value) return
  const options = { config: display, interactive: false, brand: getComputedStyle(pretty.value).getPropertyValue('--secondary-ink').trim() }
  renderVersion(pretty.value, canonical.value, CALENDAR_DISPLAY_SCHEME, { ...options, mode: 'pretty' })
  renderVersion(full.value, canonical.value, CALENDAR_DISPLAY_SCHEME, { ...options, mode: 'reduced' })
  off = attachVersionCrossfade(pretty.value, full.value, trigger.value)
  ++serial
  clearTimeout(timer)
  state.value = ''
}, { flush: 'post' })
async function copy() {
  if (!pretty.value) return
  const attempt = ++serial
  clearTimeout(timer)
  state.value = ''
  const ok = await copyRenderedVersion(pretty.value)
  if (attempt !== serial) return
  state.value = ok ? 'copied' : 'failed'
  timer = setTimeout(() => { state.value = '' }, 2000)
}
onBeforeUnmount(() => {
  ++serial
  clearTimeout(timer)
  off?.()
  if (pretty.value) disposeVersion(pretty.value)
  if (full.value) disposeVersion(full.value)
})
</script>

<template>
  <span class="release-version">
    <button ref="trigger" type="button" class="version-copy" :aria-label="`Copy version ${canonical}`" :data-copy-state="state || undefined" @click="copy">
      <span class="version-layers" aria-hidden="true">
        <span ref="pretty" class="version-pretty" />
        <span ref="full" class="version-canonical" />
      </span>
      <AppIcon class="copy-icon" :name="state === 'copied' ? 'check' : 'copy'" :size="14" />
    </button>
    <span class="sr-only" role="status" aria-live="polite">{{ state === 'copied' ? 'Version copied' : state === 'failed' ? 'Copy unavailable. Select the canonical version to copy it.' : '' }}</span>
    <input v-if="state === 'failed'" class="copy-fallback" :value="canonical" readonly aria-label="Canonical version to copy" @focus="($event.target as HTMLInputElement).select()" />
  </span>
</template>

<style scoped>
.release-version { position: relative; display: inline-flex; align-items: center; min-width: 0; max-width: 100%; }
.version-copy { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; min-height: 44px; padding: 0 6px; margin: -6px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: 500 13px/1.5 var(--mono); cursor: copy; }
.version-copy:hover { background: var(--row-hover); }
.version-copy:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
/* Different fonts and sizes share a text baseline, not the centre of their line boxes. */
.version-layers { display: inline-grid; align-items: baseline; min-width: 0; }
.version-layers > span { grid-area: 1 / 1; }
.version-pretty { color: var(--ink-2); }
/* Keep the renderer's 80% time weight legible on the glass dock. */
.version-pretty :deep(.hh), .version-pretty :deep(.mi) { color: var(--ink); }
/* The renderer supplies an inline separator colour; the dock uses its theme ink. */
.version-pretty :deep(.separator) { color: var(--ink-2) !important; }
.version-canonical { color: var(--ink); }
.copy-icon { flex: none; color: var(--ink-2); opacity: 0; transition: opacity var(--version-reveal-duration, 1000ms) ease-in-out; }
.version-copy[data-version-view="revealed"] .copy-icon, .version-copy[data-copy-state] .copy-icon { opacity: 1; }
.version-copy[data-copy-state="copied"] .copy-icon { color: var(--ok); }
.copy-fallback { position: absolute; top: 100%; right: 0; z-index: 3; width: 21ch; padding: 8px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--surface); color: var(--ink); font: 13px/1.5 var(--mono); }
@media (prefers-reduced-motion: reduce) { .copy-icon { transition: none; } }
</style>
