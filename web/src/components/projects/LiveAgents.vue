<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { absoluteTime } from '../../lib/work'
import { harnessLabel } from '../../lib/agentState'
import { agentKey, chipText, elapsedFor, liveSummary, phaseLabel, who, type LiveAgent } from '../../lib/liveAgents'
import { useLiveAgents } from '../../stores/liveAgents'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import LiveBot from './LiveBot.vue'

// The agents working in one project right now (AEON-184), as a small living
// chip: their robots at work, the lead's name and ticket, and for how long.
// It sits beside the card's or row's link (never inside it), so it can be a
// button: hover, focus or a click shows who works on what; each agent opens
// its session in Agents, each ticket its page. Status only: nothing here acts.
// Variants: `card` (name, ticket key, elapsed) and `row` (robots and key; on
// a phone robots only).
const props = withDefaults(defineProps<{ agents: LiveAgent[]; project: { title: string; routeKey: string }; variant?: 'card' | 'row' }>(), { variant: 'card' })
const live = useLiveAgents()
const projects = useProjects()
const id = useId()
const trigger = ref<HTMLButtonElement>()
const panel = ref<HTMLElement>()
const root = ref<HTMLElement>()
const open = ref(false)
const x = ref(0)
const y = ref(0)
const above = ref(true)
const asleep = ref(false)

const faces = computed(() => props.agents.slice(0, props.variant === 'card' ? 3 : 2))
const chip = computed(() => chipText(props.agents))
const summary = computed(() => liveSummary(props.agents))
const lead = computed(() => props.agents[0])
const state = computed(() => props.agents.some(a => !a.state || a.state === 'working') ? 'working' : props.agents.some(a => a.state === 'waiting') ? 'waiting' : 'stale')
const groupLabel = computed(() => state.value === 'working' ? 'working' : state.value === 'waiting' ? 'waiting for approval' : 'with no recent activity')
const elapsed = computed(() => lead.value ? elapsedFor(lead.value, live.serverNow) : '')
// Without a ticket, a lead that is still starting (or stopping) says so.
const leadPhase = computed(() => lead.value && (lead.value.phase !== 'working' || (lead.value.state && lead.value.state !== 'working')) ? phaseLabel(lead.value).toLowerCase() : '')
const clockFormat = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' })
const clock = (iso: string) => clockFormat.format(Date.parse(iso))
const agePrefix = (agent: LiveAgent) => agent.state === 'stale' || agent.state === 'waiting' ? 'session age' : 'for'
// A ticket links into the project it lives in now, which may not be this card's
// (the agent is listed here because its session started here).
const ticketHref = (agent: LiveAgent) => {
  if (!agent.ticket) return ''
  const routeKey = projects.byId(agent.ticket.project_id)?.routeKey ?? (agent.ticket.project_id === agent.project_id ? props.project.routeKey : '')
  return routeKey ? `/p/${encodeURIComponent(routeKey)}/${encodeURIComponent(agent.ticket.key)}` : ''
}

// ---------- Opening ----------
let openTimer: ReturnType<typeof setTimeout> | undefined
let closeTimer: ReturnType<typeof setTimeout> | undefined
let hovering = false
let quietFocus = false
function show(focusInside = false) {
  clearTimeout(openTimer); clearTimeout(closeTimer)
  if (!open.value) { open.value = true; void nextTick(() => { place(); if (focusInside) firstLink()?.focus() }) }
  else if (focusInside) firstLink()?.focus()
}
function hide(restore = false) {
  clearTimeout(openTimer); clearTimeout(closeTimer)
  if (!open.value) return
  open.value = false
  if (restore) { quietFocus = true; trigger.value?.focus(); quietFocus = false }
}
const firstLink = () => panel.value?.querySelector<HTMLElement>('a[href]') ?? null
function enter(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  hovering = true
  clearTimeout(closeTimer)
  if (!open.value) openTimer = setTimeout(() => show(), 240)
}
function leave(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  hovering = false
  clearTimeout(openTimer)
  closeTimer = setTimeout(() => { if (!hovering && !within(document.activeElement)) hide() }, 180)
}
const within = (node: Element | null) => !!node && (!!panel.value?.contains(node) || node === trigger.value)
function toggle(event: MouseEvent) {
  // A keyboard click (detail 0) opens and moves into the list.
  if (open.value && event.detail !== 0) { hide(); return }
  show(event.detail === 0)
}
function focusIn() { if (!quietFocus && trigger.value?.matches(':focus-visible')) show() }
function focusOut(event: FocusEvent) {
  if (within(event.relatedTarget as Element | null) || hovering) return
  hide()
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) { event.preventDefault(); event.stopPropagation(); hide(true); return }
  if (event.key === 'ArrowDown' && event.target === trigger.value) { event.preventDefault(); show(true); return }
  if (event.key !== 'Tab' || !panel.value?.contains(event.target as Node)) return
  const links = [...panel.value.querySelectorAll<HTMLElement>('a[href]')]
  const edge = event.shiftKey ? links[0] : links[links.length - 1]
  // Leaving the list at either end returns to the chip, so Tab goes on from there.
  if (event.target === edge) { event.preventDefault(); hide(true) }
}
function outside(event: PointerEvent) { if (open.value && !within(event.target as Element)) hide() }

