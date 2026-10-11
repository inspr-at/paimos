<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { CAP_MAX, limitMode, stepHarnessLimit, typedLimit, typedTotal, type HarnessLimit } from '../../lib/agentsWorking'
import { startHold } from '../../lib/pressHold'
import AppIcon from '../AppIcon.vue'

// interrupt changes when a save conflicts: an active hold ends and needs a fresh press.
// unit (AEON-1209): the light dial unit of the capacity row. The mark and flag slots sit
// beside the number; − and + are muted, keep their room and show on hover or focus.
const props = defineProps<{ value: HarnessLimit | undefined; effective: number; label: string; viewer: string; revision: string | null; total?: boolean; interrupt?: number; unit?: boolean }>()
// hold: true while − or + is held (pointer or arrow key); the parent saves only once it is false again.
// face: a click on the unit outside its buttons (a touch tap there opens a tight row's plate).
const emit = defineEmits<{ step: [delta: number]; edit: [value: HarnessLimit]; boundary: [mode: 'off' | 'none']; hold: [active: boolean]; face: [] }>()
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
// A changed viewer never inherits a hold, and a conflict ends one.
watch([() => props.viewer, () => props.interrupt], release, { flush: 'sync' })
onBeforeUnmount(release)
function faceClick(event: MouseEvent) {
  if (props.unit && !(event.target instanceof Element && event.target.closest('button, input'))) emit('face')
}
// A tight row's plate opens toward the room it has (AEON-1209): − and + at its ends, or both after
// (or before) the face at a line's edge, so neither is clipped. Placed as the plate opens; the row's
// layout never changes under it, so nothing moves while it is open.
const side = ref<'both' | 'start' | 'end'>('both')
function place() {
  const el = group.value
  if (!props.unit || !el) return
  let clip = el.parentElement
  while (clip && clip.parentElement && getComputedStyle(clip).overflowX === 'visible') clip = clip.parentElement
  if (!clip) return
  const box = el.getBoundingClientRect(), edge = clip.getBoundingClientRect(), style = getComputedStyle(el)
  const px = (name: string) => parseFloat(style.getPropertyValue(name)) || 0
  const reach = px('--pm-w') + px('--pm-gap') + px('--plate-pad'), left = box.left - edge.left, right = edge.right - box.right
  side.value = left >= reach && right >= reach ? 'both' : right >= left ? 'end' : 'start'
}
</script>

<template>
  <span ref="group" class="f-pm" :class="{ 'f-pm-total': total, [`is-${mode}`]: !total, du: unit }" :data-side="unit ? side : undefined" @keydown="keys" @keyup="keyUp" @focusout="focusOut" @pointerenter="place" @pointerdown.capture="place" @focusin="place" @click="faceClick">
    <button type="button" class="pm dec" :class="{ 'dv dm': unit }" :aria-label="destination(-1)" :data-tip="destination(-1)" :disabled="downDisabled" @pointerdown="pointerDown($event, -1)" @pointerup="release" @pointercancel="release" @pointerleave="release" @contextmenu.prevent @click="click($event, -1)"><AppIcon name="minus" :size="unit ? 11 : total ? 12 : 10" /></button>
    <span v-if="unit" class="du-mark"><slot name="mark" /></span>
    <span class="value-slot" :class="total ? 'f-num' : 'f-n lim-num'">
      <input v-if="editing" ref="input" v-model="draft" class="f-edit" type="text" inputmode="numeric" maxlength="64" :placeholder="!total && mode === 'none' ? '∞' : undefined" :aria-label="`${label}: type ${total ? '0 to 30' : 'a limit; empty for no own limit, 0 for off'}`" @blur="finishEdit()">
      <button v-else ref="valueButton" type="button" class="value" :aria-label="editTip" :data-tip="editTip" @click="startEdit">
        <svg v-if="!total && mode === 'none'" width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M8 8c-1.5-2-2.5-3-4-3a3 3 0 0 0 0 6c1.5 0 2.5-1 4-3s2.5-3 4-3a3 3 0 0 1 0 6c-1.5 0-2.5-1-4-3Z" /></svg>
        <template v-else>{{ value === 'off' ? 'off' : value }}</template>
      </button>
    </span>
    <slot v-if="unit" name="flag" />
    <button type="button" class="pm inc" :class="{ 'dv dp': unit }" :aria-label="destination(1)" :data-tip="destination(1)" :disabled="upDisabled" @pointerdown="pointerDown($event, 1)" @pointerup="release" @pointercancel="release" @pointerleave="release" @contextmenu.prevent @click="click($event, 1)"><AppIcon name="plus" :size="unit ? 11 : total ? 12 : 10" /></button>
    <span class="sr-only" aria-live="polite">{{ announced }}</span>
  </span>
</template>

<style scoped>
.f-pm { --pm-size: 22px; --value-width: 24px; --value-height: 26px; --value-size: 14px; display: inline-flex; align-items: center; flex: none; gap: 3px; padding: 0; }
.f-pm-total { --pm-size: 26px; --value-width: 30px; --value-height: 30px; --value-size: 19px; color: var(--teal-ink); }
.pm { display: grid; place-items: center; flex: none; width: var(--pm-size); height: var(--pm-size); padding: 0; border: 0; border-radius: 50%; background: var(--surface-raised); box-shadow: var(--shadow-btn); color: var(--teal-ink); cursor: pointer; transition: background .15s ease; }
.pm:disabled { opacity: .3; cursor: default; }
/* Holding is a gesture: no callout, no selection, no double-tap zoom. */
.pm { touch-action: manipulation; -webkit-user-select: none; user-select: none; -webkit-touch-callout: none; }
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

