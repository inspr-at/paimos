<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import { loadUsageDashboard } from '../lib/usageDashboard'
import { billingLabel, costStateLabel, formatCount, formatOptionalCount, formatTokens, formatUSD, formatWhen, paceLabel, rangeBounds, unitLabel, usdUnits, type UsageDashboard, type UsageGroup, type UsageTicket } from '../lib/usageFormat'
import { useProjects } from '../stores/projects'
import { useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const projects = useProjects()
const session = useSession()
const dashboard = ref<UsageDashboard | null>(null)
const loadedDays = ref<number | null>(null)
const loadedProject = ref<string | null>(null)
const loading = ref(true)
const error = ref('')
const ranges = [7, 30, 90] as const

const days = computed(() => ranges.find(value => String(value) === route.query.days) ?? 30)
const projectId = computed(() => typeof route.query.project === 'string' ? route.query.project : '')
const openProjects = computed(() => projects.projects.filter(project => !project.archived))
// Figures stay visible only for the selection they were loaded with.
const visible = computed(() => dashboard.value && loadedDays.value === days.value && loadedProject.value === projectId.value ? dashboard.value : null)

function setQuery(patch: { days?: number; project?: string }) {
  const next: Record<string, string> = {}
  const chosen = patch.days ?? days.value
  if (chosen !== 30) next.days = String(chosen)
  const project = patch.project !== undefined ? patch.project : projectId.value
  if (project) next.project = project
  void router.replace({ path: '/agents/usage', query: next })
}

// A slower read for an earlier project or range must not paint, fail, or
// finish over the selection the user has now. Leaving the page cancels it.
let generation = 0
let controller: AbortController | null = null
function stale(request: number, signal: AbortSignal, cause?: unknown) {
  return request !== generation || signal.aborted || (cause instanceof Error && cause.name === 'AbortError')
}
async function load() {
  const request = ++generation
  const previous = controller
  previous?.abort()
  const current = controller = new AbortController()
  const requestedDays = days.value
  const requestedProject = projectId.value
  loading.value = true
  error.value = ''
  const bounds = rangeBounds(requestedDays, new Date())
  try {
    const next = await loadUsageDashboard({ ...bounds, project: requestedProject || undefined }, current.signal)
    if (stale(request, current.signal)) return
    dashboard.value = next
    loadedDays.value = requestedDays
    loadedProject.value = requestedProject
  } catch (cause) {
    if (stale(request, current.signal, cause)) return
    error.value = cause instanceof Error ? cause.message : 'Usage could not be loaded.'
  } finally {
    if (!stale(request, current.signal)) loading.value = false
  }
}

onMounted(() => { void projects.load() })
onBeforeUnmount(() => {
  generation++
  controller?.abort()
  controller = null
})
watch([days, projectId], () => { void load() }, { immediate: true })

function ticketHref(ticket: UsageTicket) {
  const project = projects.byId(ticket.project_id)
  const key = project?.routeKey ?? ticket.project_key
  return `/p/${encodeURIComponent(key)}/${encodeURIComponent(ticket.key ?? '')}`
}
function tokenLine(group: UsageGroup, which: 'input' | 'output' | 'cached') {
  if (which === 'input') return formatTokens(group.input_tokens, group.input_known_rows, group.input_unknown_rows)
  if (which === 'output') return formatTokens(group.output_tokens, group.output_known_rows, group.output_unknown_rows)
  return formatTokens(group.cached_input_tokens, group.cached_input_known_rows, group.cached_input_unknown_rows)
}
const maxTrend = computed(() => {
  const amounts = visible.value?.trend.map(point => usdUnits(point.group.estimated_cost_usd)) ?? []
  return amounts.reduce((max, value) => value > max ? value : max, 1n)
})
function meterWidth(value: string | null) {
  const amount = usdUnits(value)
  if (amount <= 0n || maxTrend.value <= 0n) return 0
  const width = Number((amount * 100n) / maxTrend.value)
  return Math.max(4, Math.min(100, width))
}
const groups = computed(() => visible.value ? [
  { id: 'project', title: 'By project', rows: visible.value.by_project },
  { id: 'model', title: 'By model', rows: visible.value.by_model },
  { id: 'subscription', title: 'By subscription', rows: visible.value.by_subscription },
] : [])
</script>

<template>
  <section class="usage-page" :aria-busy="loading" aria-labelledby="usage-title">
    <header class="page-head">
      <div class="head-main">
        <p class="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</p>
        <h1 id="usage-title">Usage</h1>
        <p class="summary">Lifetime usage for sessions that started in the selected range. These are whole-session totals, not spend consumed in that range. The list price is an estimate in USD. A reported subscription is not verified coverage. Unknown is not zero.</p>
      </div>
      <div class="head-side">
        <RouterLink class="context-link" to="/agents"><AppIcon name="arrow-left" :size="13" />Agents</RouterLink>
      </div>
    </header>

    <div class="toolbar">
      <div class="seg" role="group" aria-label="Time range">
        <button v-for="value in ranges" :key="value" type="button" :aria-pressed="days === value" @click="setQuery({ days: value })">{{ value }} days</button>
      </div>
      <label class="project">
        <span>Project</span>
        <select class="field" :value="projectId" @change="setQuery({ project: ($event.target as HTMLSelectElement).value })">
          <option value="">All visible projects</option>
          <option v-for="project in openProjects" :key="project.id" :value="project.id">{{ project.title }}</option>
        </select>
      </label>
      <p v-if="visible" class="range">{{ formatWhen(visible.from) }} – {{ formatWhen(visible.to) }} <span>UTC</span></p>
    </div>

    <p v-if="loading && !visible" class="state-line" role="status"><span class="skeleton" />Loading usage</p>
    <p v-else-if="loading" class="state-line" role="status">Updating usage</p>
    <div v-if="error" class="notice" role="alert">
      <AppIcon name="alert" :size="16" />
      <p>{{ error }}</p>
      <button type="button" class="btn sm" @click="load()">Try again</button>
    </div>
    <template v-if="visible">
      <p v-if="visible.truncated" class="notice">
        <AppIcon name="alert" :size="16" />
        <span>This range has more sessions than one read holds. The totals are incomplete.</span>
      </p>

      <section class="glass-card summary-card" aria-labelledby="usage-summary-title">
        <h2 id="usage-summary-title">{{ formatCount(visible.totals.sessions) }} {{ visible.totals.sessions === 1 ? 'session' : 'sessions' }} started</h2>
        <dl class="facts">
          <div><dt>Input tokens</dt><dd>{{ tokenLine(visible.totals, 'input') }}</dd></div>
          <div><dt>Output tokens</dt><dd>{{ tokenLine(visible.totals, 'output') }}</dd></div>
          <div><dt>Cached input</dt><dd>{{ tokenLine(visible.totals, 'cached') }}</dd></div>
          <div><dt>List estimate</dt><dd>{{ formatUSD(visible.totals.estimated_cost_usd) }}</dd></div>
        </dl>
        <p class="flags">
          <span class="chip">{{ costStateLabel[visible.totals.cost_state] }}</span>
          <span v-if="visible.totals.provisional_rows">{{ formatCount(visible.totals.provisional_rows) }} provisional</span>
          <span v-if="visible.totals.cost_unknown_rows">{{ formatCount(visible.totals.cost_unknown_rows) }} unknown cost</span>
          <span v-if="visible.totals.unreported_sessions">{{ formatCount(visible.totals.unreported_sessions) }} unreported</span>
        </p>
      </section>

      <section class="block" aria-labelledby="trend-title">
        <h2 id="trend-title">Sessions started</h2>
        <p class="hint">Lifetime usage of the sessions that started on each UTC day. Not spend during that day.</p>
        <p v-if="!visible.trend.length" class="empty">No sessions started in this range.</p>
        <div v-else class="scroll">
          <table class="grid">
            <caption class="sr-only">Lifetime usage of sessions started each UTC day</caption>
            <thead><tr><th>Day</th><th>Sessions</th><th>List estimate</th><th>State</th></tr></thead>
            <tbody>
              <tr v-for="point in visible.trend" :key="point.day">
                <td>{{ formatWhen(point.day) }}</td>
                <td class="num">{{ formatCount(point.group.sessions) }}</td>
                <td>
                  <span class="cost">{{ formatUSD(point.group.estimated_cost_usd) }}</span>
                  <span v-if="point.group.estimated_cost_usd" class="meter" aria-hidden="true"><i :style="{ width: `${meterWidth(point.group.estimated_cost_usd)}%` }" /></span>
                </td>
                <td><span class="chip">{{ costStateLabel[point.group.cost_state] }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-for="block in groups" :key="block.id" class="block" :aria-labelledby="`usage-${block.id}`">
        <h2 :id="`usage-${block.id}`">{{ block.title }}</h2>
        <p v-if="block.id === 'subscription'" class="hint">Grouped by the reported billing mode and subscription label. The list estimate stays either way.</p>
        <p v-if="!block.rows.length" class="empty">Nothing in this range.</p>
        <div v-else class="scroll">
          <table class="grid">
            <caption class="sr-only">{{ block.title }}</caption>
            <thead><tr><th>Name</th><th>Sessions</th><th>Input</th><th>List estimate</th><th>State</th></tr></thead>
            <tbody>
              <tr v-for="group in block.rows" :key="group.label + (group.key ?? '')">
                <td>{{ group.label }} <span v-if="block.id === 'subscription' && group.billing_mode" class="quiet">{{ billingLabel[group.billing_mode] }}</span></td>
                <td class="num">{{ formatCount(group.sessions) }}</td>
                <td class="num">{{ tokenLine(group, 'input') }}</td>
                <td>{{ formatUSD(group.estimated_cost_usd) }}</td>
                <td><span class="chip">{{ costStateLabel[group.cost_state] }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="block" aria-labelledby="tickets-title">
        <h2 id="tickets-title">Most expensive tickets</h2>
        <p class="hint">Ranked by the known list estimate for sessions that started in this range. A ticket whose estimate is entirely unknown is counted and not ranked.</p>
        <p v-if="!visible.tickets.length" class="empty">No ticket in this range has a known list estimate.</p>
        <div v-else class="scroll">
          <table class="grid">
            <caption class="sr-only">Tickets with the highest known list estimate</caption>
            <thead><tr><th>Ticket</th><th>Sessions</th><th>List estimate</th><th>Tokens</th><th>State</th></tr></thead>
            <tbody>
              <tr v-for="ticket in visible.tickets" :key="ticket.id">
                <td><RouterLink class="ticket-link" :to="ticketHref(ticket)"><span class="key">{{ ticket.key }}</span> {{ ticket.label }}</RouterLink></td>
                <td class="num">{{ formatCount(ticket.sessions) }}</td>
                <td>{{ formatUSD(ticket.estimated_cost_usd) }}</td>
                <td class="quiet">{{ tokenLine(ticket, 'input') }} in · {{ tokenLine(ticket, 'output') }} out</td>
                <td><span class="chip">{{ costStateLabel[ticket.cost_state] }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="visible.tickets_cost_unknown" class="hint">{{ formatCount(visible.tickets_cost_unknown) }} with unknown cost.</p>
      </section>

      <section class="block" aria-labelledby="allowance-title">
        <h2 id="allowance-title">Allowance</h2>
        <p v-if="visible.allowance.state === 'withheld'" class="empty">Registered allowance windows are visible to workspace admins.</p>
        <p v-else-if="visible.allowance.state === 'none'" class="empty">No registered allowance window is open.</p>
        <template v-else>
          <p class="hint">Declared allowance, explicit reservations and the pace cap stay visible. Used, headroom and hard left are unknown while a window is provisional, including mixed settled evidence.</p>
          <div class="scroll">
            <table class="grid">
              <caption class="sr-only">Open registered allowance windows</caption>
              <thead><tr><th>Account</th><th>Unit</th><th>Allowance</th><th>Used</th><th>Reserved</th><th>Pace cap</th><th>Headroom</th><th>Hard left</th><th>Pace</th></tr></thead>
              <tbody>
                <tr v-for="window in visible.allowance.windows" :key="window.window_id">
                  <td>{{ window.label }} <span class="quiet">{{ window.harness }}</span></td>
                  <td>{{ unitLabel[window.unit] }}</td>
                  <td class="num">{{ formatCount(window.allowance) }}</td>
                  <td class="num">{{ formatOptionalCount(window.used) }}</td>
                  <td class="num">{{ formatCount(window.reserved) }}</td>
                  <td class="num">{{ formatOptionalCount(window.pace_cap) }}</td>
                  <td class="num">{{ formatOptionalCount(window.headroom) }}</td>
                  <td class="num">{{ formatOptionalCount(window.hard_remaining) }}</td>
                  <td>{{ paceLabel[window.pace_model] }}<template v-if="window.provisional"> · Provisional</template></td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </section>
    </template>
  </section>
</template>

<style scoped>
.usage-page { width: 100%; margin: 0 auto; padding: 22px var(--gutter) 32px; max-width: 1100px; }
.page-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; margin-bottom: 16px; }
.page-head h1 { margin-top: 6px; }
.summary { margin-top: 6px; max-width: 68ch; font-size: 13.5px; color: var(--ink-2); }
.head-side { display: flex; align-items: center; }
.context-link { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border-radius: 999px; color: var(--teal-ink); font-size: 13px; font-weight: 600; }
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 16px; margin-bottom: 16px; }
.project { display: inline-flex; align-items: center; gap: 8px; color: var(--ink-2); font-size: 13px; }
.project .field { min-width: 12rem; height: 32px; }
.range { margin-left: auto; color: var(--ink-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.range span { color: var(--ink-3); }
.state-line { display: flex; align-items: center; gap: 10px; color: var(--ink-2); }
.state-line .skeleton { width: 140px; }
.notice { display: flex; align-items: flex-start; gap: 8px; margin-bottom: 12px; padding: 12px 14px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); font-size: 13.5px; }
.notice svg { flex-shrink: 0; margin-top: 1px; color: var(--ink-2); }
.summary-card { padding: 16px 18px 14px; margin-bottom: 18px; }
.summary-card h2 { font-size: 18px; font-weight: 600; }
.facts { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px 16px; margin: 14px 0 0; }
.facts div { min-width: 0; }
.facts dt { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.facts dd { margin: 4px 0 0; font-size: 14px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.flags { display: flex; flex-wrap: wrap; gap: 8px 12px; align-items: center; margin-top: 12px; color: var(--ink-2); font-size: 12.5px; }
.chip { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 600 10px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; color: var(--ink-2); font-variant-ligatures: none; }
.block { margin-top: 22px; }
.block h2 { font-size: 15px; font-weight: 650; margin-bottom: 8px; }
.hint, .empty { font-size: 13px; color: var(--ink-2); }
.hint { margin-bottom: 8px; }
.scroll { overflow-x: auto; }
.grid { width: 100%; border-collapse: separate; border-spacing: 0; font-size: 13.5px; }
.grid th { height: 32px; padding: 0 10px; text-align: left; font: 500 10.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); border-bottom: 1px solid var(--line-2); font-variant-ligatures: none; white-space: nowrap; }
.grid td { padding: 10px; border-bottom: 1px solid var(--line); vertical-align: top; }
.grid tr:hover td { background: var(--row-hover); }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.quiet { color: var(--ink-2); }
.key { font: 500 12px/1 var(--mono); color: var(--ink-2); margin-right: 6px; }
.ticket-link { color: var(--ink); font-weight: 600; }
.cost { display: block; }
.meter { display: block; height: 4px; margin-top: 6px; border-radius: 999px; background: var(--track); overflow: hidden; }
.meter i { display: block; height: 100%; border-radius: inherit; background: var(--teal); }
@media (hover: hover) { .context-link:hover, .ticket-link:hover { color: var(--teal-ink); } }
.context-link:focus-visible, .ticket-link:focus-visible, .project .field:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 800px) {
  .facts { grid-template-columns: 1fr 1fr; }
  .range { margin-left: 0; }
}
@media (max-width: 600px) {
  .usage-page { padding: 16px 12px 24px; }
  .page-head { flex-direction: column; align-items: stretch; gap: 8px; }
  .facts { grid-template-columns: 1fr; }
  .project { width: 100%; }
  .project .field { flex: 1; min-width: 0; height: 44px; }
}
</style>