// ---------- Placing ----------
// Above the chip when there is room (cards keep their footers in view), else below.
function place() {
  const anchor = trigger.value, pop = panel.value
  if (!anchor || !pop) return
  const rect = anchor.getBoundingClientRect()
  const width = pop.offsetWidth, height = pop.offsetHeight
  above.value = rect.top - height - 10 > 64 || rect.bottom + height + 10 > innerHeight
  x.value = Math.round(Math.min(Math.max(8, rect.left - 6), innerWidth - width - 8))
  y.value = Math.round(above.value ? Math.max(8, rect.top - height - 8) : rect.bottom + 8)
}
let frame = 0
const replace = () => { if (open.value && !frame) frame = requestAnimationFrame(() => { frame = 0; place() }) }
watch(() => props.agents.length, () => { if (open.value) void nextTick(place) })
watch(() => props.agents.length === 0, gone => { if (gone) hide() })

// ---------- Resting off screen ----------
let seen: IntersectionObserver | undefined
onMounted(() => {
  document.addEventListener('pointerdown', outside, true)
  window.addEventListener('scroll', replace, true)
  window.addEventListener('resize', replace)
  if ('IntersectionObserver' in window && root.value) {
    seen = new IntersectionObserver(([entry]) => { asleep.value = !entry!.isIntersecting })
    seen.observe(root.value)
  }
})
onBeforeUnmount(() => {
  clearTimeout(openTimer); clearTimeout(closeTimer); cancelAnimationFrame(frame)
  document.removeEventListener('pointerdown', outside, true)
  window.removeEventListener('scroll', replace, true)
  window.removeEventListener('resize', replace)
  seen?.disconnect()
})
</script>

