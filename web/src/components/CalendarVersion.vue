<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watchEffect } from 'vue'
import { disposeVersion, parts, renderVersion } from '../vendor/calendar-version-display/version.js'
import display from '../vendor/calendar-version-display/display.json'
import { CALENDAR_DISPLAY_SCHEME } from '../lib/version-copy'

// A calendar version in the shared INSPR renderer's Pretty display, for reading
// (the copy interaction lives in VersionDisplay). Other versions read as text.
const props = withDefaults(defineProps<{ value: string; scheme?: string }>(), { scheme: CALENDAR_DISPLAY_SCHEME })
const host = ref<HTMLElement>()
watchEffect(() => {
  const element = host.value
  if (!element) return
  if (!parts(props.value, props.scheme)) { disposeVersion(element); element.textContent = props.value; return }
  renderVersion(element, props.value, props.scheme, { config: display, mode: 'pretty', brand: '#D69B31', interactive: false })
}, { flush: 'post' })
onBeforeUnmount(() => { if (host.value) disposeVersion(host.value) })
</script>

<template>
  <span ref="host" class="calendar-version" />
</template>

<style scoped>
.calendar-version { display: inline-block; font-variant-ligatures: none; white-space: nowrap; }
</style>
