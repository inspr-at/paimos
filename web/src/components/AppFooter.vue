<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { brand } from '../lib/brand'
import { useReleases } from '../stores/releases'
import { useSession } from '../stores/session'
import { useVersion } from '../stores/version'
import AppIcon from './AppIcon.vue'
import CalendarVersion from './CalendarVersion.vue'

// The footer bar: the product's name and the running version, which opens the
// release history. A small badge says how many releases are new since the last look.
defineProps<{ hidden?: boolean }>()
const emit = defineEmits<{ releases: [] }>()
const version = useVersion()
const releases = useReleases()
const session = useSession()
void version.load()
const value = computed(() => version.value?.version ?? '')
const count = computed(() => session.identity ? releases.newCount : 0)
const label = computed(() => {
  const running = value.value ? `, version ${value.value}` : ''
  const fresh = count.value === null ? ', new releases since your last visit' : count.value ? `, ${count.value} new since your last visit` : ''
  return `Release history${running}${fresh}`
})
</script>

<template>
  <footer class="app-footer" :class="{ hidden }" :inert="hidden || undefined">
    <span class="footer-name">{{ brand.wordmark }}</span>
    <span class="spacer" />
    <button v-if="session.identity" type="button" class="version-pill" :aria-label="label" data-tip="Release history" @click="emit('releases')">
      <span class="pill-face">
        <AppIcon name="history" :size="13" />
        <span v-if="version.failed" class="fallback">Version unavailable</span>
        <CalendarVersion v-else-if="value" :value="value" class="pill-version" />
        <span v-else class="skeleton pill-skeleton" />
        <span v-if="count !== 0" class="new-badge" aria-hidden="true">{{ count === null ? 'New' : `${count} new` }}</span>
      </span>
    </button>
    <span v-else class="version-plain"><CalendarVersion v-if="value" :value="value" /></span>
  </footer>
</template>

<style scoped>
/* Same glass language as the header rail, mirrored: a hairline on top. */
.app-footer {
  position: relative; z-index: 18; display: flex; align-items: center; gap: 12px; height: var(--footer-h); min-height: 0; padding: 0 var(--gutter); overflow: clip;
  background: var(--glass-2); box-shadow: inset 0 1px 0 var(--glass-edge), 0 -1px 0 var(--line);
  -webkit-backdrop-filter: blur(16px) saturate(1.2); backdrop-filter: blur(16px) saturate(1.2);
  color: var(--ink-2);
}
.footer-name { font: 600 10px/1 var(--mono); letter-spacing: .24em; color: var(--ink-3); white-space: nowrap; font-variant-ligatures: none; }
.spacer { flex: 1 1 0; }
/* The button is the whole bar height (a comfortable target); the pill is its face. */
.version-pill { display: inline-flex; align-items: center; height: 100%; padding: 0; border: 0; background: transparent; color: var(--ink-2); }
.version-pill:focus-visible { box-shadow: none; }
.pill-face {
  display: inline-flex; align-items: center; gap: 7px; height: 26px; padding: 0 10px 0 9px; border: 1px solid var(--glass-edge); border-radius: 999px;
  background: var(--field-bg); box-shadow: 0 0 0 1px var(--line); font: 500 12px/1 var(--mono); white-space: nowrap;
  transition: box-shadow .15s ease, background .15s ease, color .15s ease;
}
@media (hover: hover) { .version-pill:hover .pill-face { color: var(--teal-ink); background: var(--btn-bg-hover); box-shadow: 0 0 0 1px var(--glass-rim), 0 4px 12px -6px rgba(32, 60, 61, .3); } }
.version-pill:active .pill-face { background: var(--row-selected); }
.version-pill:focus-visible .pill-face { box-shadow: var(--focus-ring); }
.pill-version { color: var(--ink); font-size: 12px; line-height: 1; }
/* As wide as a calendar version in the pill, so the pill keeps its size when the version lands. */
.pill-skeleton { width: 94px; height: 8px; }
.fallback { font-family: var(--font); font-size: 11.5px; }
.new-badge {
  display: inline-flex; align-items: center; height: 17px; margin-right: -5px; padding: 0 7px; border-radius: 999px;
  background: var(--gold-2); color: #3a2804; font: 700 10px/1 var(--mono); letter-spacing: .02em; box-shadow: 0 0 0 1px rgba(154, 107, 18, .35);
}
.version-plain { font-size: 12px; color: var(--ink); }
@media (max-width: 600px) {
  .app-footer { gap: 8px; padding: 0 12px; transition: transform .22s ease; }
  .app-footer.hidden { transform: translateY(100%); }
  .footer-name { font-size: 9px; letter-spacing: .18em; }
  .pill-face { height: 30px; }
  .pill-version { font-size: 11.5px; }
  .pill-skeleton { width: 90px; }
}
@media (prefers-reduced-motion: reduce) { .app-footer, .pill-face { transition: none; } }
</style>