<template>
  <span v-if="agents.length" ref="root" class="live" :class="[`as-${variant}`, state, { open, asleep }]" @keydown="keydown">
    <button
      ref="trigger" type="button" class="live-chip" :aria-expanded="open" :aria-controls="open ? id : undefined"
      :aria-label="`${summary}. Who works on what`" @click="toggle" @pointerenter="enter" @pointerleave="leave" @focusin="focusIn" @focusout="focusOut"
    >
      <span class="faces">
        <LiveBot v-for="agent in faces" :key="agentKey(agent)" class="face" :state="agent.state" :harness="agent.harness" :event-pulse="live.eventPulseFor(agent)" :size="variant === 'card' ? 28 : 26" />
        <span v-if="agents.length > 1" class="count mono">{{ agents.length }}</span>
      </span>
      <span v-if="variant === 'card'" class="words">
        <span class="name">{{ chip.name }}</span>
        <template v-if="chip.key"><span class="dot" /><span class="key mono">{{ chip.key }}</span></template>
        <template v-else-if="leadPhase"><span class="dot" /><span class="phase-word">{{ leadPhase }}</span></template>
        <span class="elapsed mono">{{ elapsed }}</span>
      </span>
      <span v-else-if="chip.key" class="key mono">{{ chip.key }}</span>
      <span v-else class="row-name">{{ chip.name }}</span>

    </button>
    <Teleport to="body">
      <div
        v-if="open" :id="id" ref="panel" class="live-pop floating pop" :class="{ above }" role="dialog" :aria-label="`Agents ${groupLabel} on ${project.title}`"
        :style="{ left: `${x}px`, top: `${y}px` }" @pointerenter="enter" @pointerleave="leave" @focusout="focusOut" @keydown="keydown"
      >
        <p class="pop-head">
          <span class="status-dot" :class="state" aria-hidden="true" />
          <span>{{ agents.length }} {{ agents.length === 1 ? 'agent' : 'agents' }} {{ groupLabel }}</span>
          <span class="pop-project">{{ project.title }}</span>
        </p>
        <ul class="pop-list">
          <li v-for="(agent, i) in agents" :key="agent.session_id ?? `${agent.since}${i}`" class="pop-agent">
            <component
              :is="agent.session_id ? RouterLink : 'div'" class="agent-line" :to="agent.session_id ? `/agents/${encodeURIComponent(agent.session_id)}` : undefined"
              :aria-label="agent.session_id ? `${who(agent)}, ${harnessLabel(agent.harness)}, ${phaseLabel(agent).toLowerCase()} ${agePrefix(agent)} ${elapsedFor(agent, live.serverNow)}. Open the session` : undefined"
            >
              <LiveBot :state="agent.state" :harness="agent.harness" :event-pulse="live.eventPulseFor(agent)" :size="32" />
              <span class="agent-text">
                <span class="agent-name">{{ who(agent) }}<span class="harness">{{ harnessLabel(agent.harness) }}</span></span>
                <span class="agent-meta"><span class="phase" :class="agent.state ?? agent.phase">{{ phaseLabel(agent) }}</span><span class="sep" />{{ agePrefix(agent) }} <time class="mono" :datetime="agent.since" :title="absoluteTime(agent.since)">{{ elapsedFor(agent, live.serverNow) }}</time><span class="sep" /><span class="since">since {{ clock(agent.since) }}</span></span>
              </span>
              <AppIcon v-if="agent.session_id" class="go" name="chevron-right" :size="14" />
            </component>
            <RouterLink v-if="agent.ticket && ticketHref(agent)" class="ticket-line" :to="ticketHref(agent)">
              <span class="ticket-key mono">{{ agent.ticket.key }}</span><span class="ticket-title">{{ agent.ticket.title }}</span>
            </RouterLink>
            <p v-else class="ticket-line none">{{ agent.ticket ? agent.ticket.key : 'No ticket bound' }}</p>
          </li>
        </ul>
      </div>
    </Teleport>
  </span>
</template>

