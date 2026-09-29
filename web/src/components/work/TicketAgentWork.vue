<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import DeliveryRating from './DeliveryRating.vue'
import { loadNodeRatings, type SessionRating } from '../../lib/deliveryRating'
import {
  formatTokenCount, formatUsd, formatWorkDuration, harnessLabel, loadTicketAgentWork,
  type TicketAgentSession, type TicketAgentUsageModel, type TicketAgentWork,
} from '../../lib/ticketAgentWork'

// Sessions that worked on this ticket, epic or task, including visible
// descendants. A session with several models is counted once. Missing tokens,
// cost, model or effort are left out, never shown as zero. With no sessions the
// section is not shown at all (unless the query was capped).
const props = defineProps<{ nodeId: string; kind: string }>()

const report = ref<TicketAgentWork | null>(null)
const ratings = ref(new Map<string, SessionRating>())
const error = ref('')
const loading = ref(false)
let generation = 0
let abort: AbortController | undefined

watch(() => props.nodeId, id => { void load(id) }, { immediate: true })
onBeforeUnmount(() => abort?.abort())

async function load(id: string) {
  const request = ++generation
  abort?.abort()
  abort = new AbortController()
  report.value = null
  ratings.value = new Map()
  error.value = ''
  loading.value = true
  try {
    const [next, rated] = await Promise.all([
      loadTicketAgentWork(id, abort.signal),
      loadNodeRatings(id, abort.signal),
    ])
    if (request === generation) {
      report.value = next
      const map = new Map<string, SessionRating>()
      for (const row of rated ?? []) map.set(row.session_id, row)
      ratings.value = map
    }
  } catch (cause) {
    if (request !== generation || abort?.signal.aborted) return
    error.value = cause instanceof Error ? cause.message : 'Agent work could not be loaded.'
  } finally {
    if (request === generation) loading.value = false
  }
}

const noun = computed(() => props.kind === 'epic' ? 'epic' : props.kind === 'task' ? 'task' : 'ticket')
const rollup = computed(() => {
  if (!report.value?.includes_descendants || !report.value.sessions.length) return ''
  if (props.kind === 'epic') return 'Totals include this epic and the tickets and tasks under it.'
  if (props.kind === 'ticket') return 'Totals include this ticket and the tasks under it.'
  return 'Totals include this task and the work under it.'
})

const known = (...parts: string[]) => parts.filter(Boolean).join(' · ')
const duration = (seconds: number | null, state: string) => { const text = formatWorkDuration(seconds, state); return text === 'Time unknown' ? '' : text }

function costLabel(amount: string | null, state: string): string {
  if (state === 'unknown' || amount == null) return ''
  const text = formatUsd(amount)
  if (!text) return ''
  if (state === 'provisional') return `${text} provisional`
  if (state === 'partial') return `${text}, incomplete`
  return `${text} estimated`
}

function tokenLabel(input: string | null, output: string | null, cached: string | null, state: string, cachedState: string): string {
  if (state === 'unknown' || input == null || output == null) return ''
  const parts = [`${formatTokenCount(input)} in`, `${formatTokenCount(output)} out`]
  if (cachedState !== 'unknown' && cached != null) parts.push(`${formatTokenCount(cached)} cached`)
  const text = parts.join(' · ')
  return state === 'partial' || cachedState === 'partial' ? `${text}, incomplete` : text
}

const summary = computed(() => {
  const totals = report.value?.totals
  if (!totals) return ''
  const count = `${totals.session_count} ${totals.session_count === 1 ? 'session' : 'sessions'}`
  const time = duration(totals.duration_seconds, totals.duration_state)
  if (!report.value?.usage_available) return known(count, time)
  return known(count, time, tokenLabel(totals.input_tokens, totals.output_tokens, totals.cached_input_tokens, totals.tokens_state, totals.cached_state), costLabel(totals.estimated_cost_usd, totals.cost_state))
})

function displayModel(session: TicketAgentSession): string {
  if (session.models.length === 1) return session.models[0].model
  if (session.models.length > 1) return `${session.models.length} models`
  if (session.model_state === 'known' && session.model) return session.model
  return ''
}

function rowMeta(session: TicketAgentSession): string {
  const effort = session.effort_state === 'known' && session.effort ? session.effort : ''
  // An unlabelled session is already named after its harness.
  return known(session.label ? harnessLabel(session.harness) : '', displayModel(session), effort, duration(session.duration_seconds, session.duration_state))
}

function rowUsage(session: TicketAgentSession): string {
  return known(tokenLabel(session.input_tokens, session.output_tokens, session.cached_input_tokens, session.tokens_state, session.cached_state), costLabel(session.estimated_cost_usd, session.cost_state))
}

function modelCost(model: TicketAgentUsageModel): string {
  if (model.cost_state !== 'estimated' || model.estimated_cost_usd == null) return ''
  const amount = formatUsd(model.estimated_cost_usd)
  if (!amount) return ''
  return model.provisional ? `${amount} provisional` : `${amount} estimated`
}

function modelFigures(model: TicketAgentUsageModel): string {
  const tokens = tokenLabel(model.input_tokens, model.output_tokens, model.cached_input_tokens, model.tokens_state, model.cached_state)
  const bill = model.billing_mode === 'subscription' ? 'subscription' : model.billing_mode === 'api' ? 'API' : ''
  const figures = known(tokens, modelCost(model))
  return figures ? known(figures, bill) : ''
}