/* The light dial unit (AEON-1209, design AEON-1196 r7): no outline and no fill; the mark and
   its number sit 4 px apart and bind the unit by proximity. − and + are muted, keep their room
   and show on hover or focus, so nothing moves. The number keeps a fixed slot, so a step never
   moves a neighbour. --pm-w, --pm-gap and --plate-pad place − / + and the plate in a tight row. */
.f-pm.du { --pm-size: 20px; --pm-w: 20px; --pm-gap: 1px; --plate-pad: 4px; --value-width: 22px; --value-height: 28px; --value-size: 13.5px; position: relative; height: 28px; gap: 0; border-radius: 999px; color: var(--ink); }
.du .du-mark { position: relative; z-index: 3; display: inline-grid; place-items: center; flex: none; margin: 0 4px 0 2px; }
.du .value-slot, .du .f-num { position: relative; z-index: 3; color: var(--ink); font-weight: 650; }
.du.is-none .value-slot, .du.is-off .value-slot { color: var(--ink-2); font-weight: 600; }
.du .value { place-items: center start; padding: 0 1px; }
.du .pm { position: relative; z-index: 4; background: transparent; box-shadow: none; color: var(--ink-3); opacity: 0; transition: opacity .15s ease, background-color .15s ease; }
.du .pm:disabled { opacity: 0; color: color-mix(in srgb, var(--ink-3) 45%, transparent); }
.du:is(:focus-within, .open) .pm { opacity: 1; }
@media (hover: hover) {
  .du:hover .pm { opacity: 1; }
  .du .pm:hover:not(:disabled) { background: var(--seg-on); color: var(--teal-ink); }
}
/* Touch: −, the number and + each own a distinct 44 px target, side by side, never overlapping.
   − and + are 44 px wide themselves (the wide row always shows them); the number's target spans
   its mark (22 px) and itself, 44 px tall. */
@media (pointer: coarse) {
  .f-pm.du { --pm-w: 44px; --pm-gap: 0px; --plate-pad: 0px; height: 36px; }
  .du .pm { width: var(--pm-w); height: 36px; border-radius: 999px; }
  .du .pm, .du .pm:disabled { opacity: 1; }
  .du .pm::after, .du .value::after { content: ''; position: absolute; }
  .du .pm::after { inset: -4px 0; }
  .du .value { position: relative; }
  .du .value::after { inset: -8px 0 -8px -22px; }
}
/* A tight capacity row (under 900 px) shows only the units. Hover, focus or a tap lifts THAT
   unit onto a light plate above its neighbours, with − and + at its ends; nothing beside it
   moves (AEON-541). The plate floats on a shadow, never an outline. At a line's edge, where an
   end has no room, both sit on the side that has it (data-side, set as the plate opens). */
@container cap (max-width: 899px) {
  .du .pm, .du .pm:disabled { position: absolute; top: 50%; translate: 0 -50%; opacity: 0; pointer-events: none; }
  .du .dec { left: calc(-1 * (var(--pm-w) + var(--pm-gap))); }
  .du .inc { right: calc(-1 * (var(--pm-w) + var(--pm-gap))); }
  .du[data-side="end"] .dec { left: calc(100% + var(--pm-gap)); }
  .du[data-side="end"] .inc { right: calc(-2 * (var(--pm-w) + var(--pm-gap))); }
  .du[data-side="start"] .dec { left: calc(-2 * (var(--pm-w) + var(--pm-gap))); }
  .du[data-side="start"] .inc { right: calc(100% + var(--pm-gap)); }
  .du::before { content: ''; position: absolute; z-index: 2; inset: -4px calc(-1 * (var(--pm-w) + var(--pm-gap) + var(--plate-pad))); border-radius: 999px; background: var(--surface-raised); box-shadow: var(--shadow-pop); opacity: 0; transform: scale(.9); transition: opacity .15s ease, transform .15s ease; pointer-events: none; }
  .du[data-side="end"]::before { inset: -4px calc(-2 * (var(--pm-w) + var(--pm-gap)) - var(--plate-pad)) -4px -4px; }
  .du[data-side="start"]::before { inset: -4px -4px -4px calc(-2 * (var(--pm-w) + var(--pm-gap)) - var(--plate-pad)); }
  .du:is(:focus-within, .open) { z-index: 6; }
  .du:is(:focus-within, .open)::before { opacity: 1; transform: none; }
  .du:is(:focus-within, .open) .pm { opacity: 1; pointer-events: auto; }
  .du:is(:focus-within, .open) .pm:disabled { opacity: .45; }
}
@media (hover: hover) {
  @container cap (max-width: 899px) {
    .du:hover { z-index: 6; }
    .du:hover::before { opacity: 1; transform: none; }
    .du:hover .pm { opacity: 1; pointer-events: auto; }
    .du:hover .pm:disabled { opacity: .45; }
  }
}
/* Touch in a tight row: closed, the whole unit is one target and a tap opens its plate; open,
   the plate's −, number and + take over. */
@media (pointer: coarse) {
  @container cap (max-width: 899px) {
    .du:not(.open, :focus-within) .value-slot { pointer-events: none; }
  }
}
@media (prefers-reduced-motion: reduce) { .du .pm, .du::before { transition: none; } }
</style>