<style scoped>
.live { --signal: var(--teal); position: relative; display: inline-flex; min-width: 0; max-width: 100%; }
.live.waiting { --signal: var(--warn); }
.live.stale { --signal: var(--ink-3); }
.live-chip {
  position: relative; display: inline-flex; align-items: center; gap: 7px; min-width: 0; max-width: 100%; height: 32px; padding: 0 11px 0 3px;
  border: 0; border-radius: 999px; background: color-mix(in oklab, var(--signal) 5%, var(--surface-raised)); box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--signal) 18%, transparent);
  color: var(--ink); font: 500 12px/1 var(--font); cursor: pointer; -webkit-user-select: none; user-select: none; transition: none;
}
.live-chip:hover, .live.open .live-chip { box-shadow: inset 0 0 0 1px color-mix(in oklab, var(--signal) 40%, transparent); }
/* A finger's reach: the chip answers a little beyond its edge (44px tall, and wide on a phone row). */
.live-chip::before { content: ''; position: absolute; inset: -6px -2px; border-radius: 999px; }
.live-chip:focus-visible { outline: none; box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.faces { position: relative; display: inline-flex; align-items: center; flex-shrink: 0; }
.face + .face { margin-left: -10px; }
.face:nth-child(1) { z-index: 3; } .face:nth-child(2) { z-index: 2; } .face:nth-child(3) { z-index: 1; }
/* The total accompanies overlapping robots at every size. */
.count { position: relative; z-index: 4; display: grid; place-items: center; flex-shrink: 0; min-width: 16px; height: 16px; margin-left: 3px; padding: 0 3px; border-radius: 999px; background: var(--surface-sunken); color: var(--ink-2); font-size: 10px; font-weight: 600; }
.words { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
.name, .row-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 650; letter-spacing: -.005em; }
.row-name { font-weight: 600; font-size: 11.5px; }
.dot { flex-shrink: 0; width: 3px; height: 3px; border-radius: 50%; background: var(--ink-3); }
.key { flex-shrink: 0; font-size: 11px; font-weight: 600; letter-spacing: .04em; color: var(--teal-ink); white-space: nowrap; }
.phase-word { flex-shrink: 0; color: var(--ink-2); }
.elapsed { flex-shrink: 0; font-size: 11px; color: var(--ink-3); }
.as-row .live-chip { height: 30px; gap: 6px; padding: 0 8px 0 2px; }
.as-row .face + .face { margin-left: -10px; }
/* Keep two overlapping robots and the total in the phone row's gutter. */
@media (max-width: 760px) {
  .as-row .key, .as-row .row-name { display: none; }
  .as-row .live-chip { height: 32px; padding: 0 3px; }
  .as-row .live-chip::before { inset: -6px -2px; }
}
@container live-card (max-width: 300px) { .elapsed { display: none; } }

/* ---------- The popover ---------- */
/* Placed by left and top (set once when it opens or the page moves); it arrives
   by transform and opacity only. */
.live-pop { position: fixed; z-index: 75; width: min(330px, calc(100vw - 16px)); padding: 8px; }
@media (prefers-reduced-motion: no-preference) {
  .live-pop { animation: live-pop-in .16s cubic-bezier(.2, .7, .2, 1); }
  .live-pop:not(.above) { animation-name: live-pop-in-below; }
}
@keyframes live-pop-in { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }
@keyframes live-pop-in-below { from { opacity: 0; transform: translateY(-4px); } to { opacity: 1; transform: none; } }
.pop-head { display: flex; align-items: center; gap: 8px; padding: 4px 8px 8px; font: 600 11px/1.2 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-2); font-variant-ligatures: none; }
.pop-project { margin-left: auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 50%; font: 500 12px/1.2 var(--font); letter-spacing: 0; text-transform: none; color: var(--ink-3); }
.status-dot { flex-shrink: 0; width: 6px; height: 6px; border-radius: 50%; background: var(--teal); }
.status-dot.waiting { background: var(--warn); }
.status-dot.stale { background: var(--ink-3); }
.pop-list { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.pop-agent { display: grid; gap: 2px; padding: 4px; border-radius: 10px; background: var(--surface-sunken); }
/* Hover answers at once: no colour or shadow transitions (only the compositor moves things here). */
.agent-line, .ticket-line { transition: none; }
.agent-line { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 4px 6px; border-radius: 8px; color: var(--ink); text-decoration: none; }
a.agent-line:hover, .ticket-line[href]:hover { background: var(--row-hover); }
a.agent-line:focus-visible, .ticket-line:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.agent-text { display: grid; gap: 3px; min-width: 0; flex: 1; }
.agent-name { display: flex; align-items: center; gap: 7px; min-width: 0; font-size: 13.5px; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.harness { flex-shrink: 0; padding: 2px 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 10.5px/1.2 var(--font); color: var(--ink-2); }
.agent-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 12px; color: var(--ink-2); }
.phase { font-weight: 600; color: var(--teal-ink); }
.phase.waiting { color: var(--warn); }
.phase.stale { color: var(--ink-3); }
.phase.starting, .phase.stopping { color: var(--teal-ink); }
.sep { width: 3px; height: 3px; border-radius: 50%; background: var(--ink-3); }
.agent-meta time { color: var(--ink); font-size: 11.5px; }
.since { white-space: nowrap; color: var(--ink-3); }
.go { flex-shrink: 0; color: var(--ink-3); }
a.agent-line:hover .go { color: var(--teal-ink); }
.ticket-line { display: flex; align-items: baseline; gap: 8px; min-width: 0; margin: 0; padding: 6px 8px 7px 46px; border-radius: 8px; color: var(--ink-2); font-size: 12.5px; text-decoration: none; }
.ticket-key { flex-shrink: 0; font-size: 11px; font-weight: 600; letter-spacing: .04em; color: var(--teal-ink); }
.ticket-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ticket-line.none { color: var(--ink-3); font-size: 12px; }
@media (max-width: 760px) { .ticket-line { align-items: center; min-height: 44px; } }
</style>
