<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref } from 'vue'
import { listHostLabels, setHostLabel, message } from '../../lib/agents'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
const props = defineProps<{ host: string; label?: string; editable: boolean }>()
const emit = defineEmits<{ renamed: [label: string] }>()
const anchor = ref<HTMLElement | null>(null)
const draft = ref('')
const busy = ref(false)
const error = ref('')
const input = ref<HTMLInputElement>()
let mounted = true
onBeforeUnmount(() => { mounted = false; operation++ })
let operation = 0
async function open(event: MouseEvent) {
  if (!props.editable) return
  draft.value = props.label || props.host
  error.value = ''
  anchor.value = event.currentTarget as HTMLElement
  const current = ++operation
  busy.value = true
  try {
    const labels = await listHostLabels()
    if (current !== operation) return
    const label = labels.find(item => item.host === props.host)?.label || props.host
    draft.value = label
    emit('renamed', label)
  } catch (err) { if (current === operation) error.value = message(err) }
  finally { if (current === operation) { busy.value = false; await nextTick(); input.value?.focus() } }
}
function close(restore = false) {
  const target = anchor.value
  anchor.value = null
  operation++
  busy.value = false
  if (restore) target?.focus({ preventScroll: true })
}
async function save(reset = false) {
  if (busy.value) return
  const label = draft.value.trim()
  if (!reset && (!label || label.length > 128)) { error.value = 'Enter a name of 1–128 characters.'; return }
  busy.value = true
  error.value = ''
  try {
    const result = await setHostLabel(props.host, reset ? null : label)
    if (!mounted) return
    emit('renamed', result.label)
    close(true)
  } catch (err) { error.value = message(err) }
  finally { busy.value = false }
}
</script>

<template>
  <span class="host-control">
    <button type="button" class="host-badge" :disabled="!editable" :title="`${label || host} · ${host}`" :aria-label="`Your name for this computer: ${label || host}`" @click.stop="open">
      <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="2.5" width="10" height="8" rx="1" /><path d="m3 10.5-1.5 2h13l-1.5-2" /></svg>
      <span class="host-name">{{ label || host }}</span>
    </button>
    <button v-if="editable" type="button" class="host-pencil" :aria-label="`Rename computer ${host} for yourself`" @click.stop="open"><AppIcon name="edit" :size="12" /></button>
    <FloatingPanel v-if="anchor" :anchor="anchor" label="Your name for this computer" :width="300" cycle @close="close">
      <form class="host-form" @submit.prevent="save()">
        <label>Your name for this computer<input ref="input" v-model="draft" aria-label="Your name for this computer" maxlength="128" data-autofocus :disabled="busy" /></label>
        <p>Only you see this name. Registered host: {{ host }}</p>
        <p v-if="error" role="alert">{{ error }}</p>
        <div class="host-buttons"><button class="btn primary" :disabled="busy" type="submit">Save</button><button class="btn" :disabled="busy" type="button" @click="save(true)">Use '{{ host }}'</button><button class="btn" type="button" @click="close(true)">Cancel</button></div>
      </form>
    </FloatingPanel>
  </span>
</template>

<style scoped>
.host-control { position: relative; display: inline-flex; align-items: center; }
.host-badge { display: inline-flex; align-items: center; gap: 5px; width: 104px; height: 24px; box-sizing: border-box; padding: 0 7px; border: 0; border-radius: 999px; background: color-mix(in srgb, var(--ink-3) 8%, transparent); color: var(--ink-3); font: 11px/1 var(--mono); cursor: pointer; }
.host-badge:disabled { opacity: 1; cursor: default; }
.host-badge svg { flex: none; }
.host-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.host-pencil { position: absolute; left: calc(100% + 2px); top: 0; display: grid; place-items: center; width: 20px; height: 24px; border: 0; background: transparent; color: var(--ink-3); opacity: 0; cursor: pointer; }
:global(.c-host:hover) .host-pencil, .host-control:focus-within .host-pencil { opacity: 1; }
.host-pencil:focus-visible, .host-badge:focus-visible { outline: 2px solid var(--ink-3); outline-offset: 2px; }
.host-form { padding: 8px; }
.host-form label { display: grid; gap: 8px; font-size: 13px; color: var(--ink); }
.host-form input { width: 100%; box-sizing: border-box; }
.host-form p { font-size: 11.5px; color: var(--ink-3); overflow-wrap: anywhere; }
.host-buttons { display: flex; flex-wrap: wrap; gap: 6px; }
@media (hover: none) { .host-pencil { opacity: 1; } }
</style>
