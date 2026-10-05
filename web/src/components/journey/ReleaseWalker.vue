<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { contentUrl, isImage, listAttachments, type Attachment } from '../../lib/attachments'
import { hours, type WalkerTicket } from '../../lib/journey'
import type { Plan } from '../../lib/usePlan'
import { statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import MarkdownBody from '../MarkdownBody.vue'
import WalkerBar from './WalkerBar.vue'
import { vClipTip } from '../../lib/clipTip'

// The release walker: every ticket of the release (and, while planning, the
// backlog) with its screens, one at a time. The screens are the ticket's image
// attachments; ↑↓ step through them, C compares two, Z shows them at 100 %.
// Space puts the ticket in the release or back in the backlog while planning.
const props = defineProps<{ plan: Plan; editable: boolean; releaseLabel: string; projectKey: string; workById: Map<string, ListItem>; startKey: string | null }>()
const emit = defineEmits<{ close: []; moved: [key: string]; open: [key: string] }>()

const dialog = ref<HTMLDialogElement>()
const order = computed(() => props.plan.order.value)
const index = ref(Math.max(0, order.value.findIndex(t => t.key === props.startKey)))
const ticket = computed<WalkerTicket | undefined>(() => order.value[index.value])
const item = computed(() => ticket.value ? props.workById.get(ticket.value.ticket_node_id) : undefined)
const details = ref(window.innerWidth > 900)
const descriptionId = useId(), descriptionExpanded = ref(false), descriptionClipped = ref(false)
function measureDescription(clipped: boolean) { descriptionClipped.value = clipped }
watch(() => [ticket.value?.ticket_node_id, item.value?.body], () => { descriptionExpanded.value = false; descriptionClipped.value = false })
const sheet = ref(false)
const searching = ref(false)
const term = ref('')
const compare = ref(false)
const zoom = ref(false)
const split = ref(50)
const screen = ref(0)
const hint = ref(false)
let opener: HTMLElement | null = null

// ---------- Screens: the ticket's image attachments, cached per ticket ----------
const screens = ref(new Map<string, Attachment[] | 'loading' | 'error'>())
const current = computed(() => { const value = ticket.value ? screens.value.get(ticket.value.ticket_node_id) : undefined; return Array.isArray(value) ? value : [] })
const screensState = computed(() => { const value = ticket.value ? screens.value.get(ticket.value.ticket_node_id) : undefined; return value === undefined || value === 'loading' ? 'loading' : value === 'error' ? 'error' : 'ready' })
async function loadScreens(t: WalkerTicket | undefined) {
  if (!t || screens.value.has(t.ticket_node_id)) return
  screens.value = new Map(screens.value).set(t.ticket_node_id, 'loading')
  try {
    const list = (await listAttachments(t.ticket_node_id)).filter(isImage)
    screens.value = new Map(screens.value).set(t.ticket_node_id, list)
  } catch { screens.value = new Map(screens.value).set(t.ticket_node_id, 'error') }
}
watch(ticket, t => {
  screen.value = 0; compare.value = false; zoom.value = false
  void loadScreens(t)
  // Neighbours load ahead, so stepping feels immediate.
  void loadScreens(order.value[(index.value + 1) % Math.max(1, order.value.length)])
  if (t) emit('moved', t.key)
}, { immediate: true })
const shown = computed(() => current.value[screen.value])
const other = computed(() => current.value[(screen.value + 1) % Math.max(1, current.value.length)])

// ---------- Moving ----------
function go(i: number) { if (order.value.length) { index.value = ((i % order.value.length) + order.value.length) % order.value.length; searching.value = false; dismissHint() } }
const step = (delta: number) => go(index.value + delta)
function feature(delta: number) {
  const starts = props.plan.groups.value.filter(g => g.tickets.length).map(g => order.value.indexOf(g.tickets[0]))
  if (!starts.length) return
  const at = starts.filter(s => s <= index.value).length - 1
  go(starts[((at + delta) % starts.length + starts.length) % starts.length])
}
function toggle(id?: string) {
  const target = id ?? ticket.value?.ticket_node_id
  if (target && props.editable) void props.plan.toggleTicket(target)
}
function stepScreen(delta: number) { if (current.value.length > 1) { screen.value = (screen.value + delta + current.value.length) % current.value.length; compare.value = false } }
const stateLabel = computed(() => {
  const t = ticket.value
  if (!t) return ''
  const state = item.value?.state
  if (state && statusMeta(state).closed) return statusMeta(state).label.toLowerCase()
  return props.plan.included.value.has(t.ticket_node_id) ? props.releaseLabel.toLowerCase() : 'backlog'
})
const featureOf = computed(() => props.plan.groups.value.find(g => ticket.value && g.tickets.includes(ticket.value)))
const featureNo = computed(() => featureOf.value?.feature ? props.plan.groups.value.filter(g => g.feature).indexOf(featureOf.value) + 1 : 0)
const acceptance = computed(() => { const value = item.value?.fields?.acceptance_criteria; return typeof value === 'string' ? value.trim() : '' })
const stateOf = (t: WalkerTicket) => props.workById.get(t.ticket_node_id)?.state ?? ''

// ---------- Search ----------
const matches = computed(() => {
  const needle = term.value.trim().toLowerCase()
  const groups = props.plan.groups.value.map(g => ({ group: g, tickets: g.tickets.filter(t => !needle || t.key.toLowerCase().includes(needle) || t.title.toLowerCase().includes(needle)) }))
  return groups.filter(g => g.tickets.length)
})
const searchInput = ref<HTMLInputElement>()
async function openSearch() { searching.value = true; sheet.value = false; term.value = ''; await nextTick(); searchInput.value?.focus() }
function searchKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); searching.value = false; dialog.value?.focus() }
  else if (event.key === 'Enter') { event.preventDefault(); const first = matches.value[0]?.tickets[0]; if (first) go(order.value.indexOf(first)) }
}

