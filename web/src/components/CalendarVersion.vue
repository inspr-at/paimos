<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watchEffect } from 'vue'
import { disposeVersion, parts, renderVersion } from '../vendor/calendar-version-display/version.js'
import display from '../vendor/calendar-version-display/display.json'
import { CALENDAR_DISPLAY_SCHEME, VERSION_COPY_TEXT } from '../lib/version-copy'
import { attachVersionReveal, revealControl } from '../lib/version-reveal'

// A calendar version in the shared INSPR renderer's Pretty display. Standing
// alone it is the renderer's own pill (hover, focus or tap reveals the seconds;
// a click copies). Inside a control (a release row, the footer's history button)
// that control keeps its action and its hover or focus reveals the seconds.
// Other versions read as text.
// `rest` draws the Pretty display as plain text (no copy button, no reveal of the
// seconds): the stamp a marketing name shows on hover.
const props = withDefaults(defineProps<{ value: string; scheme?: string; rest?: boolean }>(), { scheme: CALENDAR_DISPLAY_SCHEME, rest: false })
const host = ref<HTMLElement>()
let reveal: (() => void) | undefined
watchEffect(() => {
  const element = host.value
  reveal?.(); reveal = undefined
  if (!element) return
  if (!parts(props.value, props.scheme)) { disposeVersion(element); element.textContent = props.value; return }
  const control = props.rest ? null : revealControl(element)
  renderVersion(element, props.value, props.scheme, { config: display, mode: 'pretty', brand: '#D69B31', interactive: !props.rest && !control, text: VERSION_COPY_TEXT })
  if (control) reveal = attachVersionReveal(element, control)
}, { flush: 'post' })
onBeforeUnmount(() => { reveal?.(); if (host.value) disposeVersion(host.value) })
</script>

<template>
  <span ref="host" class="calendar-version" />
</template>

<style scoped>
.calendar-version { display: inline-block; font-variant-ligatures: none; white-space: nowrap; }
</style>
