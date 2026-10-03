<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { CAP_MAX, limitMode, stepHarnessLimit, typedLimit, typedTotal, type HarnessLimit } from '../../lib/agentsWorking'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ value: HarnessLimit | undefined; effective: number; label: string; viewer: string; revision: string | null; total?: boolean }>()
const emit = defineEmits<{ step: [delta: number]; edit: [value: HarnessLimit]; boundary: [mode: 'off' | 'none'] }>()
const editing = ref(false), draft = ref(''), input = ref<HTMLInputElement>(), valueButton = ref<HTMLButtonElement>(), group = ref<HTMLElement>()
let owner = '', original: HarnessLimit | undefined, revision: string | null = null
const mode = computed(() => limitMode(props.value))
const downDisabled = computed(() => props.value === 'off' || typeof props.value === 'number' && props.value <= 0)
const upDisabled = computed(() => props.total ? Number(props.value) >= CAP_MAX : mode.value === 'none')
const valueCopy = computed(() => props.total ? `Run up to ${props.value} at once` : mode.value === 'none' ? `no own limit · up to ${props.effective} now` : mode.value === 'off' ? 'off · no new starts' : `at most ${props.value}`)
const editTip = computed(() => `${props.label}: ${valueCopy.value}. Click to type${props.total ? ' 0 to 30.' : ': empty = no own limit, 0 = off, 1 to 30 = at most.'}`)
function destination(delta: number) {
  if (props.total) return delta < 0 ? 'One agent fewer at once' : 'One agent more at once'
  if (delta < 0 && downDisabled.value) return `${props.label}: already ${props.value === 'off' ? 'off' : 'at most 0'}`
  if (delta > 0 && upDisabled.value) return `${props.label}: already no own limit`
  const next = stepHarnessLimit(props.value, props.effective, delta)
  return `${props.label}: ${next === 'off' ? 'off · no new starts' : next === 'no_limit' ? 'no own limit' : `at most ${next}`}`
}
function keepFocus(button: Element | null) {
  void nextTick(() => {
    if (button instanceof HTMLButtonElement && button.isConnected && button.disabled && document.activeElement === button) {
      group.value?.querySelector<HTMLButtonElement>('.pm:not(:disabled)')?.focus()
    }
  })
}
function step(delta: number) {
  if (delta < 0 ? downDisabled.value : upDisabled.value) return
  const button = document.activeElement
  emit('step', delta); keepFocus(button)
}
function startEdit() {
  owner = props.viewer; original = props.value; revision = props.revision
  draft.value = typeof props.value === 'number' ? String(props.value) : props.value === 'off' ? '0' : ''
  editing.value = true
  void nextTick(() => { input.value?.focus(); input.value?.select() })
}
function finishEdit(cancel = false, restoreFocus = false) {
  if (!editing.value) return
  const current = owner === props.viewer && original === props.value && revision === props.revision
  const value = props.total ? typedTotal(draft.value) : typedLimit(draft.value)
  editing.value = false
  if (!cancel && current && value !== undefined) emit('edit', value)
  if (restoreFocus) void nextTick(() => valueButton.value?.focus())
}
function keys(event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.altKey || event.isComposing) return
  if (event.target instanceof HTMLInputElement) {
    if (event.key === 'Enter' || event.key === 'Escape') {
      event.preventDefault(); event.stopPropagation(); finishEdit(event.key === 'Escape', true)
    }
    return
  }
  if (!props.total && (event.key === 'Home' || event.key === 'End')) {
    event.preventDefault()
    const button = document.activeElement
    emit('boundary', event.key === 'Home' ? 'off' : 'none'); keepFocus(button)
    return
  }
  const delta = event.key === 'ArrowUp' || event.key === 'ArrowRight' ? 1 : event.key === 'ArrowDown' || event.key === 'ArrowLeft' ? -1 : 0
  if (delta) { event.preventDefault(); step(delta) }
}
// A draft belongs to the viewer and value on screen when editing started.
watch(() => [props.viewer, props.value, props.revision], () => { if (editing.value) finishEdit(true) }, { flush: 'sync' })
</script>

<template>
  <span ref="group" class="f-pm" :class="{ 'f-pm-total': total, [`is-${mode}`]: !total }" @keydown="keys">
    <button type="button" class="pm" :aria-label="destination(-1)" :data-tip="destination(-1)" :disabled="downDisabled" @click="step(-1)"><AppIcon name="minus" :size="total ? 12 : 10" /></button>
    <span class="value-slot" :class="total ? 'f-num' : 'f-n lim-num'">
      <input v-if="editing" ref="input" v-model="draft" class="f-edit" type="text" inputmode="numeric" maxlength="64" :placeholder="!total && mode === 'none' ? '∞' : undefined" :aria-label="`${label}: type ${total ? '0 to 30' : 'a limit; empty for no own limit, 0 for off'}`" @blur="finishEdit()">
      <button v-else ref="valueButton" type="button" class="value" :aria-label="editTip" :data-tip="editTip" @click="startEdit">
        <svg v-if="!total && mode === 'none'" width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M8 8c-1.5-2-2.5-3-4-3a3 3 0 0 0 0 6c1.5 0 2.5-1 4-3s2.5-3 4-3a3 3 0 0 1 0 6c-1.5 0-2.5-1-4-3Z" /></svg>
        <template v-else>{{ value === 'off' ? 'off' : value }}</template>
      </button>
    </span>
    <button type="button" class="pm" :aria-label="destination(1)" :data-tip="destination(1)" :disabled="upDisabled" @click="step(1)"><AppIcon name="plus" :size="total ? 12 : 10" /></button>
  </span>
</template>

<style scoped>
.f-pm { --pm-size: 22px; --value-width: 24px; --value-height: 26px; --value-size: 14px; display: inline-flex; align-items: center; flex: none; gap: 3px; padding: 0; }
.f-pm-total { --pm-size: 26px; --value-width: 30px; --value-height: 30px; --value-size: 19px; color: var(--teal-ink); }
.pm { display: grid; place-items: center; flex: none; width: var(--pm-size); height: var(--pm-size); padding: 0; border: 0; border-radius: 50%; background: var(--surface-raised); box-shadow: var(--shadow-btn); color: var(--teal-ink); cursor: pointer; }
.pm:disabled { opacity: .3; cursor: default; }
.value-slot { display: block; flex: none; width: var(--value-width); height: var(--value-height); color: var(--ink); font: 650 var(--value-size)/1 var(--font); font-variant-numeric: tabular-nums; }
.f-num { color: var(--teal-ink); font-weight: 700; }
.is-none .value-slot, .is-off .value-slot { color: var(--ink-3); font-weight: 500; }
.is-off .value-slot { font-size: 12.5px; }
.value, .f-edit { display: grid; place-items: center; box-sizing: border-box; width: 100%; height: 100%; padding: 0; border: 0; border-radius: 6px; background: transparent; color: inherit; font: inherit; text-align: center; font-variant-numeric: tabular-nums; }
.value { cursor: text; }
.f-edit { background: var(--field-bg); box-shadow: var(--field-inset), inset 0 0 0 1.5px var(--teal); }
.f-edit::placeholder { color: var(--ink-3); }
.pm:focus-visible, .value:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.f-edit:focus-visible { outline: none; }
@media (hover: hover) {
  .pm:hover:not(:disabled) { background: var(--row-hover); }
  .value:hover { background: var(--surface-2); box-shadow: inset 0 -1px 0 var(--line-2); }
}
@media (pointer: coarse) { .f-pm { --pm-size: 44px; --value-width: 44px; --value-height: 44px; } }
</style>
