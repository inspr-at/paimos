<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The lifeline strip on a ticket (design r4b §3.3, §5, §6; AEON-1359): one segment per status visit from the event
// log, in proportion to time and filled like its status icon; gate markers above it where the ticket entered a group,
// with the final (or current) status pinned at the right end; and the line of visits under it. Hover, the arrow keys
// or a tap show a segment's tip; markers show theirs on hover, focus or tap. Every part keeps its height while loading,
// on failure and when the figures land, and tips float on the body, so nothing below moves (AEON-541).
import { computed, nextTick, onBeforeUnmount, onBeforeUpdate, onMounted, ref, shallowRef, useId, watch } from 'vue'
import '../../styles/status-fills.css'
import StatusIcon from './StatusIcon.vue'
import { APIError } from '../../lib/api'
import { onAccessRevalidate } from '../../lib/authz'
import { deliveryLanguage } from '../../lib/delivery'
import { lifelineView, typicalsOf, type LifelineView, type TipLine } from '../../lib/lifelineView'
import { readLifeline, readTypicals } from '../../lib/statusTiming'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { MARKER_SIZE, placeMarkers, stripWidths } from '../../lib/workFigures'
import { workFiguresText } from '../../lib/workFiguresText'
import { useProfile } from '../../stores/profile'

const props = defineProps<{ nodeId: string; projectId: string; revision: string }>()
const profile = useProfile()
const lang = computed(() => deliveryLanguage(profile.profile?.locale))
const text = computed(() => workFiguresText(lang.value))
const uid = useId()

// ---------- Reads: the lifeline and the project's typicals together, so the "usually" notes land with the strip ----------
const scope = useIdentityScope(), reads = scope.lane()
const status = ref<'loading' | 'ready' | 'error' | 'forbidden'>('loading')
const answer = shallowRef<Answer | null>(null)
const keyOf = () => `${scope.owner.value}|${props.nodeId}|${props.revision}`
function load() {
  const node = props.nodeId, project = props.projectId, key = keyOf()
  const known = kept.get(key)
  if (known) { reads.cancel(); answer.value = known; status.value = 'ready'; return }
  if (answer.value?.lifeline.node_id !== node) { answer.value = null; status.value = 'loading' }
  void reads.run(({ after, signal }) => after(Promise.all([readLifeline(node, signal), readTypicals(scope.owner.value, project).catch(() => null)]), ([lifeline, timing]) => {
    if (node !== props.nodeId) return
    const next = { lifeline, timing }
    kept.set(key, next)
    if (kept.size > 24) kept.delete(kept.keys().next().value!)
    answer.value = next
    status.value = 'ready'
  }), { failed: error => {
    if (node !== props.nodeId) return
    answer.value = null
    status.value = error instanceof APIError && error.status === 403 ? 'forbidden' : 'error'
  } })
}
const view = computed<LifelineView | null>(() => answer.value ? lifelineView(answer.value.lifeline, typicalsOf(answer.value.timing?.steps), lang.value) : null)

// ---------- Layout: widths from the strip's measured width, markers on whole-pixel notches ----------
const strip = ref<HTMLElement>()
const width = ref(0)
let resize: ResizeObserver | null = null
// The live segment pauses off-screen and in a hidden tab.
const offscreen = ref(false), hiddenTab = ref(false)
let seen: IntersectionObserver | null = null
const visibility = () => { hiddenTab.value = document.hidden }
onMounted(() => {
  resize = new ResizeObserver(entries => { width.value = Math.floor(entries[0]?.contentRect.width ?? 0) })
  if (strip.value) { resize.observe(strip.value); width.value = Math.floor(strip.value.clientWidth) }
  if (typeof IntersectionObserver === 'function' && strip.value) {
    seen = new IntersectionObserver(entries => { offscreen.value = !(entries[entries.length - 1]?.isIntersecting ?? true) })
    seen.observe(strip.value)
  }
  visibility()
  document.addEventListener('visibilitychange', visibility)
  document.addEventListener('pointerdown', outside, true)
  window.addEventListener('scroll', scrolled, { passive: true, capture: true })
})
onBeforeUnmount(() => {
  resize?.disconnect(); seen?.disconnect()
  document.removeEventListener('visibilitychange', visibility)
  document.removeEventListener('pointerdown', outside, true)
  window.removeEventListener('scroll', scrolled, { capture: true })
})
const widths = computed(() => view.value && width.value ? stripWidths(view.value.segments.map(segment => segment.seconds), width.value) : [])
const lefts = computed(() => { let x = 0; return widths.value.map(w => { const left = x; x += w + 2; return left }) })
const places = computed(() => view.value && width.value ? placeMarkers(view.value.markerModels, widths.value, width.value) : [])

