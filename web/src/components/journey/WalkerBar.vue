<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { hours, nextPickLabel, selectionOf, type TicketGroup, type WalkerTicket } from '../../lib/journey'
import AppIcon from '../AppIcon.vue'
import FeatureCheck from './FeatureCheck.vue'

// The walker's header (A3): the release eyebrow and "x of y", then one bar of
// features (a gold hairline between them) with their tickets as chips. A feature
// line carries its tri-state box, its name (jumps to its first ticket) and, on
// the right, the epic key: the only link, opening the epic in a new window. The
// chip's checkbox shows only over its slot. The bar drags sideways with a grab
// cursor, fades at its edges and shows a position thumb while it moves.
const props = defineProps<{
  groups: TicketGroup[]; order: WalkerTicket[]; index: number; included: Set<string>; memory: Map<string, string[]>
  editable: boolean; releaseLabel: string; projectKey: string; details: boolean; stateOf: (ticket: WalkerTicket) => string
}>()
const emit = defineEmits<{ go: [index: number]; step: [delta: number]; feature: [delta: number]; toggle: [id: string]; toggleFeature: [id: string]; search: []; details: []; sheet: []; close: [] }>()

const nav = ref<HTMLElement>()
const thumb = ref<HTMLElement>()
const overflow = ref(false)
const fadeLeft = ref(false)
const fadeRight = ref(false)
const dragging = ref(false)
const current = computed(() => props.order[props.index])
const inRelease = computed(() => props.order.filter(t => props.included.has(t.ticket_node_id)).length)
const indexOf = (ticket: WalkerTicket) => props.order.indexOf(ticket)
const featureNumber = (group: TicketGroup) => props.groups.filter(g => g.feature).indexOf(group) + 1
const shortTitle = (title: string) => title.length <= 26 ? title : `${title.slice(0, 26).replace(/\s+\S*$/, '')}…`
const epicHref = (key: string) => `/p/${encodeURIComponent(props.projectKey)}/${encodeURIComponent(key)}`
// Room for the feature line: box, number and name, and the epic key.
function lineWidth(group: TicketGroup) {
  const name = group.feature ? `F${featureNumber(group)} ${group.feature.title}` : 'No feature'
  const width = (props.editable && group.feature ? 24 : 0) + Math.min(name.length, 34) * 7.2 + 16 + (group.feature ? group.feature.key.length * 7 + 30 : 0)
  return `${Math.round(Math.max(116, Math.min(360, width)))}px`
}
function tip(ticket: WalkerTicket) {
  const where = props.included.has(ticket.ticket_node_id) ? props.releaseLabel.toLowerCase() : 'backlog'
  return `${ticket.key} · ${ticket.title}${ticket.estimated_hours != null ? ` · ${hours(ticket.estimated_hours)}` : ''} · ${where}`
}
function checkLabel(ticket: WalkerTicket) {
  return props.included.has(ticket.ticket_node_id) ? `${ticket.key} is in ${props.releaseLabel}, click to defer it to the backlog` : `${ticket.key} is in the backlog, click to include it in ${props.releaseLabel}`
}