function priceTip(model: TicketAgentUsageModel): string {
  const version = model.price_version ? `List price version ${model.price_version}` : 'List price'
  return `${version}. Not a billed charge.`
}

function rowName(session: TicketAgentSession): string {
  return session.label || harnessLabel(session.harness)
}
</script>

<template>
  <section v-if="!loading && (error || (report && (report.sessions.length || report.scope_truncated)))" class="agent-work" aria-label="Agent work">
    <header class="section-head">
      <h3 class="eyebrow"><AppIcon name="agent" :size="12" />Agent work</h3>
    </header>
    <p v-if="error" class="note warn" role="alert">{{ error }} <button type="button" class="retry" @click="load(nodeId)">Try again</button></p>
    <template v-else-if="report">
      <template v-if="!report.sessions.length">
        <p class="note">No agent sessions are recorded in the included items.</p>
        <p class="note">Some items under this {{ noun }} were left out of the query.</p>
      </template>
      <template v-else>
        <p v-if="report.sessions.length > 1 || report.includes_descendants" class="summary">{{ summary }}</p>
        <p v-if="rollup" class="note">{{ rollup }}</p>
        <p v-if="report.totals.tokens_state === 'partial' || report.totals.cost_state === 'partial'" class="note">Totals count reported figures only.</p>
        <p v-if="report.scope_truncated" class="note">Some items under this {{ noun }} were left out of the query.</p>
        <p v-if="report.list_truncated" class="note">Showing the {{ report.sessions.length }} most recent sessions. Totals cover only those.</p>
        <ul>
          <li v-for="session in report.sessions" :key="session.id">
            <div class="session-top">
              <RouterLink class="name" :to="`/agents/${session.id}`" :aria-label="`${rowName(session)}. Open the session`">{{ rowName(session) }}</RouterLink>
              <span v-if="session.ticket_node_id !== nodeId" class="ticket-key">{{ session.ticket_key }}</span>
              <span v-if="session.models.length === 1 && session.models[0]?.price_version" class="price" :data-tip="priceTip(session.models[0])">price v{{ session.models[0].price_version }}</span>
            </div>
            <p class="meta">{{ rowMeta(session) }}</p>
            <p v-if="report.usage_available && session.models.length > 1 && rowUsage(session)" class="meta figures">{{ rowUsage(session) }}</p>
            <p v-else-if="report.usage_available && session.models.length === 1 && session.models[0] && modelFigures(session.models[0])" class="meta figures">{{ modelFigures(session.models[0]) }}</p>
            <p v-if="session.models_truncated" class="note">Some models for this session were left out, so its figures are incomplete.</p>
            <ul v-if="session.models.length > 1" class="models" :aria-label="`Models for ${rowName(session)}`">
              <li v-for="model in session.models" :key="model.model">
                <span class="model-name">{{ model.model }}<span v-if="model.price_version" class="price" :data-tip="priceTip(model)"> · price v{{ model.price_version }}</span></span>
                <span v-if="modelFigures(model)">{{ modelFigures(model) }}</span>
              </li>
            </ul>
            <DeliveryRating v-if="session.phase === 'stopped' && ratings.get(session.id)" :session-id="session.id" :initial="ratings.get(session.id)" />
          </li>
        </ul>
      </template>
    </template>
  </section>
</template>

<style scoped>
.agent-work { min-width: 0; }
.section-head { display: flex; align-items: center; min-height: 26px; margin-bottom: 8px; }
.eyebrow { display: inline-flex; align-items: center; gap: 6px; margin: 0; }
.summary { margin: 0 0 6px; font-size: 13px; line-height: 1.45; color: var(--ink); }
.note { margin: 0 0 8px; font-size: 12.5px; line-height: 1.45; color: var(--ink-3); }
.note.warn { color: var(--ink-2); }
.retry { margin-left: 6px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; cursor: pointer; }
.retry:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
ul { display: grid; gap: 8px; margin: 8px 0 0; padding: 0; list-style: none; }
li { min-width: 0; padding: 10px 12px; border-radius: 10px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line); }
.session-top { display: flex; align-items: center; gap: 8px; min-width: 0; }
.name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 13.5px; font-weight: 600; text-decoration: none; }
@media (hover: hover) { .name:hover { color: var(--teal-ink); } }
.name:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.ticket-key { flex: none; font-family: var(--mono); font-size: 11px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.meta { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; margin: 3px 0 0; font-size: 12.5px; line-height: 1.4; color: var(--ink-2); overflow-wrap: anywhere; }
.figures { color: var(--ink-3); }
.price { flex: none; color: var(--ink-3); font-family: var(--mono); font-size: 11px; white-space: nowrap; }
.model-name .price { font-size: inherit; }
.models { gap: 6px; margin: 8px 0 0; }
.models li { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; margin: 0; padding: 0; background: transparent; box-shadow: none; border-radius: 0; font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.model-name { min-width: 0; overflow-wrap: anywhere; font-family: var(--mono); font-size: 11.5px; color: var(--ink-2); }
</style>
