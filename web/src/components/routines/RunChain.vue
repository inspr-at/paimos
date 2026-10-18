<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { getNode, getRoutineRun } from '../../lib/api'
import { when } from '../../lib/recurrences'
import { STEP_ICON, checkedDetail, runChain, type ChainTicket, type RoutineRun, type RunDetail } from '../../lib/routineRuns'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// One run, opened in the logbook (AEON-1192): ticket → agent → actions → check →
// person → settled, from S21's bounded evidence. A late answer for another run, or
// for the run before its state moved on, never paints here.
const props = defineProps<{ recurrenceId: string; run: RoutineRun; projectKey: string; zone: string }>()
const scope = useIdentityScope(), evidence = scope.lane(), tickets = scope.lane()
const detail = ref<RunDetail | null>(null), failure = ref(''), ticket = ref<ChainTicket | 'loading' | 'missing' | null>(null)
function load() {
  const id = props.recurrenceId, runId = props.run.id
  failure.value = ''
  void evidence.run(({ after, signal }) => after(getRoutineRun(id, runId, signal), raw => {
    const found = checkedDetail(raw, runId)
    if (props.run.id === runId && props.recurrenceId === id) detail.value = found
  }), { failed: error => { if (props.run.id === runId) failure.value = error instanceof Error ? error.message : 'The run could not be read.' } })
}
function loadTicket() {
  const nodeId = props.run.work_node_id
  if (!nodeId) { tickets.cancel(); ticket.value = null; return }
  if (ticket.value && ticket.value !== 'missing' && ticket.value !== 'loading') return
  ticket.value = 'loading'
  void tickets.run(({ after }) => after(getNode(nodeId), node => {
    if (props.run.work_node_id !== nodeId) return
    ticket.value = { key: node.key, title: node.title, state: node.state, number: node.recurrence?.number ?? null, projectKey: node.recurrence?.project_key || props.projectKey }
  }), { failed: () => { if (props.run.work_node_id === nodeId) ticket.value = 'missing' } })
}
// The run's own state moving on (the list's poll) reads its evidence again.
watch(() => `${props.recurrenceId}/${props.run.id}`, () => { detail.value = null; ticket.value = null }, { flush: 'sync' })
watch(() => `${props.recurrenceId}/${props.run.id}/${props.run.state}/${props.run.updated_at}`, () => { load(); loadTicket() }, { immediate: true })
const chain = computed(() => detail.value ? runChain(detail.value, { ticket: ticket.value, when: at => when(at, props.zone), stateWord: state => statusMeta(state).label.toLowerCase() }) : null)
</script>

<template>
  <div class="chain-wrap">
    <p v-if="failure" class="chain-note" role="alert"><AppIcon name="alert" :size="13" /><span>This run’s evidence couldn’t be read: {{ failure }}</span><button type="button" class="btn sm ghost" @click="load">Try again</button></p>
    <p v-else-if="!chain" class="chain-note" role="status"><span class="skeleton" aria-hidden="true" /><span class="sr-only">Reading the run…</span></p>
    <template v-else>
      <ol class="chain">
        <li v-for="step in chain.steps" :key="step.id" class="stp" :class="step.tone" :data-step="step.label.toLowerCase()">
          <span class="s-ic" aria-hidden="true"><span v-if="STEP_ICON[step.tone] === 'live'" class="pulse" /><AppIcon v-else :name="STEP_ICON[step.tone] as 'check'" :size="11" /></span>
          <span class="s-l">{{ step.label }}</span>
          <span class="s-t"><template v-for="(part, index) in step.parts" :key="index"><RouterLink v-if="part.kind === 'key'" :to="part.to" class="key">{{ part.text }}</RouterLink><RouterLink v-else-if="part.kind === 'go'" :to="part.to" class="go">{{ part.text }}<AppIcon name="chevron-right" :size="12" /></RouterLink><b v-else-if="part.kind === 'strong'">{{ part.text }}</b><span v-else-if="part.kind === 'code'" class="code">{{ part.text }}</span><template v-else>{{ part.text }}</template></template></span>
        </li>
      </ol>
      <p v-if="chain.truncated" class="chain-note"><AppIcon name="info" :size="13" /><span>This run has more attempts, actions or effects than shown here.</span></p>
    </template>
  </div>
</template>

<style scoped>
.chain-wrap { margin: 4px 0 10px 18px; }
.chain { display: grid; margin: 0; padding: 4px 0; list-style: none; }
.stp { position: relative; display: grid; grid-template-columns: 20px 64px minmax(0, 1fr); gap: 10px; align-items: start; padding: 6px 0; font-size: 12.5px; line-height: 1.5; color: var(--ink); }
.stp:not(:last-child)::before { content: ''; position: absolute; left: 9.5px; top: 26px; bottom: -6px; width: 1px; background: var(--line-2); }
.s-ic { display: grid; place-items: center; width: 20px; height: 20px; border-radius: 50%; color: var(--ink-2); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); }
.stp.bad .s-ic { color: var(--danger); background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.stp.warn .s-ic, .stp.wait .s-ic, .stp.hold .s-ic { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.stp.live .s-ic { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.stp.off { color: var(--ink-3); }
.s-l { padding-top: 1px; font: 500 10px/1.9 var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); }
.s-t { min-width: 0; overflow-wrap: anywhere; }
.s-t b { font-weight: 650; }
.stp.bad .s-t b { color: var(--danger); }
.key { margin-right: 2px; font: 600 11.5px var(--mono); color: var(--teal-ink); text-decoration: none; }
.go { display: inline-flex; align-items: center; gap: 2px; white-space: nowrap; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; text-decoration: none; }
.key:hover, .go:hover { text-decoration: underline; text-underline-offset: 2px; }
.code { font: 500 11px/1 var(--mono); color: var(--ink-3); white-space: nowrap; }
.chain-note { display: flex; align-items: center; gap: 8px; min-height: 32px; padding: 4px 0; font-size: 12px; color: var(--ink-3); }
.chain-note > svg { flex: none; }
.chain-note .skeleton { width: min(60%, 320px); }
.pulse { position: relative; display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); }
@media (prefers-reduced-motion: no-preference) {
  .pulse::after { content: ''; position: absolute; inset: -3px; border-radius: 50%; box-shadow: 0 0 0 1.5px var(--teal); opacity: 0; animation: ring 1.8s ease-out infinite; }
  @keyframes ring { 0% { transform: scale(.6); opacity: .8; } 100% { transform: scale(1.5); opacity: 0; } }
}
@container logbook (max-width: 560px) {
  .chain-wrap { margin-left: 0; }
  .stp { grid-template-columns: 20px minmax(0, 1fr); }
  .s-l, .s-t { grid-column: 2; }
  .s-l { line-height: 1.5; }
  .stp:not(:last-child)::before { display: none; }
}
@media (pointer: coarse) { .go, .key { display: inline-flex; align-items: center; min-height: 44px; } }
</style>
