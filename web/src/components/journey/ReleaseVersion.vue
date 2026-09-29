<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Uses the same pinned presentation bundle as Aeon's shell; never infers a
// release version or its scheme from a timestamp, key or coordinate shape.
import { ref, watchEffect, onBeforeUnmount } from 'vue'
import { renderVersion, disposeVersion } from '../../vendor/calendar-version-display/version.js'
import display from '../../vendor/calendar-version-display/display.json'
import { VERSION_COPY_TEXT } from '../../lib/version-copy'
const props = defineProps<{ version: string; scheme: string; interactive?: boolean }>()
const host = ref<HTMLElement>(), error = ref(false)
watchEffect(() => {
  if (!host.value) return
  error.value = false
  try {
    renderVersion(host.value, props.version, props.scheme, { config: display, mode: 'pretty', brand: '#D69B31', interactive: props.interactive ?? true, text: VERSION_COPY_TEXT })
  } catch { disposeVersion(host.value); host.value.textContent = ''; error.value = true }
}, { flush: 'post' })
onBeforeUnmount(() => { if (host.value) disposeVersion(host.value) })
</script>
<template><span class="release-version"><span ref="host" /><span v-if="error" class="error">Version metadata unavailable</span></span></template>
<style scoped>.release-version { display:inline-flex; align-items:center; min-height:32px; font:11px var(--mono); }</style>