// ---------- Swipe on touch ----------
let swipe: { x: number; y: number } | null = null
function touchStart(event: PointerEvent) { if (event.pointerType === 'touch' && !zoom.value) swipe = { x: event.clientX, y: event.clientY } }
function touchEnd(event: PointerEvent) {
  if (!swipe) return
  const dx = event.clientX - swipe.x, dy = event.clientY - swipe.y
  swipe = null
  if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) * 1.5) step(dx < 0 ? 1 : -1)
}
// Compare: drag the divide.
const sliding = ref(false)
function slide(event: PointerEvent) {
  if (!sliding.value) return
  const frame = (event.currentTarget as HTMLElement).closest('.cmp')?.getBoundingClientRect()
  if (frame) split.value = Math.round(Math.max(0, Math.min(100, (event.clientX - frame.left) / frame.width * 100)))
}

// ---------- Keys ----------
function keydown(event: KeyboardEvent) {
  // Reading full context must keep native scrolling, including Shift+Space.
  if (event.target instanceof Element && event.target.closest('.description.revealed') &&
    ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', ' ', 'PageUp', 'PageDown', 'Home', 'End'].includes(event.key)) return
  const target = event.target as HTMLElement
  if (event.key === 'Escape') {
    event.preventDefault()
    if (sheet.value) sheet.value = false
    else if (searching.value) searching.value = false
    else if (compare.value) compare.value = false
    else if (zoom.value) zoom.value = false
    else close()
    return
  }
  if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || event.metaKey || event.ctrlKey || event.altKey) return
  if (sheet.value && event.key !== '?') return
  const shift = event.shiftKey
  switch (event.key) {
    case 'ArrowRight': event.preventDefault(); if (compare.value) split.value = Math.min(100, split.value + 5); else if (shift) feature(1); else step(1); break
    case 'ArrowLeft': event.preventDefault(); if (compare.value) split.value = Math.max(0, split.value - 5); else if (shift) feature(-1); else step(-1); break
    case 'ArrowDown': event.preventDefault(); stepScreen(1); break
    case 'ArrowUp': event.preventDefault(); stepScreen(-1); break
    case ' ':
      if (target.closest('button') && target !== dialog.value) return
      event.preventDefault(); toggle(); break
    case 'c': case 'C': if (current.value.length > 1) { event.preventDefault(); compare.value = !compare.value; zoom.value = false } break
    case 'z': case 'Z': if (shown.value) { event.preventDefault(); zoom.value = !zoom.value; compare.value = false } break
    case 'i': case 'I': event.preventDefault(); details.value = !details.value; break
    case '/': event.preventDefault(); void openSearch(); break
    case '?': event.preventDefault(); sheet.value = !sheet.value; searching.value = false; break
    default: return
  }
  dismissHint()
}

