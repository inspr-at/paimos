<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Uses the same pinned presentation bundle as Aeon's shell; never infers a
// release version or its scheme from a timestamp, key or coordinate shape.
import { ref, watchEffect, onBeforeUnmount } from 'vue'
import { renderVersion, disposeVersion } from '../../vendor/calendar-version-display/version.js'
import display from '../../vendor/calendar-version-display/display.json'
import { VERSION_COPY_TEXT } from '../../lib/version-copy'
import { attachVersionReveal, revealControl } from '../../lib/version-reveal'
// Not interactive (inside a row that navigates): the row's hover or keyboard
// focus reveals the seconds and a click still reaches the row.
const props = defineProps<{ version: string; scheme: string; interactive?: boolean }>()
const host = ref<HTMLElement>(), error = ref(false)
let reveal: (() => void) | undefined
watchEffect(() => {
  reveal?.(); reveal = undefined
  if (!host.value) return
  error.value = false
  const interactive = props.interactive ?? true
  try {
    renderVersion(host.value, props.version, props.scheme, { config: display, mode: 'pretty', brand: '#D69B31', interactive, text: VERSION_COPY_TEXT })
    if (!interactive) reveal = attachVersionReveal(host.value, revealControl(host.value) ?? host.value)
  } catch { disposeVersion(host.value); host.value.textContent = ''; error.value = true }
}, { flush: 'post' })
onBeforeUnmount(() => { reveal?.(); if (host.value) disposeVersion(host.value) })
</script>
<template><span class="release-version"><span ref="host" /><span v-if="error" class="error">Version metadata unavailable</span></span></template>
<style scoped>.release-version { display:inline-flex; align-items:center; min-height:32px; font:11px var(--mono); }</style>