// ---------- Tips: one at a time, fixed on the body, opaque ----------
interface Tip { kind: 'strip' | 'gate'; anchor: HTMLElement; lines: TipLine[]; pinned: boolean; at: number }
const tip = shallowRef<Tip | null>(null)
const tipEl = ref<HTMLElement>()
const place = ref({ x: 0, y: 0, placed: false })
const selected = ref(-1)
function showTip(next: Omit<Tip, 'pinned'> & { pinned?: boolean }) {
  tip.value = { ...next, pinned: !!next.pinned }
  place.value = { x: 0, y: 0, placed: false }
  void nextTick(position)
}
function position() {
  const current = tip.value, el = tipEl.value
  if (!current || !el || !current.anchor.isConnected) return
  // A manual popover sits in the top layer, so the tip also shows over a modal dialog holding the ticket (Releases).
  if (typeof el.showPopover === 'function' && !el.matches(':popover-open')) el.showPopover()
  const rect = current.anchor.getBoundingClientRect(), w = el.offsetWidth, h = el.offsetHeight
  let y = rect.top - h - 8
  if (y < 8) y = rect.bottom + 8
  place.value = { x: Math.round(Math.max(8, Math.min(window.innerWidth - w - 8, rect.left + rect.width / 2 - w / 2))), y: Math.round(y), placed: true }
}
function hideTip() { tip.value = null; selected.value = -1 }
function outside(event: PointerEvent) {
  const target = event.target as Node
  if (!tip.value?.pinned || strip.value?.contains(target) || gatesEl.value?.contains(target) || tipEl.value?.contains(target)) return
  hideTip()
}
function scrolled() { if (tip.value) hideTip() }

// Template refs, collected again on every render, so a shorter history never keeps a removed element.
let segmentEls: HTMLElement[] = [], markerEls: HTMLElement[] = []
onBeforeUpdate(() => { segmentEls = []; markerEls = [] })
const setSegment = (el: unknown, i: number) => { if (el instanceof HTMLElement) segmentEls[i] = el }
const setMarker = (el: unknown, k: number) => { if (el instanceof HTMLElement) markerEls[k] = el }
function select(index: number, pinned = false) {
  const segments = view.value?.segments ?? []
  if (!segments.length) return
  const i = Math.max(0, Math.min(segments.length - 1, index))
  const anchor = segmentEls[i]
  if (!anchor) return
  selected.value = i
  showTip({ kind: 'strip', anchor, lines: segments[i].tip, pinned, at: i })
}
function segmentAt(clientX: number): number {
  let best = 0, distance = Infinity
  segmentEls.forEach((el, k) => {
    const r = el.getBoundingClientRect(), d = clientX < r.left ? r.left - clientX : clientX > r.right ? clientX - r.right : 0
    if (d < distance) { distance = d; best = k }
  })
  return best
}
function stripMove(event: PointerEvent) {
  if (event.pointerType !== 'mouse' || !view.value?.segments.length) return
  const i = segmentAt(event.clientX)
  if (i !== selected.value || tip.value?.kind !== 'strip') select(i)
}
function stripLeave(event: PointerEvent) {
  if (event.pointerType === 'mouse' && document.activeElement !== strip.value && tip.value?.kind === 'strip' && !tip.value.pinned) hideTip()
}
function stripDown(event: PointerEvent) {
  if (event.pointerType === 'mouse' || !view.value?.segments.length) return
  const i = segmentAt(event.clientX)
  if (i === selected.value && tip.value?.pinned) hideTip()
  else select(i, true)
}
function stripFocus() {
  if (strip.value?.matches(':focus-visible') && view.value?.segments.length) select(selected.value < 0 ? view.value.segments.length - 1 : selected.value)
}
function stripBlur() { if (tip.value?.kind === 'strip' && !tip.value.pinned) hideTip() }
function stripKey(event: KeyboardEvent) {
  if (event.key === 'Escape' && tip.value) { event.preventDefault(); event.stopPropagation(); hideTip(); return }
  if (event.altKey || event.ctrlKey || event.metaKey || !view.value?.segments.length) return
  const last = view.value.segments.length - 1, current = selected.value < 0 ? last : selected.value
  const next = { ArrowLeft: current - 1, ArrowRight: current + 1, Home: 0, End: last }[event.key]
  if (next === undefined) return
  event.preventDefault()
  select(next)
}