// ---------- First-use hint ----------
function dismissHint() {
  if (!hint.value) return
  hint.value = false
  try { localStorage.setItem('aeon.walker-hint', '1') } catch { /* private window */ }
}
onMounted(() => {
  opener = document.activeElement as HTMLElement | null
  dialog.value?.showModal()
  dialog.value?.focus()
  try { hint.value = localStorage.getItem('aeon.walker-hint') !== '1' } catch { hint.value = false }
})
function close() { dialog.value?.close(); emit('close') }
onBeforeUnmount(() => { if (opener?.isConnected) opener.focus({ preventScroll: true }) })
const phone = window.matchMedia('(max-width: 720px)').matches
const touch = window.matchMedia('(hover: none)').matches
</script>

<template>
  <dialog ref="dialog" class="walker" aria-label="Release walker" tabindex="-1" @cancel.prevent @keydown="keydown">
    <WalkerBar
      :groups="plan.groups.value" :order="order" :index="index" :included="plan.included.value" :memory="plan.memory.value" :editable="editable"
      :release-label="releaseLabel" :project-key="projectKey" :details="details" :state-of="stateOf"
      @go="go" @step="step" @feature="feature" @toggle="toggle" @toggle-feature="id => plan.toggleFeature(id)"
      @search="openSearch" @details="details = !details" @sheet="sheet = !sheet" @close="close"
    />
    <div v-if="searching" class="jump" role="dialog" aria-label="Find a ticket">
      <input ref="searchInput" v-model="term" class="field" placeholder="Search tickets" aria-label="Search tickets" @keydown="searchKeys" />
      <div class="jump-list">
        <template v-for="entry in matches" :key="entry.group.id || 'loose'">
          <p class="jump-h">{{ entry.group.feature ? `${entry.group.feature.key} · ${entry.group.feature.title}` : 'Not tied to a feature' }}</p>
          <button v-for="t in entry.tickets" :key="t.ticket_node_id" type="button" class="jump-row" :class="{ on: t === ticket }" @click="go(order.indexOf(t))"><span class="mono">{{ t.key }}</span>{{ t.title }}</button>
        </template>
        <p v-if="!matches.length" class="jump-none">No match.</p>
      </div>
    </div>

    <div class="shell" :class="{ 'with-info': details && !phone }">
      <main class="stage" :aria-label="ticket ? `${ticket.key} ${ticket.title}` : 'No ticket'" @pointerdown="touchStart" @pointerup="touchEnd">
        <button type="button" class="edge l" aria-label="Previous ticket" @click="step(-1)"><span class="a"><AppIcon name="chevron-left" :size="20" /></span></button>
        <button type="button" class="edge r" aria-label="Next ticket" @click="step(1)"><span class="a"><AppIcon name="chevron-right" :size="20" /></span></button>

        <div v-if="!ticket" class="empty-card"><h2>No tickets in this release</h2><p>Tickets appear here once the requirements are agreed or tickets are added.</p></div>
        <div v-else-if="screensState === 'loading'" class="shot-skeleton skeleton" aria-label="Loading screens" role="status" />
        <div v-else-if="compare && shown && other" class="cmp" :style="{ '--split': `${split}%` }" @pointermove="slide" @pointerup="sliding = false" @pointerleave="sliding = false">
          <img class="shot" :src="contentUrl(shown.id, 'preview')" :alt="shown.caption || shown.name" draggable="false" />
          <img class="shot over" :src="contentUrl(other.id, 'preview')" :alt="other.caption || other.name" draggable="false" :style="{ clipPath: `inset(0 0 0 ${split}%)` }" />
          <div class="divide" :style="{ left: `${split}%` }">
            <button type="button" class="knob" role="slider" aria-label="Compare divide" :aria-valuenow="split" aria-valuemin="0" aria-valuemax="100" @pointerdown.stop="sliding = true"><AppIcon name="chevron-left" :size="12" /><AppIcon name="chevron-right" :size="12" /></button>
          </div>
          <span class="cmp-tag a">{{ shown.caption || shown.name }}</span><span class="cmp-tag b">{{ other.caption || other.name }}</span>
        </div>
        <div v-else-if="shown" class="shot-wrap" :class="{ zoom }">
          <button type="button" class="shot-btn" :aria-label="zoom ? 'Fit the screen' : 'Show at 100 %'" @click="zoom = !zoom">
            <img class="shot" :src="contentUrl(shown.id, zoom ? 'original' : 'preview')" :alt="shown.caption || shown.name" draggable="false" />
          </button>
        </div>
        <div v-else class="empty-card">
          <span class="empty-icon"><AppIcon name="image" :size="20" /></span>
          <h2>No screen for this ticket</h2>
          <p>{{ screensState === 'error' ? 'Its attachments could not be loaded.' : 'Drop a screen design on the ticket to see it here, or ask Aithema to draft one.' }}</p>
          <button type="button" class="btn sm" @click="emit('open', ticket.key)"><AppIcon name="arrow" :size="13" />Open {{ ticket.key }}</button>
        </div>

        <div v-if="current.length" class="pill" role="group" aria-label="Screens">
          <button v-for="(s, i) in current" :key="s.id" type="button" class="pill-btn" :aria-pressed="i === screen" @click="screen = i; compare = false">{{ s.caption || s.name }}</button>
          <span v-if="current.length > 1" class="pill-keys" aria-hidden="true"><KeyCap k="up" /><KeyCap k="down" /></span>
          <span class="pill-sep" aria-hidden="true" />
          <button v-if="current.length > 1" type="button" class="pill-btn" :aria-pressed="compare" aria-keyshortcuts="c" @click="compare = !compare; zoom = false">Compare</button>
          <button type="button" class="pill-btn" :aria-pressed="zoom" aria-keyshortcuts="z" @click="zoom = !zoom; compare = false">100 %</button>
        </div>

        <div v-if="hint && ticket && touch" class="hint-bubble one" role="note" @click="dismissHint">
          <span>Swipe, or use the arrows below, to move between tickets.</span>
        </div>
        <div v-else-if="hint && ticket" class="hint-bubble" role="note" @click="dismissHint">
          <span><KeyCap k="left" /><KeyCap k="right" /> tickets</span>
          <span><KeyCap k="up" /><KeyCap k="down" /> screens</span>
          <span v-if="editable"><kbd class="keycap">Space</kbd> include</span>
          <span><kbd class="keycap">?</kbd> all shortcuts</span>
        </div>
      </main>

      <aside v-if="details && ticket" class="info" aria-label="Ticket details">
        <p class="meta mono">{{ ticket.key }}<template v-if="featureNo"> · F{{ featureNo }}</template><template v-if="ticket.estimated_hours != null"> · {{ hours(ticket.estimated_hours) }}</template> · {{ stateLabel }}</p>
        <h2 class="info-title">{{ ticket.title }}</h2>
        <template v-if="featureOf?.feature">
          <p class="eyebrow fe"><span>Feature</span><a class="ekey" :href="`/p/${encodeURIComponent(projectKey)}/${encodeURIComponent(featureOf.feature.key)}`" target="_blank" rel="noopener">{{ featureOf.feature.key }}<AppIcon name="external" :size="10" /></a></p>
          <p class="info-text"><b class="mono">F{{ featureNo }}</b> {{ featureOf.feature.title }}</p>
        </template>
        <template v-if="acceptance">
          <p class="eyebrow">Acceptance</p>
          <MarkdownBody class="info-md" :body="acceptance" />
        </template>
        <template v-if="item?.body">
          <p class="eyebrow">Description</p>
          <MarkdownBody :id="descriptionId" v-clip-tip="{ text: '', onClip: measureDescription }" class="info-md description" :class="{ clamp: !descriptionExpanded, revealed: descriptionExpanded }" :body="item.body" :tabindex="descriptionExpanded ? 0 : undefined" aria-label="Ticket description" />
          <button v-if="descriptionClipped" type="button" class="description-toggle" :aria-expanded="descriptionExpanded" :aria-controls="descriptionId" @click="descriptionExpanded = !descriptionExpanded">
            <!-- Reserve both labels so revealing cannot resize the control. -->
            <span class="reveal-label" :class="{ active: !descriptionExpanded }" :aria-hidden="descriptionExpanded">Show full description</span>
            <span class="reveal-label" :class="{ active: descriptionExpanded }" :aria-hidden="!descriptionExpanded">Show preview</span>
          </button>
        </template>
        <p class="eyebrow">Screens · {{ current.length }}</p>
        <div v-if="current.length" class="thumbs">
          <button v-for="(s, i) in current" :key="s.id" type="button" class="thumb" :class="{ on: i === screen }" :aria-label="`Show ${s.caption || s.name}`" @click="screen = i; compare = false"><img :src="contentUrl(s.id, 'thumb')" alt="" loading="lazy" /></button>
        </div>
        <p v-else class="info-faint">None yet.</p>
        <div class="info-links">
          <button type="button" class="btn sm" @click="emit('open', ticket.key)"><AppIcon name="arrow" :size="13" />Open ticket</button>
          <button v-if="editable" type="button" class="btn sm ghost" @click="toggle()">{{ plan.included.value.has(ticket.ticket_node_id) ? 'Defer to backlog' : 'Include in release' }}</button>
        </div>
      </aside>
    </div>

    <div v-if="sheet" class="sheet-scrim" @click.self="sheet = false">
      <section class="sheet" role="dialog" aria-label="Walker shortcuts">
        <header><h2>Shortcuts</h2><button type="button" class="xbtn" aria-label="Close shortcuts" @click="sheet = false"><AppIcon name="close" :size="14" /></button></header>
        <dl>
          <div><dt><KeyCap k="left" /><KeyCap k="right" /></dt><dd>Previous / next ticket, wraps around</dd></div>
          <div><dt><kbd class="keycap">Shift</kbd><KeyCap k="left" /><KeyCap k="right" /></dt><dd>Previous / next feature</dd></div>
          <div><dt><KeyCap k="up" /><KeyCap k="down" /></dt><dd>Screens of this ticket</dd></div>
          <div v-if="editable"><dt><kbd class="keycap">Space</kbd></dt><dd>Include in the release / defer (or the checkbox on the ticket)</dd></div>
          <div><dt><kbd class="keycap">C</kbd></dt><dd>Compare two screens</dd></div>
          <div><dt><kbd class="keycap">Z</kbd></dt><dd>Zoom to 100 %</dd></div>
          <div><dt><kbd class="keycap">I</kbd></dt><dd>Show / hide details</dd></div>
          <div><dt><kbd class="keycap">/</kbd></dt><dd>Find a ticket</dd></div>
          <div><dt><kbd class="keycap">?</kbd></dt><dd>This sheet</dd></div>
          <div><dt><kbd class="keycap">Esc</kbd></dt><dd>Close</dd></div>
        </dl>
      </section>
    </div>
  </dialog>
