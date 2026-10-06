<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { CAP_MAX, limitMode, stepHarnessLimit, typedLimit, typedTotal, type HarnessLimit } from '../../lib/agentsWorking'
import { startHold } from '../../lib/pressHold'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ value: HarnessLimit | undefined; effective: number; label: string; viewer: string; revision: string | null; total?: boolean }>()
// hold: true while − or + is held (pointer or arrow key); the parent saves only once it is false again.
const emit = defineEmits<{ step: [delta: number]; edit: [value: HarnessLimit]; boundary: [mode: 'off' | 'none']; hold: [active: boolean] }>()
const editing = ref(false), draft = ref(''), input = ref<HTMLInputElement>(), valueButton = ref<HTMLButtonElement>(), group = ref<HTMLElement>()
let owner = '', original: HarnessLimit | undefined, revision: string | null = null
const mode = computed(() => limitMode(props.value))
const downDisabled = computed(() => props.value === 'off' || typeof props.value === 'number' && props.value <= 0)
const upDisabled = computed(() => props.total ? Number(props.value) >= CAP_MAX : mode.value === 'none')
const valueCopy = computed(() => props.total ? `Run up to ${props.value} at once` : mode.value === 'none' ? `no own limit · up to ${props.effective} now` : mode.value === 'off' ? 'off · no new starts' : `at most ${props.value}`)
const editTip = computed(() => `${props.label}: ${valueCopy.value}. Click to type${props.total ? ' 0 to 30.' : ': empty = no own limit, 0 = off, 1 to 30 = at most.'}`)
// Only the value a hold or tap ends on is announced, never each step.
const announced = ref('')
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
const ended = (delta: number) => delta < 0 ? downDisabled.value : upDisabled.value
function step(delta: number) {
  if (ended(delta)) return
  const button = document.activeElement
  emit('step', delta); keepFocus(button)
}
// A repeat stops at a ladder end, and pauses before crossing into off or no own
// limit: a long hold never lands in a mode by accident; crossing needs a fresh press.
function canRepeat(delta: number) {
  if (ended(delta)) return false
  if (props.total) return true
  const next = stepHarnessLimit(props.value, props.effective, delta)
  return next !== 'off' && next !== 'no_limit'
}
let held: { delta: number; key?: string; stop: () => void } | null = null
// Repeating may pause at a boundary while the button is still held; the hold itself
// (and with it the save) ends only on release.
function press(delta: number, key?: string) {
  release()
  if (ended(delta)) return
  emit('hold', true)
  const hold = { delta, key, stop: () => {} }
  held = hold
  window.addEventListener('blur', release)
  hold.stop = startHold(() => step(delta), () => canRepeat(delta), () => {})
}
function release() {
  const hold = held
  if (!hold) return
  held = null
  hold.stop()
  window.removeEventListener('blur', release)
  emit('hold', false)
  void nextTick(() => { announced.value = valueCopy.value })
}
// The button that runs out of steps ends the hold and hands focus to its sibling.
watch([downDisabled, upDisabled], () => { if (held && ended(held.delta)) { const button = document.activeElement; release(); keepFocus(button) } })
let pointerPressed = false
function pointerDown(event: PointerEvent, delta: number) {
  if (event.button !== 0 || event.ctrlKey || event.metaKey) return
  const button = event.currentTarget as HTMLElement
  // Touch captures the pointer implicitly; release it so leaving the button ends the hold.
  if (button.hasPointerCapture?.(event.pointerId)) button.releasePointerCapture(event.pointerId)
  pointerPressed = true
  // Focus here instead of the browser's mousedown, which would land on a button
  // this very press may disable; one that runs out then hands focus to its sibling.
  // Also keeps a long press from selecting text.
  event.preventDefault()
  button.focus({ preventScroll: true })
  press(delta)
}
// A pointer press has already stepped; a click without one (keyboard, assistive tech) steps once.
function click(event: MouseEvent, delta: number) {
  const stepped = pointerPressed && event.detail > 0
  pointerPressed = false
  if (!stepped) { press(delta); release() }
}
function focusOut(event: FocusEvent) {
  if (!(event.relatedTarget instanceof Node && group.value?.contains(event.relatedTarget))) release()
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
const arrow = (key: string) => key === 'ArrowUp' || key === 'ArrowRight' ? 1 : key === 'ArrowDown' || key === 'ArrowLeft' ? -1 : 0
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
  const delta = arrow(event.key)
  if (!delta) return
  event.preventDefault()
  // The hold keeps its own pace; the OS key repeat is ignored, also after a hold paused at a boundary.
  if (event.repeat) return
  press(delta, event.key)
}
function keyUp(event: KeyboardEvent) { if (held?.key && held.key === event.key) release() }
// A draft belongs to the viewer and value on screen when editing started.
watch(() => [props.viewer, props.value, props.revision], () => { if (editing.value) finishEdit(true) }, { flush: 'sync' })
// A changed viewer never inherits a hold.
watch(() => props.viewer, release, { flush: 'sync' })
onBeforeUnmount(release)
</script>

