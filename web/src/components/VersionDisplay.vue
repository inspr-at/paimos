<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch, watchEffect } from 'vue'
import { renderVersion, disposeVersion } from '../vendor/calendar-version-display/version.js'
import display from '../vendor/calendar-version-display/display.json'
import { useVersion } from '../stores/version'
import { codenameOf, releaseAria } from '../lib/codenames'
import { copyToClipboard, VERSION_COPY_TEXT } from '../lib/version-copy'
import ReleaseName from './ReleaseName.vue'

// The running release: its marketing name, with the calendar version on hover or
// keyboard focus (AEON-430). The name is the control that copies the version.
// A build without a name (dev, no history) draws the renderer's own copy pill.
// menuitem: inside a menu the copy control is one of its items (roving focus, AEON-312).
const props = defineProps<{ menuitem?: boolean }>()
const version = useVersion()
const host = ref<HTMLElement>()
void version.load()
const running = computed(() => version.value && version.value.version !== 'dev' ? version.value.version : '')
const named = computed(() => running.value ? version.value?.codename || codenameOf(running.value) : '')
const label = computed(() => `${releaseAria(named.value, running.value)} — ${VERSION_COPY_TEXT.copy}`)

watchEffect(() => {
  if (!host.value || !version.value) return
  const { version: value, scheme } = version.value
  if (value === 'dev') {
    disposeVersion(host.value)
    host.value.textContent = 'dev'
  } else {
    renderVersion(host.value, value, scheme, { config: display, mode: 'pretty', brand: '#D69B31', text: VERSION_COPY_TEXT })
    // The renderer names the copy control itself (AEON-309); inside a menu it becomes one of its items.
    if (props.menuitem && host.value.getAttribute('role') === 'button') { host.value.setAttribute('role', 'menuitem'); host.value.setAttribute('tabindex', '-1') }
  }
}, { flush: 'post' })
// The name can arrive after the pill was drawn (the history loads): let go of the pill.
watch(host, (_, old) => { if (old) disposeVersion(old) })
onBeforeUnmount(() => { if (host.value) disposeVersion(host.value) })

// Copy: the exact canonical version. The state is said aloud and shown in place of the name for a moment.
const state = ref<'' | 'copied' | 'failed'>('')
const note = ref<HTMLElement>()
let timer: ReturnType<typeof setTimeout> | undefined
async function copy() {
  clearTimeout(timer)
  state.value = await copyToClipboard(running.value.replace(/^v/, '')) ? 'copied' : 'failed'
  if (state.value === 'failed') {
    // Last resort: the version, selected, so it can be copied by hand.
    await nextTick()
    if (note.value) window.getSelection()?.selectAllChildren(note.value)
  }
  timer = setTimeout(() => { state.value = '' }, state.value === 'failed' ? 6000 : 2000)
}
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <span class="version-display">
    <span v-if="version.failed" class="version-fallback">Version unavailable</span>
    <button
      v-else-if="named" type="button" class="version-copy-named" :role="menuitem ? 'menuitem' : undefined" :tabindex="menuitem ? -1 : undefined"
      :aria-label="label" :data-copy-state="state || undefined" @click="copy"
    >
      <ReleaseName v-show="!state" :version="running" :name="named" />
      <span v-if="state" ref="note" class="copy-note">{{ state === 'copied' ? VERSION_COPY_TEXT.copied : running.replace(/^v/, '') }}</span>
      <span class="sr-only" role="status" aria-live="polite">{{ state === 'copied' ? VERSION_COPY_TEXT.copied : state === 'failed' ? VERSION_COPY_TEXT.failed : '' }}</span>
    </button>
    <span v-else ref="host" class="version-coordinate" :aria-label="version.value ? undefined : 'Loading version'"></span>
  </span>
</template>

<style scoped>
.version-display { display: inline-flex; align-items: center; min-height: 44px; font: 500 12.5px/1.5 var(--mono); color: var(--ink); font-variant-ligatures: none; }
.version-coordinate { height: 44px; min-height: 44px; min-width: 44px; line-height: 44px; }
.version-fallback { font-size: 11.5px; color: var(--ink-2); }
/* The name in the interface's own voice, the stamp in mono; a copy cursor says what a click does. */
.version-copy-named { display: inline-flex; align-items: center; height: 44px; min-width: 44px; padding: 0 6px; border: 0; border-radius: 8px; background: transparent; color: inherit; font: inherit; cursor: copy; text-align: left; }
.version-copy-named :deep(.rn-name) { font: 600 12.5px/1.5 var(--font); letter-spacing: .005em; }
.version-copy-named:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.copy-note { font: 600 12px/1.5 var(--mono); user-select: text; }
</style>