// ---------- Gate markers: hover (immediate), focus, or a tap anywhere in the row picks the nearest ----------
const gatesEl = ref<HTMLElement>()
function showMarker(k: number, pinned = false) {
  const marker = view.value?.markers[k], anchor = markerEls[k]
  if (!marker || !anchor) return
  selected.value = -1
  showTip({ kind: 'gate', anchor, lines: [{ text: marker.text, tone: 'plain' }], pinned, at: k })
}
function markerEnter(event: PointerEvent, k: number) { if (event.pointerType === 'mouse' && !tip.value?.pinned) showMarker(k) }
function markerLeave(event: PointerEvent) { if (event.pointerType === 'mouse' && tip.value?.kind === 'gate' && !tip.value.pinned) hideTip() }
function markerFocus(event: FocusEvent, k: number) { if ((event.target as HTMLElement).matches(':focus-visible')) showMarker(k) }
function markerBlur(k: number) { if (tip.value?.kind === 'gate' && tip.value.at === k && !tip.value.pinned) hideTip() }
function markerKey(event: KeyboardEvent) { if (event.key === 'Escape' && tip.value) { event.preventDefault(); event.stopPropagation(); hideTip() } }
function rowDown(event: PointerEvent) {
  if (event.pointerType === 'mouse' || !markerEls.length) return
  let best = 0, distance = Infinity
  markerEls.forEach((el, k) => { const r = el.getBoundingClientRect(), d = Math.abs(event.clientX - (r.left + r.width / 2)); if (d < distance) { distance = d; best = k } })
  event.preventDefault()
  if (tip.value?.kind === 'gate' && tip.value.at === best && tip.value.pinned) hideTip()
  else showMarker(best, true)
}

const live = computed(() => tip.value?.kind === 'strip' ? tip.value.lines.map(line => line.text).join('. ') : '')
const lineId = `${uid}-line`

// Last, so the first run finds every ref it touches. Another ticket, revision or person closes the tip and reads again.
watch(() => [props.nodeId, props.revision, scope.owner.value], () => { hideTip(); load() }, { immediate: true })
// A permission reset drops the read in flight with the scope; ask again in the new one.
onBeforeUnmount(onAccessRevalidate({ reset: () => { if (status.value === 'loading') load() } }))
</script>

<script lang="ts">
import type { TicketLifeline as Lifeline, TicketStatusTiming as Timing } from '../../lib/statusTiming'
type Answer = { lifeline: Lifeline; timing: Timing | null }
// Answers kept per person, ticket and revision, across sheets: reopening a ticket lands at once.
const kept = new Map<string, Answer>()
</script>