<template>
  <span ref="group" class="f-pm" :class="{ 'f-pm-total': total, [`is-${mode}`]: !total }" @keydown="keys" @keyup="keyUp" @focusout="focusOut">
    <button type="button" class="pm dec" :aria-label="destination(-1)" :data-tip="destination(-1)" :disabled="downDisabled" @pointerdown="pointerDown($event, -1)" @pointerup="release" @pointercancel="release" @pointerleave="release" @contextmenu.prevent @click="click($event, -1)"><AppIcon name="minus" :size="total ? 12 : 10" /></button>
    <span class="value-slot" :class="total ? 'f-num' : 'f-n lim-num'">
      <input v-if="editing" ref="input" v-model="draft" class="f-edit" type="text" inputmode="numeric" maxlength="64" :placeholder="!total && mode === 'none' ? '∞' : undefined" :aria-label="`${label}: type ${total ? '0 to 30' : 'a limit; empty for no own limit, 0 for off'}`" @blur="finishEdit()">
      <button v-else ref="valueButton" type="button" class="value" :aria-label="editTip" :data-tip="editTip" @click="startEdit">
        <svg v-if="!total && mode === 'none'" width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M8 8c-1.5-2-2.5-3-4-3a3 3 0 0 0 0 6c1.5 0 2.5-1 4-3s2.5-3 4-3a3 3 0 0 1 0 6c-1.5 0-2.5-1-4-3Z" /></svg>
        <template v-else>{{ value === 'off' ? 'off' : value }}</template>
      </button>
    </span>
    <button type="button" class="pm inc" :aria-label="destination(1)" :data-tip="destination(1)" :disabled="upDisabled" @pointerdown="pointerDown($event, 1)" @pointerup="release" @pointercancel="release" @pointerleave="release" @contextmenu.prevent @click="click($event, 1)"><AppIcon name="plus" :size="total ? 12 : 10" /></button>
    <span class="sr-only" aria-live="polite">{{ announced }}</span>
  </span>
</template>

<style scoped>
.f-pm { --pm-size: 22px; --value-width: 24px; --value-height: 26px; --value-size: 14px; display: inline-flex; align-items: center; flex: none; gap: 3px; padding: 0; }
.f-pm-total { --pm-size: 26px; --value-width: 30px; --value-height: 30px; --value-size: 19px; color: var(--teal-ink); }
.pm { display: grid; place-items: center; flex: none; width: var(--pm-size); height: var(--pm-size); padding: 0; border: 0; border-radius: 50%; background: var(--surface-raised); box-shadow: var(--shadow-btn); color: var(--teal-ink); cursor: pointer; transition: background .15s ease; }
.pm:disabled { opacity: .3; cursor: default; }
/* Holding is a gesture: no callout, no selection, no double-tap zoom. */
.pm { touch-action: manipulation; user-select: none; -webkit-user-select: none; -webkit-touch-callout: none; }
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
@media (prefers-reduced-motion: reduce) { .pm { transition: none; } }
</style>