// ---------- Overflow, fades, epic keys and the position thumb ----------
let thumbTimer: ReturnType<typeof setTimeout> | undefined
function measure() {
  const el = nav.value
  if (!el) return
  const max = el.scrollWidth - el.clientWidth
  overflow.value = max > 2
  fadeLeft.value = overflow.value && el.scrollLeft > 4
  fadeRight.value = overflow.value && el.scrollLeft < max - 4
  // An epic key steps aside when it would touch its feature's name or the bar's edges.
  const bar = el.getBoundingClientRect()
  for (const line of el.querySelectorAll<HTMLElement>('.fl')) {
    const key = line.querySelector<HTMLElement>('.ekey'), label = line.querySelector<HTMLElement>('.flt')
    if (!key || !label) continue
    const k = key.getBoundingClientRect(), l = label.getBoundingClientRect()
    key.classList.toggle('gone', k.left < l.right + 12 || k.left < bar.left + 44 || k.right > bar.right - 40)
  }
}
function showThumb() {
  const el = nav.value, t = thumb.value
  if (!el || !t || !overflow.value) return
  const width = Math.max(28, el.clientWidth * el.clientWidth / el.scrollWidth)
  const x = (el.clientWidth - width) * (el.scrollLeft / (el.scrollWidth - el.clientWidth))
  t.style.width = `${width}px`
  t.style.transform = `translateX(${x + el.offsetLeft}px)`
  t.classList.add('on')
  clearTimeout(thumbTimer)
  thumbTimer = setTimeout(() => t.classList.remove('on'), 700)
}
function onScroll() { measure(); showThumb() }
// A vertical wheel moves the bar sideways.
function wheel(event: WheelEvent) {
  const el = nav.value
  if (!el || !overflow.value || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return
  event.preventDefault()
  el.scrollLeft += event.deltaY
}

// ---------- Drag with the pointer (touch keeps native scrolling) ----------
const reduced = window.matchMedia('(prefers-reduced-motion: reduce)')
let drag: { x: number; left: number; moved: boolean; t: number; vx: number; id: number } | null = null
let dragEnded = 0
let momentum = 0
function down(event: PointerEvent) {
  if (event.pointerType === 'touch' || event.button !== 0 || !overflow.value || !nav.value) return
  if ((event.target as HTMLElement).closest('.fcb, .cb, .ekey, a, input')) return
  cancelAnimationFrame(momentum)
  drag = { x: event.clientX, left: nav.value.scrollLeft, moved: false, t: event.timeStamp, vx: 0, id: event.pointerId }
}
function move(event: PointerEvent) {
  const el = nav.value
  if (!drag || !el || event.pointerId !== drag.id) return
  const dx = event.clientX - drag.x
  if (!drag.moved) {
    if (Math.abs(dx) < 5) return
    drag.moved = true; dragging.value = true
    el.setPointerCapture(event.pointerId)
    el.style.scrollBehavior = 'auto'
  }
  const dt = Math.max(1, event.timeStamp - drag.t)
  drag.vx = .7 * drag.vx + .3 * ((event.movementX || 0) / dt)
  drag.t = event.timeStamp
  const target = drag.left - dx
  const max = el.scrollWidth - el.clientWidth
  el.scrollLeft = target
  // Past either end the bar gives a little, then springs back.
  const over = target < 0 ? target : target > max ? target - max : 0
  el.style.transform = over && !reduced.matches ? `translateX(${-Math.sign(over) * Math.min(24, Math.abs(over) * .25)}px)` : ''
  showThumb()
}
function up(event: PointerEvent) {
  const el = nav.value
  if (!drag || !el || event.pointerId !== drag.id) return
  const wasDrag = drag.moved, vx = drag.vx
  drag = null
  if (!wasDrag) return
  dragging.value = false
  dragEnded = event.timeStamp
  el.style.scrollBehavior = ''
  if (el.style.transform) { el.style.transition = 'transform .35s cubic-bezier(.2,.7,.2,1)'; el.style.transform = ''; setTimeout(() => { if (el) el.style.transition = '' }, 360) }
  if (reduced.matches) return
  let v = -vx * 16
  const step = () => { if (Math.abs(v) < .4 || !nav.value) return; nav.value.scrollLeft += v; v *= .93; momentum = requestAnimationFrame(step) }
  momentum = requestAnimationFrame(step)
}
// A drag never selects a chip.
function clickCapture(event: MouseEvent) { if (event.timeStamp - dragEnded < 120) { event.stopPropagation(); event.preventDefault() } }

// The current chip stays in view (centred after a keyboard move).
async function reveal(smooth = true) {
  await nextTick()
  const el = nav.value, chip = el?.querySelector<HTMLElement>('.ck.on')
  if (!el || !chip) return
  const left = chip.offsetLeft - el.clientWidth / 2 + chip.offsetWidth / 2
  el.scrollTo({ left: Math.max(0, left), behavior: smooth && !reduced.matches ? 'smooth' : 'auto' })
}
watch(() => props.index, () => void reveal())
let resize: ResizeObserver | undefined
onMounted(() => {
  void reveal(false)
  requestAnimationFrame(measure)
  if (nav.value) { resize = new ResizeObserver(measure); resize.observe(nav.value) }
})
onBeforeUnmount(() => { resize?.disconnect(); clearTimeout(thumbTimer); cancelAnimationFrame(momentum) })
defineExpose({ reveal })
</script>

<template>
  <header class="walker-bar" :class="{ ovf: overflow, dragging }">
    <div class="rt2"><span class="eyebrow">{{ releaseLabel }}</span><b class="count">{{ inRelease }} of {{ order.length }}</b></div>
    <div class="garrs">
      <button type="button" class="garr" aria-label="Previous feature" data-tip="Previous feature · Shift Left arrow" @click="emit('feature', -1)"><AppIcon name="chevron-left" :size="14" /></button>
      <button type="button" class="garr" aria-label="Previous ticket" data-tip="Previous ticket · Left arrow" @click="emit('step', -1)"><AppIcon name="chevron-left" :size="14" /></button>
    </div>
    <div
      ref="nav" class="nav" :class="{ 'fade-l': fadeLeft, 'fade-r': fadeRight }" role="group" aria-label="Features and tickets"
      @scroll.passive="onScroll" @wheel="wheel" @pointerdown="down" @pointermove="move" @pointerup="up" @pointercancel="up" @click.capture="clickCapture"
    >
      <div v-for="group in groups" :key="group.id || 'loose'" class="fg" :class="{ cur: current && group.tickets.includes(current), nofeat: !group.feature }" :style="{ '--fw': lineWidth(group) }">
        <div class="fl">
          <span class="flt">
            <FeatureCheck
              v-if="editable && group.feature && group.tickets.length" :state="selectionOf(group.tickets, included)" :label="group.feature.title"
              :included="group.tickets.filter(t => included.has(t.ticket_node_id)).length" :total="group.tickets.length"
              :next="nextPickLabel(selectionOf(group.tickets, included), memory.get(group.id))" @toggle="emit('toggleFeature', group.id)"
            />
            <button v-if="group.tickets.length" type="button" class="fn" :data-tip="`${group.feature?.title ?? 'Tickets without a feature'} · go to its first ticket`" @click="emit('go', indexOf(group.tickets[0]))">
              <span v-if="group.feature" class="rid">F{{ featureNumber(group) }}</span>{{ group.feature?.title ?? 'No feature' }}
            </button>
            <span v-else class="fn"><span v-if="group.feature" class="rid">F{{ featureNumber(group) }}</span>{{ group.feature?.title }}</span>
          </span>
          <a v-if="group.feature" class="ekey" :href="epicHref(group.feature.key)" target="_blank" rel="noopener" :data-tip="`Epic ${group.feature.key} · opens in a new window`">{{ group.feature.key }}<AppIcon name="external" :size="10" class="ext" /></a>
        </div>
        <div class="chips">
          <template v-if="group.tickets.length">
            <div
              v-for="ticket in group.tickets" :key="ticket.ticket_node_id" class="ck"
              :class="{ on: ticket === current, in: included.has(ticket.ticket_node_id), out: !included.has(ticket.ticket_node_id), can: editable, done: ['done', 'delivered', 'accepted'].includes(stateOf(ticket)) }"
              :data-tip="ticket === current ? undefined : tip(ticket)" @click="emit('go', indexOf(ticket))"
            >
              <span class="slot">
                <i class="dot" aria-hidden="true" />
                <button
                  v-if="editable" type="button" class="cb" role="checkbox" :aria-checked="included.has(ticket.ticket_node_id)" :aria-label="checkLabel(ticket)"
                  :data-tip="included.has(ticket.ticket_node_id) ? `In ${releaseLabel.toLowerCase()} · click to defer (Space)` : 'Backlog · click to include (Space)'" @click.stop="emit('toggle', ticket.ticket_node_id)"
                ><svg viewBox="0 0 16 16" width="10" height="10" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m3.5 8.5 3 3 6-7" /></svg></button>
              </span>
              <button type="button" class="go" :aria-current="ticket === current ? 'true' : undefined" :aria-label="`${ticket.key} ${ticket.title}`" @click.stop="emit('go', indexOf(ticket))">
                <span class="k">{{ ticket.key }}</span><span v-if="ticket === current" class="tt">{{ shortTitle(ticket.title) }}</span>
              </button>
            </div>
          </template>
          <span v-else class="notix">No tickets</span>
        </div>
      </div>
    </div>
    <span ref="thumb" class="navthumb" :class="{ drag: dragging }" aria-hidden="true" />
    <div class="garrs">
      <button type="button" class="garr" aria-label="Next feature" data-tip="Next feature · Shift Right arrow" @click="emit('feature', 1)"><AppIcon name="chevron-right" :size="14" /></button>
      <button type="button" class="garr" aria-label="Next ticket" data-tip="Next ticket · Right arrow" @click="emit('step', 1)"><AppIcon name="chevron-right" :size="14" /></button>
    </div>
    <div class="tools">
      <button type="button" class="gic" aria-label="Find a ticket" aria-keyshortcuts="/" data-tip="Find a ticket · /" @click="emit('search')"><AppIcon name="search" :size="15" /></button>
      <button type="button" class="tool" :aria-pressed="details" aria-label="Details" aria-keyshortcuts="i" data-tip="Details · I" @click="emit('details')"><AppIcon name="info" :size="14" class="tool-icon" /><span class="tool-label">Details</span></button>
      <button type="button" class="gic" aria-label="Shortcuts" aria-keyshortcuts="?" data-tip="Shortcuts · ?" @click="emit('sheet')"><AppIcon name="keyboard" :size="15" /></button>
      <button type="button" class="xbtn" aria-label="Close the walker" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
    </div>
  </header>
</template>

<style scoped>
.walker-bar {
  position: relative; display: flex; align-items: center; gap: 10px; min-height: 68px; padding: 7px 14px;
  background: var(--glass); border-bottom: 1px solid var(--line); -webkit-backdrop-filter: blur(18px) saturate(1.1); backdrop-filter: blur(18px) saturate(1.1);
}
.rt2 { display: grid; gap: 2px; flex-shrink: 0; min-width: 76px; }
.rt2 .eyebrow { font-size: 10px; letter-spacing: .18em; }
.count { font-size: 14px; font-weight: 600; color: var(--ink); font-variant-numeric: tabular-nums; white-space: nowrap; }
/* Two arrows per side: the feature arrow beside the feature line, the ticket arrow beside the chips. */
.garrs { display: none; grid-template-rows: 20px 28px; row-gap: 5px; flex-shrink: 0; }
.ovf .garrs { display: grid; }
.garr { display: grid; place-items: center; width: 24px; height: 24px; align-self: center; padding: 0; border: 0; border-radius: 7px; background: transparent; color: var(--ink-2); opacity: .45; cursor: pointer; }
.garr:first-child { height: 20px; }
.garr:hover, .garr:focus-visible { opacity: 1; background: var(--surface); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--line-2); }
.nav { position: relative; display: flex; align-items: flex-start; flex: 1; min-width: 0; overflow-x: auto; overflow-y: hidden; scrollbar-width: none; padding: 2px 0; }
.nav::-webkit-scrollbar { display: none; }
.ovf .nav { cursor: grab; user-select: none; }
.dragging .nav, .dragging .nav * { cursor: grabbing !important; }
.nav.fade-l { -webkit-mask-image: linear-gradient(90deg, transparent, #000 48px); mask-image: linear-gradient(90deg, transparent, #000 48px); }
.nav.fade-r { -webkit-mask-image: linear-gradient(90deg, #000 calc(100% - 48px), transparent); mask-image: linear-gradient(90deg, #000 calc(100% - 48px), transparent); }
.nav.fade-l.fade-r { -webkit-mask-image: linear-gradient(90deg, transparent, #000 48px, #000 calc(100% - 48px), transparent); mask-image: linear-gradient(90deg, transparent, #000 48px, #000 calc(100% - 48px), transparent); }
.fg { display: grid; grid-template-rows: 20px 28px; row-gap: 5px; flex-shrink: 0; min-width: clamp(116px, var(--fw, 116px), 360px); }
.fg + .fg { margin-left: 8px; padding-left: 10px; border-left: 1px solid var(--line-2); }
.fl { display: flex; align-items: center; gap: 12px; height: 20px; min-width: 0; font: 500 12.5px/20px var(--font); color: var(--ink-2); }
.flt { position: sticky; left: 6px; display: inline-flex; align-items: center; gap: 7px; min-width: 0; max-width: 300px; }
.fn { min-width: 0; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: pointer; }
.fn:hover { color: var(--teal-ink); }
.fn:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.rid { margin-right: 6px; font: 500 10px/1 var(--mono); letter-spacing: .06em; color: var(--secondary-ink); }
.fg.cur .fl { color: var(--teal-ink); font-weight: 600; }
.nofeat .fn { color: var(--ink-3); }
.ekey { position: sticky; right: 52px; display: inline-flex; align-items: center; gap: 3px; margin-left: auto; padding-left: 6px; flex-shrink: 0; font: 500 10.5px/1 var(--mono); letter-spacing: .04em; color: var(--ink-3); text-decoration: none; transition: opacity .15s ease; }
.ekey:hover { color: var(--teal-ink); }
.ekey:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.ekey .ext { opacity: .6; }
.ekey.gone { opacity: 0; visibility: hidden; }
.chips { display: flex; align-items: center; gap: 6px; }
.ck {
  display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px 0 7px; border-radius: 999px; white-space: nowrap;
  background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line); font-size: 13px; color: var(--ink-2); cursor: pointer;
}
.ck:hover { box-shadow: inset 0 0 0 1px var(--aqua); }
.ck:has(.go:focus-visible) { box-shadow: inset 0 0 0 1px var(--aqua), var(--focus-ring); }
.go { display: inline-flex; align-items: center; gap: 6px; min-width: 0; height: 100%; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; cursor: pointer; outline: none; }
.ck.on { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line), 0 4px 12px -6px color-mix(in srgb, var(--primary-line) 55%, transparent); }
.k { font: 500 11.5px/1 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.ck.out .k { color: var(--ink-3); }
.ck.on .k { color: var(--teal-ink); }
.tt { max-width: 240px; overflow: hidden; text-overflow: ellipsis; font-weight: 600; color: var(--teal-ink); }
/* The slot: a 26px target over a 16px mark; the checkbox shows only over the slot, on the current chip, or with keyboard focus. */
.slot { display: grid; place-items: center; flex-shrink: 0; box-sizing: border-box; width: 26px; height: 26px; padding: 5px; margin: -5px; }
.slot > * { grid-area: 1 / 1; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--teal); }
.ck.out .dot { background: transparent; box-shadow: inset 0 0 0 1.5px var(--ink-3); }
.ck.done .dot { background: var(--ok); box-shadow: none; }
.cb { display: none; place-items: center; width: 16px; height: 16px; padding: 0; border: 0; border-radius: 4px; background: var(--surface); color: var(--button-ink); box-shadow: inset 0 0 0 1.5px var(--line-2); cursor: pointer; }
.ck.in .cb { background: var(--teal); box-shadow: none; }
.ck.out .cb svg { visibility: hidden; }
.ck.can .slot:hover .cb, .ck.can.on .cb, .ck.can .cb:focus-visible { display: grid; }
.ck.can .slot:hover .dot, .ck.can.on .dot, .ck.can .slot:has(.cb:focus-visible) .dot { display: none; }
.cb:focus-visible { box-shadow: var(--focus-ring); }
.notix { display: inline-flex; align-items: center; height: 28px; padding: 0 10px; border-radius: 999px; box-shadow: inset 0 0 0 1px var(--line-2); border: 0; outline: 1px dashed var(--line-2); outline-offset: -1px; font-size: 12px; color: var(--ink-3); }
.navthumb { position: absolute; left: 0; bottom: -1px; height: 2px; border-radius: 2px; background: var(--teal); opacity: 0; transition: opacity .3s ease; pointer-events: none; }
.navthumb.on { opacity: .5; }
.navthumb.drag { opacity: .85; }
.tools { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.gic, .xbtn { display: grid; place-items: center; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-2); cursor: pointer; }
.gic:hover { background: var(--surface); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--line-2); }
.xbtn { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line-2); }
.xbtn:hover { color: var(--ink); background: var(--surface); }
.gic:focus-visible, .xbtn:focus-visible, .tool:focus-visible { box-shadow: var(--focus-ring); }
.tool-icon { display: none; }
.tool { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 12px; border: 0; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font-size: 12.5px; font-weight: 600; cursor: pointer; }
.tool[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
@media (max-width: 900px) { .rt2 .eyebrow { display: none; } .rt2 { min-width: 0; } }
@media (max-width: 720px) {
  .walker-bar { flex-wrap: wrap; gap: 6px 8px; padding: 6px 10px; }
  .rt2 { order: 1; }
  .tools { order: 2; margin-left: auto; }
  .gic, .xbtn, .tool { width: 44px; height: 44px; }
  .tool { padding: 0; justify-content: center; border-radius: 50%; }
  .tool-icon { display: block; }
  .tool-label { display: none; }
  .nav { order: 3; flex-basis: 100%; }
  .garrs { display: none !important; }
}
</style>
