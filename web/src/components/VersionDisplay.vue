<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watchEffect } from 'vue'
import { renderVersion, disposeVersion } from '../vendor/calendar-version-display/version.js'
import display from '../vendor/calendar-version-display/display.json'
import { useVersion } from '../stores/version'
import { VERSION_COPY_TEXT } from '../lib/version-copy'

const version = useVersion()
const host = ref<HTMLElement>()
void version.load()
watchEffect(() => {
  if (!host.value || !version.value) return
  const { version: value, scheme } = version.value
  if (value === 'dev') {
    disposeVersion(host.value)
    host.value.textContent = 'dev'
  } else {
    renderVersion(host.value, value, scheme, { config: display, mode: 'pretty', brand: '#D69B31', text: VERSION_COPY_TEXT })
  }
}, { flush: 'post' })
onBeforeUnmount(() => { if (host.value) disposeVersion(host.value) })
</script>

<template>
  <span class="version-display">
    <span v-if="version.failed" class="version-fallback">Version unavailable</span>
    <span v-else ref="host" class="version-coordinate" :aria-label="version.value ? undefined : 'Loading version'"></span>
  </span>
</template>

<style scoped>
.version-display { display: inline-flex; align-items: center; min-height: 44px; font: 500 12.5px/1.5 var(--mono); color: var(--ink); font-variant-ligatures: none; }
.version-coordinate { height: 44px; min-height: 44px; min-width: 44px; line-height: 44px; }
.version-fallback { font-size: 11.5px; color: var(--ink-2); }
</style>