<template>
  <section class="life" :aria-label="text.caption" :aria-busy="status === 'loading' || undefined" data-testid="ticket-lifeline">
    <div class="life-cap">
      <span>{{ text.caption }}</span>
      <span v-if="view" class="life-total">
        <template v-if="view.caption.label">{{ view.caption.label }} <b>{{ view.caption.value }}</b></template>
        <template v-else><b>{{ view.caption.value }}</b> {{ view.caption.after }}</template>
      </span>
    </div>
    <div ref="gatesEl" class="gates" role="group" :aria-label="text.gatesLabel" @pointerdown="rowDown">
      <template v-if="view && width">
        <template v-for="(marker, k) in view.markers" :key="`${marker.kind}-${marker.index}-${k}`">
          <button :ref="el => setMarker(el, k)" type="button" class="gate" :class="{ final: marker.kind !== 'gate', 'tip-open': tip?.kind === 'gate' && tip.at === k }"
            :data-gi="k" :data-kind="marker.kind" :data-displaced="places[k]?.displaced || undefined" :style="{ left: `${places[k]?.left ?? 0}px`, width: `${MARKER_SIZE}px`, height: `${MARKER_SIZE}px` }"
            :aria-label="marker.text" @pointerenter="markerEnter($event, k)" @pointerleave="markerLeave" @focus="markerFocus($event, k)" @blur="markerBlur(k)" @keydown="markerKey">
            <StatusIcon :state="marker.icon" :size="13" />
          </button>
          <i class="gate-notch" :class="{ now: marker.kind === 'now' }" :data-gi="k" :style="{ left: `${places[k]?.notch ?? 0}px` }" aria-hidden="true" />
        </template>
      </template>
    </div>
    <div ref="strip" class="strip f-slide-hover" :tabindex="view?.segments.length ? 0 : -1" role="group" aria-roledescription="lifeline" :aria-label="text.stripLabel" :aria-describedby="lineId"
      @pointermove="stripMove" @pointerleave="stripLeave" @pointerdown="stripDown" @focus="stripFocus" @blur="stripBlur" @keydown="stripKey">
      <span v-if="status === 'loading'" class="sk strip-sk" aria-hidden="true" />
      <template v-else-if="view && width">
        <i v-for="(segment, i) in view.segments" :key="segment.index" :ref="el => setSegment(el, i)"
          class="sg" :class="[segment.fill, { 'f-now': segment.now, offscreen: segment.now && (offscreen || hiddenTab), 'f-short': (widths[i] ?? 0) < 10, sel: selected === i && tip?.kind === 'strip' }]"
          :data-i="i" :data-s="segment.status" :style="{ left: `${lefts[i] ?? 0}px`, width: `${widths[i] ?? 0}px` }" />
      </template>
    </div>
    <p :id="lineId" class="life-line">
      <template v-if="view">
        <template v-for="(item, i) in view.items" :key="i"><span v-if="i" class="sep"> · </span><span class="it">{{ item.label }}<template v-if="item.value"> <b>{{ item.value }}</b></template><template v-if="item.soFar"> {{ text.soFar }}</template><span v-if="item.usual" class="usual" :class="{ attn: item.usual.attn }">{{ item.usual.text }}</span></span></template>
        <template v-if="view.partly"><span class="sep"> · </span><span class="it partly">{{ text.partly }}</span></template>
      </template>
      <template v-else-if="status === 'error'">{{ text.failed }} <button type="button" class="retry" @click="load">{{ text.retry }}</button></template>
      <template v-else-if="status === 'forbidden'">{{ text.forbidden }}</template>
      <span v-else class="sk line-sk" aria-hidden="true" />
    </p>
    <span class="sr-only" aria-live="polite">{{ live }}</span>
    <Teleport to="body">
      <div v-if="tip" ref="tipEl" popover="manual" class="lf-tip" :class="{ gate: tip.kind === 'gate', pinned: tip.pinned }" role="tooltip"
        :style="{ left: `${place.x}px`, top: `${place.y}px`, visibility: place.placed ? undefined : 'hidden' }">
        <p v-for="(line, i) in tip.lines" :key="i" :class="line.tone">{{ line.text }}</p>
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.life { margin-top: 16px; }
.life-cap { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; height: 18px; font-size: 12.5px; line-height: 18px; color: var(--ink-3); white-space: nowrap; }
.life-cap b { font-weight: 600; color: var(--ink); font-variant-numeric: tabular-nums; }
/* Gate markers: a fixed row above the strip; the group icon where the ticket entered a group, a 1 px notch at the
   exact crossing. Markers never overlap (a cluster sits side by side), and the first and last overhang the strip. */
