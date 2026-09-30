<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { harnessLabel } from '../../lib/agentState'
import { agentKey, elapsedFor, phaseLabel, who, type LiveAgent } from '../../lib/liveAgents'
import { useLiveAgents } from '../../stores/liveAgents'
import AgentStateLabel from '../agents/AgentStateLabel.vue'
import LiveBot from '../projects/LiveBot.vue'

// The people working on one ticket right now (AEON-233): one bot and name in the
// Assignee cell, and +N when several are bound. The control sits in the row and
// never selects or opens the ticket. A withheld session stays a detail, not a link.
const props = withDefaults(defineProps<{ workers: LiveAgent[]; ticketKey: string; variant?: 'cell' | 'cue' }>(), { variant: 'cell' })
const live = useLiveAgents()
const id = useId()
const root = ref<HTMLElement>()
const trigger = ref<HTMLElement>()
const panel = ref<HTMLElement>()
const open = ref(false)
const x = ref(0)
const y = ref(0)

const lead = computed(() => props.workers[0])
const more = computed(() => Math.max(0, props.workers.length - 1))
const leadTo = computed(() => lead.value?.session_id ? `/agents/${encodeURIComponent(lead.value.session_id)}` : '')
const botSize = computed(() => props.variant === 'cue' ? 16 : 18)

function lineLabel(agent: LiveAgent) {
  const openSession = agent.session_id ? 'Open the session' : 'Session details are withheld'
  return `${who(agent)}, ${harnessLabel(agent.harness)}, ${phaseLabel(agent).toLowerCase()}. ${openSession}`
}
const leadLabel = computed(() => {
  const agent = lead.value
  if (!agent) return ''
  const limits = [
    live.truncated ? 'Some live sessions are not shown.' : '',
    live.pollStale ? 'Showing the last successful update.' : '',
  ].filter(Boolean)
  return `${lineLabel(agent)}${limits.length ? ` ${limits.join(' ')}` : ''}`
})

function place() {
  const anchor = trigger.value instanceof HTMLElement ? trigger.value : root.value
  const pop = panel.value
  if (!anchor || !pop) return
  const rect = anchor.getBoundingClientRect()
  const width = pop.offsetWidth
  const height = pop.offsetHeight
  const below = rect.bottom + height + 10 <= innerHeight || rect.top - height - 10 <= 64
  x.value = Math.round(Math.min(Math.max(8, rect.left), innerWidth - width - 8))
  y.value = Math.round(below ? Math.min(innerHeight - height - 8, rect.bottom + 6) : Math.max(8, rect.top - height - 6))
}
function show(focusInside = false) {
  if (!props.workers.length) return
  open.value = true
  void nextTick(() => { place(); if (focusInside) panel.value?.querySelector<HTMLElement>('a[href]')?.focus() })
}
function hide(restore = false) {
  if (!open.value) return
  open.value = false
  if (restore) trigger.value?.focus()
}
function leadClick(event: MouseEvent) { if (!leadTo.value) show(event.detail === 0) }
function setLeadTrigger(el: unknown) {
  if (more.value) return
  trigger.value = el instanceof HTMLElement ? el : undefined
}
function toggle(event: MouseEvent) { if (open.value) hide(); else show(event.detail === 0) }
function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) { event.preventDefault(); event.stopPropagation(); hide(true); return }
  if (event.key !== 'Enter' && event.key !== ' ') return
  const target = event.target
  if (!(target instanceof Element) || target.closest('.worker-pop')) return
  if (!target.closest('button.worker-lead, .worker-more')) return
  event.preventDefault()
  event.stopPropagation()
  if (open.value) hide(); else show(true)
}
const within = (node: EventTarget | null) => node instanceof Node && (!!root.value?.contains(node) || !!panel.value?.contains(node))
function outside(event: PointerEvent) { if (open.value && !within(event.target)) hide() }
let frame = 0
const replace = () => { if (open.value && !frame) frame = requestAnimationFrame(() => { frame = 0; place() }) }
watch(() => props.workers.length, length => { if (!length) hide(); else if (open.value) void nextTick(place) })
onMounted(() => {
  document.addEventListener('pointerdown', outside, true)
  window.addEventListener('scroll', replace, true)
  window.addEventListener('resize', replace)
})
onBeforeUnmount(() => {
  cancelAnimationFrame(frame)
  document.removeEventListener('pointerdown', outside, true)
  window.removeEventListener('scroll', replace, true)
  window.removeEventListener('resize', replace)
})
</script>

