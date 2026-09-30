<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { codenameOf } from '../lib/codenames'
import { revealControl, watchTrigger } from '../lib/version-reveal'
import CalendarVersion from './CalendarVersion.vue'

// A release by its marketing name (AEON-430). Hover or keyboard focus on the
// name, or on the control around it (the footer's history button, a release
// row), reveals the Pretty calendar version ("26·09·30 11:53") over the name in
// the footer's chip style; a screen reader gets it as the name's description.
// A release with no known name reads as its calendar version, as before.
const props = defineProps<{ version: string; name?: string }>()
defineSlots<{ default?: () => unknown }>()
const label = computed(() => props.name || codenameOf(props.version))
const id = useId()
const root = ref<HTMLElement>()
const shown = ref(false)
const standalone = ref(false)
let stop: (() => void) | undefined
// The name can arrive after the first paint (the history loads), so follow the element.
watch(root, host => {
  stop?.(); stop = undefined; shown.value = false
  if (!host) return
  const control = revealControl(host)
  standalone.value = !control
  stop = watchTrigger(control ?? host, on => { shown.value = on })
}, { flush: 'post' })
onBeforeUnmount(() => stop?.())
</script>

<template>
  <span v-if="label" ref="root" class="release-name" :class="{ shown, standalone }" :data-version="version" :tabindex="standalone ? 0 : undefined" :aria-describedby="id">
    <span class="rn-name" lang="en"><slot>{{ label }}</slot></span>
    <span :id="id" class="rn-stamp" role="tooltip"><CalendarVersion :value="version" rest /></span>
  </span>
  <CalendarVersion v-else :value="version" />
</template>

<style scoped>
/* Name and stamp share one cell, so the width is the wider of the two and revealing never moves the layout. */
.release-name { display: inline-grid; grid-template-columns: minmax(0, auto); align-items: center; max-width: 100%; min-width: 0; border-radius: 8px; outline: none; }
.release-name:focus-visible { box-shadow: var(--focus-ring); }
.rn-name, .rn-stamp { grid-area: 1 / 1; min-width: 0; transition: opacity .16s ease; }
.rn-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rn-stamp { justify-self: start; opacity: 0; pointer-events: none; font: 500 12px/1 var(--mono); color: var(--ink); white-space: nowrap; }
.shown .rn-name { opacity: 0; }
.shown .rn-stamp { opacity: 1; }
/* On its own (a heading, a tile) the stamp is the footer's chip, small beside a larger name. */
.standalone .rn-stamp { padding: 4px 10px; border: 1px solid var(--glass-edge); border-radius: 999px; background: var(--field-bg); box-shadow: 0 0 0 1px var(--line); }
@media (prefers-reduced-motion: reduce) { .rn-name, .rn-stamp { transition: none; } }
</style>