.gates { position: relative; height: 18px; margin-top: 4px; }
.gate { position: absolute; top: 0; display: grid; place-items: center; margin: 0; padding: 0; border: 0; border-radius: 4px; background: transparent; cursor: default; z-index: 1; }
.gate:hover, .gate.tip-open { background: var(--row-hover); }
.gate:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 1px; box-shadow: none; }
.gate-notch { position: absolute; bottom: 0; width: 1px; height: 3px; background: var(--ink-3); }
.gate-notch.now { background: var(--ink-2); }
.strip { position: relative; height: 28px; margin: 2px 0 0; border-radius: 6px; touch-action: pan-y; outline: none; }
.strip:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; box-shadow: none; }
.sg { position: absolute; top: 9px; height: 10px; border-radius: 3px; }
.sg.sel { outline: 1.5px solid var(--ink-2); outline-offset: 2px; }
/* An open ticket ends in a live "now" segment with a fading end. */
.sg.f-now { -webkit-mask-image: linear-gradient(90deg, #000 calc(100% - 10px), rgba(0, 0, 0, .35)); mask-image: linear-gradient(90deg, #000 calc(100% - 10px), rgba(0, 0, 0, .35)); }
.life-line { min-height: calc(12.5px * 1.55); margin: 2px 0 0; font-size: 12.5px; line-height: 1.55; color: var(--ink-2); }
.life-line .it { white-space: nowrap; }
.life-line .it b { font-weight: 600; color: var(--ink); font-variant-numeric: tabular-nums; }
.life-line .usual { color: var(--ink-3); }
.life-line .usual.attn { color: var(--warn-ink); font-weight: 600; }
.life-line .sep, .life-line .partly { color: var(--ink-3); }
.retry { display: inline; height: auto; margin: 0; padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; text-decoration: underline; text-underline-offset: 2px; }
.retry:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; border-radius: 3px; }
.retry { position: relative; }
@media (pointer: coarse) { .retry::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: 44px; transform: translate(-50%, -50%); } }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
.strip-sk { position: absolute; top: 9px; left: 0; right: 0; height: 10px; border-radius: 3px; }
.line-sk { display: inline-block; width: 60%; height: 10px; vertical-align: middle; }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: lf-sk 1.4s ease-in-out infinite; }
}
@keyframes lf-sk { to { background-position: -200% 0; } }
.sr-only { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; border: 0; }
@media (max-width: 720px), (pointer: coarse) {
  /* The strip's 44 px reach tucks under the marker row, so a notch still sits right above its crossing. */
  .gates { height: 22px; z-index: 2; }
  .gates + .strip { margin-top: -12px; }
  .gate { top: 2px; }
  .gate::before { content: ''; position: absolute; top: 50%; left: 50%; width: 44px; height: 44px; transform: translate(-50%, -50%); }
  /* The end markers' reach grows inwards, so it never pushes past the sheet's padding. */
  .gate[data-gi="0"]::before { left: 0; transform: translateY(-50%); }
  .gate.final::before { left: auto; right: 0; transform: translateY(-50%); }
  .strip { height: 44px; }
  .sg, .strip-sk { top: 17px; }
}
</style>

<style>
/* The tips live on body: fixed and opaque, so they never move the page or let it show through (AEON-541, r2 fix 1). */
.lf-tip { position: fixed; z-index: 80; inset: auto; width: max-content; max-width: min(320px, calc(100vw - 16px)); margin: 0; padding: 5px 10px; border: 0; overflow: visible; border-radius: 8px; background: var(--tip-solid); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4; box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px color-mix(in srgb, var(--shadow-black) 50%, transparent); pointer-events: none; }
.lf-tip.pinned { pointer-events: auto; }
.lf-tip.gate { max-width: calc(100vw - 16px); white-space: nowrap; }
.lf-tip p { margin: 0; color: inherit; }
.lf-tip p + p { margin-top: 1px; }
.lf-tip .title { font-weight: 650; }
.lf-tip .muted { color: var(--tip-muted); }
.lf-tip .attn { color: var(--tip-attn); }
</style>