<template>
  <span v-if="lead" ref="root" class="ticket-workers" :class="`as-${variant}`" @click.stop @keydown="onKey" @dragstart.stop.prevent>
    <component
      :is="leadTo ? RouterLink : 'button'" :ref="setLeadTrigger" :to="leadTo || undefined" :type="leadTo ? undefined : 'button'"
      class="worker-lead" :data-tip="`${who(lead)} · ${phaseLabel(lead)}`" :aria-label="leadLabel" :aria-expanded="!leadTo ? open : undefined" :aria-controls="!leadTo && open ? id : undefined"
      @click.stop="leadClick"
    >
      <LiveBot :id="lead.principal_id" :state="lead.state" :harness="lead.harness" :event-pulse="live.eventPulseFor(lead)" :size="botSize" />
      <span class="worker-name">{{ who(lead) }}</span>
    </component>
    <button
      v-if="more" ref="trigger" type="button" class="worker-more" :aria-expanded="open" :aria-controls="open ? id : undefined"
      :aria-label="`${more} more ${more === 1 ? 'worker' : 'workers'} on ${ticketKey}. Show each worker`" @click.stop="toggle"
    >+{{ more }}</button>
    <Teleport to="body">
      <div v-if="open" :id="id" ref="panel" class="worker-pop pop" role="dialog" :aria-label="`Workers on ${ticketKey}`" :style="{ left: `${x}px`, top: `${y}px` }" @click.stop @keydown="onKey">
        <ul>
          <li v-for="(agent, i) in workers" :key="agentKey(agent)">
            <component
              :is="agent.session_id ? RouterLink : 'div'" class="worker-line" :to="agent.session_id ? `/agents/${encodeURIComponent(agent.session_id)}` : undefined"
              :aria-label="agent.session_id ? lineLabel(agent) : undefined"
            >
              <LiveBot :id="agent.principal_id" :index="i" :state="agent.state" :harness="agent.harness" :event-pulse="live.eventPulseFor(agent)" :size="28" />
              <span class="worker-copy">
                <span class="worker-name">{{ who(agent) }}<span class="harness">{{ harnessLabel(agent.harness) }}</span></span>
                <span class="worker-meta"><AgentStateLabel :state="agent.state ?? 'working'" /><span class="age">{{ elapsedFor(agent, live.serverNow) }}</span></span>
              </span>
            </component>
            <p v-if="!agent.session_id" class="withheld">Session details are withheld</p>
          </li>
        </ul>
        <p v-if="live.truncated" class="limit">Some live sessions are not shown.</p>
        <p v-if="live.pollStale" class="limit">Showing the last successful update.</p>
      </div>
    </Teleport>
  </span>
</template>

<style scoped>
.ticket-workers { display: inline-flex; align-items: center; gap: 4px; min-width: 0; max-width: 100%; }
.worker-lead {
  display: inline-flex; align-items: center; gap: 4px; min-width: 0; max-width: 100%; height: 22px; padding: 0 2px 0 0;
  border: 0; border-radius: 999px; background: transparent; color: var(--ink); font: 600 12.5px/1 var(--font); text-decoration: none; cursor: pointer;
}
.worker-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.worker-more {
  flex-shrink: 0; height: 18px; min-width: 18px; padding: 0 4px; border: 0; border-radius: 999px;
  background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 10px/18px var(--mono); cursor: pointer;
}
.worker-lead:hover, .worker-more:hover { background: var(--row-hover); }
.worker-lead:focus-visible, .worker-more:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.as-cue { flex-shrink: 0; min-width: max-content; max-width: none; }
.as-cue .worker-lead { flex-shrink: 0; min-width: max-content; max-width: none; height: 20px; padding: 0 6px 0 1px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 12px; }
.as-cue .worker-name { flex-shrink: 0; min-width: max-content; overflow: visible; text-overflow: clip; }
.as-cue .worker-lead:focus-visible { box-shadow: inset 0 0 0 1px var(--chip-line), var(--focus-ring); }
.worker-pop { position: fixed; z-index: 80; width: min(280px, calc(100vw - 16px)); max-height: min(320px, calc(100vh - 16px)); overflow: auto; padding: 6px; }
.worker-pop ul { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.worker-pop li { display: grid; gap: 2px; padding: 4px; border-radius: 10px; background: var(--surface-sunken); }
.worker-line { display: flex; align-items: center; gap: 8px; min-height: 44px; padding: 2px 4px; border-radius: 8px; color: var(--ink); text-decoration: none; }
a.worker-line:hover { background: var(--row-hover); }
a.worker-line:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.worker-copy { display: grid; gap: 2px; min-width: 0; }
.worker-copy .worker-name { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 650; }
.harness { flex-shrink: 0; padding: 1px 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 10px/1.4 var(--font); color: var(--ink-2); }
.worker-meta { display: flex; align-items: center; gap: 6px; color: var(--ink-2); font-size: 12px; }
.age { font-family: var(--mono); font-size: 11px; color: var(--ink-3); }
.withheld, .limit { margin: 0; padding: 0 6px 4px; font-size: 11.5px; color: var(--ink-3); }
.limit { padding: 6px 6px 2px; }
</style>
