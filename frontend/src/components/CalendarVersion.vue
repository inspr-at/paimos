<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import { ref } from 'vue'

type VersionMode = 'pretty' | 'reduced'
const preferenceKey = 'paimos:calendar-version-mode'
const preferredMode = ref<VersionMode>('pretty')
try {
  if (localStorage.getItem(preferenceKey) === 'reduced') preferredMode.value = 'reduced'
} catch {
  /* Storage is optional; the shared default is Pretty. */
}
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, unref, watchEffect } from 'vue'
import { useBranding } from '@/composables/useBranding'
import display from '@/vendor/calendar-version-display/display.json'
import { disposeVersion, parts, renderVersion } from '@/vendor/calendar-version-display/version.js'
import { attachVersionInteraction } from '@/vendor/calendar-version-display/version-interaction.js'

const props = withDefaults(
  defineProps<{
    version: string
    scheme?: string
    prefix?: string
    mode?: VersionMode
    brand?: string
    interactive?: boolean
  }>(),
  { scheme: () => __APP_VERSION_SCHEME__, prefix: 'v', interactive: true },
)
const { branding } = useBranding()
const host = ref<HTMLElement | null>(null)
const mode = computed(() => props.mode ?? preferredMode.value)
const label = computed(() => `${props.prefix}${props.version}`)
const valid = computed(() => parts(label.value, props.scheme) !== null)
let reducedInteraction: { dispose: () => void } | undefined
let feedbackObserver: MutationObserver | undefined

function dispose() {
  feedbackObserver?.disconnect()
  feedbackObserver = undefined
  reducedInteraction?.dispose()
  reducedInteraction = undefined
  if (host.value) disposeVersion(host.value)
}

watchEffect(
  () => {
    const element = host.value
    if (!element) return
    dispose()
    renderVersion(element, label.value, props.scheme, {
      config: display,
      mode: mode.value,
      brand: props.brand ?? unref(branding)?.colors?.accent ?? display.tint.default,
      interactive: props.interactive,
    })
    element.dataset.version = label.value
    element.dataset.canonical = props.version
    if (valid.value && props.interactive) {
      // Upstream adds interaction for Pretty; use the same helper for SemVer.
      if (mode.value === 'reduced')
        reducedInteraction = attachVersionInteraction(element, props.version)
      element.setAttribute('aria-label', `${props.version} — Copy version`)
      feedbackObserver = new MutationObserver(() => {
        const feedback = element.querySelector('[role="status"]')
        if (!feedback) return
        if (element.dataset.copyState === 'copied') feedback.textContent = 'Copied'
        if (element.dataset.copyState === 'error')
          feedback.textContent = 'Copy unavailable. Select the version.'
      })
      feedbackObserver.observe(element, { attributes: true, attributeFilter: ['data-copy-state'] })
    }
  },
  { flush: 'post' },
)

function toggleMode() {
  preferredMode.value = mode.value === 'pretty' ? 'reduced' : 'pretty'
  try {
    localStorage.setItem(preferenceKey, preferredMode.value)
  } catch {
    /* Optional preference. */
  }
}
onBeforeUnmount(dispose)
</script>

<template>
  <span class="calendar-version">
    <span ref="host" class="calendar-version-label" />
    <button
      v-if="valid && interactive && !props.mode"
      class="calendar-version-mode"
      type="button"
      :title="mode === 'pretty' ? 'Show SemVer' : 'Show Pretty version'"
      :aria-label="mode === 'pretty' ? 'Show SemVer' : 'Show Pretty version'"
      @click.stop="toggleMode"
    >
      <svg
        viewBox="0 0 16 16"
        width="12"
        height="12"
        fill="none"
        stroke="currentColor"
        stroke-width="1.4"
        aria-hidden="true"
      >
        <path d="M5.5 4 1.5 8l4 4m5-8 4 4-4 4M9.5 2l-3 12" />
      </svg>
    </button>
  </span>
</template>

<style scoped>
.calendar-version {
  display: inline-flex;
  align-items: center;
  gap: 0.3em;
  max-width: 100%;
}
.calendar-version-label {
  min-width: 0;
}
.calendar-version-mode {
  display: inline-flex;
  align-items: center;
  padding: 0.15em;
  border: 0;
  background: transparent;
  color: inherit;
  opacity: 0.55;
  cursor: pointer;
  flex: none;
}
.calendar-version-mode:hover,
.calendar-version-mode:focus-visible {
  opacity: 1;
}
</style>