</template>

<style scoped>
.walker {
  width: 100vw; height: 100dvh; max-width: none; max-height: none; margin: 0; padding: 0; border: 0; color: var(--ink); outline: none;
  background: radial-gradient(90% 70% at 50% 40%, var(--aqua-3), var(--canvas) 70%);
  display: none; grid-template-rows: auto minmax(0, 1fr); grid-template-columns: minmax(0, 1fr);
}
.walker[open] { display: grid; }
.walker::backdrop { background: rgba(4, 12, 14, .5); }
.shell { position: relative; display: grid; grid-template-columns: minmax(0, 1fr); min-height: 0; }
.shell.with-info { grid-template-columns: minmax(0, 1fr) 300px; }
.stage { position: relative; display: grid; place-items: center; min-height: 0; overflow: hidden; padding: 24px 84px 72px; touch-action: pan-y; }
.edge { position: absolute; top: 0; bottom: 0; z-index: 2; display: grid; place-items: center; width: 72px; padding: 0; border: 0; background: transparent; color: var(--ink-2); cursor: pointer; }
.edge.l { left: 0; } .edge.r { right: 0; }
.edge .a { display: grid; place-items: center; width: 40px; height: 40px; border-radius: 50%; opacity: .35; transition: opacity .15s ease, background .15s ease; }
.edge:hover .a, .edge:focus-visible .a { opacity: 1; background: var(--surface); box-shadow: 0 0 0 1px var(--line-2), var(--shadow-pop, 0 8px 20px -10px rgba(0, 0, 0, .4)); color: var(--teal-ink); }
.edge:focus-visible { outline: none; }
.shot-wrap { display: grid; place-items: center; width: 100%; height: 100%; min-height: 0; }
.shot-btn { display: grid; place-items: center; max-width: 100%; max-height: 100%; padding: 0; border: 0; background: transparent; cursor: zoom-in; }
.shot-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 10px; }
.shot { max-width: 100%; max-height: calc(100dvh - 68px - 110px); object-fit: contain; border-radius: 10px; background: #fff; box-shadow: 0 30px 70px -32px rgba(16, 35, 39, .6), 0 0 0 1px var(--line); }
.shot-wrap.zoom { overflow: auto; place-items: start center; }
.shot-wrap.zoom .shot-btn { cursor: zoom-out; max-width: none; max-height: none; }
.shot-wrap.zoom .shot { max-width: none; max-height: none; }
.shot-skeleton { width: min(70%, 900px); height: 60%; border-radius: 12px; }
.cmp { position: relative; display: grid; max-width: 100%; max-height: calc(100dvh - 68px - 110px); border-radius: 10px; overflow: hidden; }
.cmp .shot { grid-area: 1 / 1; max-height: calc(100dvh - 68px - 110px); box-shadow: none; }
.cmp .over { position: relative; }
.divide { position: absolute; top: 0; bottom: 0; width: 2px; margin-left: -1px; background: var(--gold); }
.knob { position: absolute; top: 50%; left: 50%; display: inline-flex; align-items: center; justify-content: center; width: 34px; height: 34px; margin: -17px 0 0 -17px; padding: 0; border: 2px solid var(--gold); border-radius: 50%; background: var(--surface); color: var(--gold-ink); cursor: ew-resize; touch-action: none; }
.knob:focus-visible { box-shadow: var(--focus-ring); }
.cmp-tag { position: absolute; top: 10px; padding: 3px 8px; border-radius: 999px; background: var(--glass); box-shadow: 0 0 0 1px var(--line); font-size: 11.5px; color: var(--ink-2); }
.cmp-tag.a { left: 10px; } .cmp-tag.b { right: 10px; }
.empty-card { display: grid; justify-items: center; gap: 8px; max-width: 420px; padding: 32px 28px; border-radius: var(--radius); background: var(--glass); box-shadow: var(--shadow); text-align: center; }
.empty-card h2 { font-size: 18px; font-weight: 500; }
.empty-card p { font-size: 13.5px; color: var(--ink-2); }
.empty-icon { display: grid; place-items: center; width: 40px; height: 40px; border-radius: 12px; background: var(--row-selected); color: var(--teal-ink); }
.pill { position: absolute; left: 50%; bottom: 18px; z-index: 3; display: flex; align-items: center; gap: 4px; max-width: calc(100% - 40px); padding: 4px; border-radius: 999px; transform: translateX(-50%); background: var(--glass); box-shadow: var(--shadow); -webkit-backdrop-filter: blur(14px); backdrop-filter: blur(14px); overflow-x: auto; scrollbar-width: none; }
.pill-btn { flex-shrink: 0; height: 28px; max-width: 200px; padding: 0 12px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
.pill-btn:hover { color: var(--ink); background: var(--row-hover); }
.pill-btn[aria-pressed="true"] { background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.pill-btn:focus-visible { box-shadow: var(--focus-ring); }
.pill-keys { display: inline-flex; flex-shrink: 0; gap: 2px; }
.pill-sep { width: 1px; height: 18px; margin: 0 4px; background: var(--line-2); flex-shrink: 0; }
/* The first-use hint: a dark bubble in both themes. */
.hint-bubble { position: absolute; top: 18px; left: 18px; z-index: 4; display: grid; grid-template-columns: auto auto; gap: 8px 16px; padding: 12px 14px; border-radius: 12px; background: rgba(16, 35, 39, .94); color: #fffefa; font-size: 12.5px; box-shadow: 0 16px 40px -18px rgba(0, 0, 0, .6); cursor: pointer; }
.hint-bubble span { display: inline-flex; align-items: center; gap: 3px; color: #fffefa; }
.hint-bubble .keycap { margin-right: 3px; background: rgba(255, 254, 250, .14); color: #fffefa; box-shadow: inset 0 0 0 1px rgba(255, 254, 250, .28); }
.info { display: grid; grid-template-columns: minmax(0, 1fr); align-content: start; gap: 8px; padding: 16px 16px 24px; overflow: auto; background: var(--glass); border-left: 1px solid var(--line); -webkit-backdrop-filter: blur(16px); backdrop-filter: blur(16px); }
.meta { font-size: 11px; color: var(--ink-3); letter-spacing: .02em; }
.info-title { font-size: 16px; font-weight: 600; line-height: 1.35; overflow-wrap: anywhere; }
.info .eyebrow { margin-top: 8px; }
.fe { display: flex; align-items: center; justify-content: space-between; }
.fe .ekey { display: inline-flex; align-items: center; gap: 3px; font: 500 10.5px/1 var(--mono); letter-spacing: .04em; color: var(--ink-3); text-decoration: none; text-transform: none; }
.fe .ekey:hover { color: var(--teal-ink); }
.info-text { font-size: 13.5px; color: var(--ink); overflow-wrap: anywhere; }
.info-text b { color: var(--gold-ink); font-weight: 500; font-size: 11px; }
.info-md { font-size: 13px; }
.description { max-height: 180px; overflow: hidden; scrollbar-gutter: stable; }
.info-md.clamp { -webkit-mask-image: linear-gradient(180deg, #000 70%, transparent); mask-image: linear-gradient(180deg, #000 70%, transparent); }
/* A long description scrolls in place once revealed, keeping its toggle and
   the controls below still. Short descriptions retain their natural height. */
.description.revealed { overflow: auto; }
.description-toggle { display: grid; align-items: center; width: max-content; min-width: 44px; max-width: 100%; min-height: 28px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.reveal-label { grid-area: 1 / 1; min-width: 0; overflow-wrap: anywhere; visibility: hidden; }
.reveal-label.active { visibility: visible; }
@media (pointer: coarse) { .description-toggle { min-height: 44px; } }
.description-toggle:focus-visible, .description:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
.info-faint { font-size: 12.5px; color: var(--ink-3); }
.thumbs { display: flex; flex-wrap: wrap; gap: 6px; }
.thumb { width: 58px; height: 44px; padding: 0; border: 0; border-radius: 7px; overflow: hidden; background: var(--surface-2); box-shadow: 0 0 0 1px var(--line); cursor: pointer; }
.thumb img { width: 100%; height: 100%; object-fit: cover; }
.thumb.on { box-shadow: 0 0 0 2px var(--teal); }
.thumb:focus-visible { box-shadow: var(--focus-ring); }
.info-links { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 10px; }
.jump { position: absolute; top: 68px; right: 120px; z-index: 6; display: grid; gap: 6px; width: 360px; max-height: 60dvh; padding: 10px; border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.jump-list { display: grid; gap: 1px; overflow: auto; }
.jump-h { padding: 8px 8px 2px; font: 500 10px/1.2 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--gold-ink); }
.jump-row { display: flex; gap: 10px; align-items: baseline; padding: 7px 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; cursor: pointer; }
.jump-row .mono { flex-shrink: 0; font-size: 11px; color: var(--ink-3); }
.jump-row:hover, .jump-row.on { background: var(--row-selected); }
.jump-row:focus-visible { box-shadow: var(--focus-ring); }
.jump-none { padding: 10px 8px; font-size: 12.5px; color: var(--ink-3); }
.sheet-scrim { position: absolute; inset: 0; z-index: 10; display: grid; place-items: center; background: color-mix(in oklab, var(--canvas) 70%, transparent); -webkit-backdrop-filter: blur(8px); backdrop-filter: blur(8px); }
.sheet { width: min(var(--dialog-m), calc(100vw - 32px)); padding: 22px 24px; border-radius: 18px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.sheet header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.sheet h2 { font-size: 18px; font-weight: 500; }
.sheet dl { display: grid; margin: 0; }
.sheet dl div { display: grid; grid-template-columns: 124px 1fr; gap: 12px; align-items: center; padding: 7px 0; border-bottom: 1px solid var(--line); font-size: 13.5px; }
.sheet dt { display: flex; gap: 3px; }
.sheet dd { margin: 0; color: var(--ink); }
.xbtn { display: grid; place-items: center; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 50%; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); cursor: pointer; }
.xbtn:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 720px) {
  .stage { padding: 14px 12px 64px; }
  .edge { width: 44px; top: auto; bottom: 10px; height: 44px; }
  .edge.l { left: 6px; } .edge.r { right: 6px; }
  .edge .a { opacity: .8; background: var(--surface); box-shadow: 0 0 0 1px var(--line-2); }
  .pill { bottom: 12px; max-width: calc(100% - 120px); }
  .jump { left: 10px; right: 10px; width: auto; top: 104px; }
  .shot { max-height: calc(100dvh - 190px); }
  /* Details come up as a sheet over the screen. */
  .info { position: absolute; left: 0; right: 0; bottom: 0; z-index: 5; max-height: 60dvh; border-left: 0; border-top: 1px solid var(--line); border-radius: 18px 18px 0 0; box-shadow: 0 -18px 40px -24px rgba(0, 0, 0, .5); background: var(--surface-raised); }
  .hint-bubble.one { grid-template-columns: 1fr; right: 18px; }
}
</style>
