<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import { loadUsageDashboard } from '../lib/usageDashboard'
import UsageTrendChart from '../components/usage/UsageTrendChart.vue'
import { formatRating } from '../lib/deliveryRating'
import { billingLabel, compactCount, costStateLabel, formatAllowanceAmount, formatCount, formatTokens, formatUSD, formatWhen, paceLabel, rangeBounds, unitLabel, type AllowanceWindow, type CostState, type UsageDashboard, type UsageGroup, type UsageTicket } from '../lib/usageFormat'
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
type Which = 'input' | 'output' | 'cached'
function tokenParts(group: UsageGroup, which: Which) {
  if (which === 'input') return { value: group.input_tokens, known: group.input_known_rows, unknown: group.input_unknown_rows }
  if (which === 'output') return { value: group.output_tokens, known: group.output_known_rows, unknown: group.output_unknown_rows }
  return { value: group.cached_input_tokens, known: group.cached_input_known_rows, unknown: group.cached_input_unknown_rows }
}
/** Compact tokens, or null when unknown (shown as a dash, never zero). */
function tokens(group: UsageGroup, which: Which) {
  const part = tokenParts(group, which)
  return part.known ? compactCount(part.value) : null
}
/** Exact tokens with their coverage, for tooltips and screen readers. */
function exactTokens(group: UsageGroup, which: Which) {
  const part = tokenParts(group, which)
  return formatTokens(part.value, part.known, part.unknown)
}
function coverage(group: UsageGroup, which: Which) {
  const part = tokenParts(group, which)
  return part.known && part.unknown ? `from ${part.known} of ${part.known + part.unknown}` : ''
}
function ticketTokens(ticket: UsageTicket) {
  const input = tokens(ticket, 'input')
  const output = tokens(ticket, 'output')
  return [input && `${input} in`, output && `${output} out`].filter(Boolean).join(' · ')
}
function stateNote(state: CostState) {
  return state === 'known' || state === 'unknown' ? '' : costStateLabel[state].toLowerCase()
}
const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`
const gaps = computed(() => {
  const totals = visible.value?.totals
  if (!totals || totals.cost_state === 'known') return ''
  const parts = [
    totals.unreported_sessions ? `${plural(totals.unreported_sessions, 'session', 'sessions')} reported no usage` : '',
    totals.cost_unknown_rows ? `${plural(totals.cost_unknown_rows, 'report', 'reports')} without a price` : '',
    totals.provisional_rows ? `${formatCount(totals.provisional_rows)} provisional` : '',
  ].filter(Boolean)
  return parts.length ? `Totals are incomplete: ${parts.join(', ')}.` : 'Totals are incomplete.'
})

type GroupBy = 'project' | 'model' | 'harness' | 'subscription'
const groupBy = ref<GroupBy>('project')
const groupOptions: { id: GroupBy; label: string }[] = [
  { id: 'project', label: 'Project' },
  { id: 'model', label: 'Model' },
  { id: 'harness', label: 'Harness' },
  { id: 'subscription', label: 'Subscription' },
]
const groupRows = computed(() => {
  const data = visible.value
  if (!data) return []
  if (groupBy.value === 'project') return data.by_project
  if (groupBy.value === 'model') return data.by_model
  if (groupBy.value === 'harness') return data.by_harness ?? []
  return data.by_subscription
})
function harnessName(value: string) {
  if (!value || value === 'Unreported') return value || 'Unreported'
  return value.slice(0, 1).toUpperCase() + value.slice(1)
}
function groupLabel(group: UsageGroup) {
  return groupBy.value === 'harness' ? harnessName(group.label) : group.label
}
const ratingGroups = computed(() => {
  const ratings = visible.value?.ratings
  if (!ratings || (groupBy.value !== 'model' && groupBy.value !== 'harness')) return []
  return groupBy.value === 'model' ? ratings.by_model ?? [] : ratings.by_harness ?? []
})
const showRating = computed(() => groupRows.value.some(row => ratingGroups.value.some(item => item.label === row.label && item.votes > 0)))
function ratingText(label: string) {
  const found = ratingGroups.value.find(item => item.label === label && item.votes > 0)
  return found ? formatRating(found.average) : ''
}
const ratingFact = computed(() => {
  const ratings = visible.value?.ratings
  if (!ratings || ratings.votes < 1) return null
  const value = formatRating(ratings.average)
  if (!value) return null
  const lines = [
    ...(ratings.by_model ?? []).map(item => `${item.label} ${formatRating(item.average)}`),
    ...(ratings.by_harness ?? []).map(item => `${harnessName(item.label)} ${formatRating(item.average)}`),
  ].filter(line => !line.endsWith(' '))
  return { value, detail: plural(ratings.votes, 'vote', 'votes'), tip: lines.join(' · ') }
})

function amount(value: number | null, window: AllowanceWindow) {
  return formatAllowanceAmount(value, window.unit)
}
function share(value: number | null, window: AllowanceWindow) {
  if (value === null || !window.allowance) return 0
  return Math.max(0, Math.min(100, (value / window.allowance) * 100))
}
function allowanceTotal(window: AllowanceWindow) {
  const value = amount(window.allowance, window) ?? '–'
  return window.unit === 'cost_micros' ? value : `${value} ${unitLabel[window.unit]}`
}
function windowFacts(window: AllowanceWindow) {
  return [
    { label: 'Used', value: amount(window.used, window), key: 'used' },
    { label: 'Reserved', value: amount(window.reserved, window), key: 'reserved' },
    { label: 'Pace cap', value: amount(window.pace_cap, window), key: 'cap' },
    { label: 'Headroom', value: amount(window.headroom, window), key: '' },
    { label: 'Hard left', value: amount(window.hard_remaining, window), key: '' },
  ]
}
const caveats = 'Whole-session totals, not spend consumed in the range. The list price is an estimate in USD. A reported subscription is not verified coverage. Unknown is not zero.'
const provisionalNote = 'Used, headroom and hard left stay unknown while a window is provisional, including mixed settled evidence.'
const rankingNote = 'Ranked by the known list estimate of sessions that started in this range. A ticket with no known estimate is counted, not ranked.'
</script>


<template>
  <section class="usage-page" :aria-busy="loading" aria-labelledby="usage-title">
    <header class="page-head">
      <div class="head-main">
        <p class="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</p>
        <h1 id="usage-title">Usage</h1>
        <p class="summary">
          Lifetime usage of the sessions that started in this range, priced at list rates.
          <span class="info" tabindex="0" :data-tip="caveats"><AppIcon name="info" :size="14" /><span class="sr-only">{{ caveats }}</span></span>
        </p>
      </div>
      <RouterLink class="context-link" to="/agents"><AppIcon name="arrow-left" :size="13" />Agents</RouterLink>
    </header>

    <div class="toolbar">
      <div class="seg" role="group" aria-label="Time range">
        <button v-for="value in ranges" :key="value" type="button" :aria-pressed="days === value" @click="setQuery({ days: value })">{{ value }} days</button>
      </div>
      <label class="project">
        <span class="sr-only">Project</span>
        <select class="field" :value="projectId" @change="setQuery({ project: ($event.target as HTMLSelectElement).value })">
          <option value="">All projects</option>
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

      <section class="glass-card card summary-card" aria-labelledby="usage-summary-title">
        <h2 id="usage-summary-title" class="card-title">{{ formatCount(visible.totals.sessions) }} {{ visible.totals.sessions === 1 ? 'session' : 'sessions' }} started</h2>
        <dl class="facts">
          <div class="lead">
            <dt>List estimate</dt>
            <dd>
              <template v-if="visible.totals.estimated_cost_usd !== null">{{ formatUSD(visible.totals.estimated_cost_usd) }}</template>
              <span v-else class="unknown"><span aria-hidden="true">–</span><span class="sr-only">Unknown</span></span>
            </dd>
          </div>
          <div v-for="item in ([['input', 'Input tokens'], ['output', 'Output tokens'], ['cached', 'Cached input']] as const)" :key="item[0]">
            <dt>{{ item[1] }}</dt>
            <dd :data-tip="exactTokens(visible.totals, item[0])">
              <template v-if="tokens(visible.totals, item[0])">{{ tokens(visible.totals, item[0]) }}</template>
              <span v-else class="unknown"><span aria-hidden="true">–</span></span>
              <span class="sr-only">{{ exactTokens(visible.totals, item[0]) }}</span>
              <span v-if="coverage(visible.totals, item[0])" class="coverage" aria-hidden="true">{{ coverage(visible.totals, item[0]) }} reports</span>
            </dd>
          </div>
          <div v-if="ratingFact">
            <dt>Rating</dt>
            <dd :data-tip="ratingFact.tip || undefined">
              {{ ratingFact.value }}
              <span class="coverage">{{ ratingFact.detail }}</span>
            </dd>
          </div>
        </dl>
        <p v-if="gaps" class="gaps"><AppIcon name="info" :size="14" />{{ gaps }}</p>
      </section>

      <section class="glass-card card" aria-labelledby="trend-title">
        <header class="card-head">
          <h2 id="trend-title" class="card-title">By start day</h2>
          <span class="unit">List estimate, USD</span>
        </header>
        <p v-if="!visible.trend.length" class="empty">No sessions started in this range.</p>
        <UsageTrendChart v-else :trend="visible.trend" :from="visible.from" :to="visible.to" />
      </section>

      <div class="pair">
        <section class="glass-card card breakdown" aria-labelledby="breakdown-title">
          <header class="card-head">
            <h2 id="breakdown-title" class="card-title">Breakdown</h2>
            <div class="seg" role="group" aria-label="Group by">
              <button v-for="option in groupOptions" :key="option.id" type="button" :aria-pressed="groupBy === option.id" @click="groupBy = option.id">{{ option.label }}</button>
            </div>
          </header>
          <p v-if="!groupRows.length" class="empty">Nothing in this range.</p>
          <table v-else class="grid">
            <caption class="sr-only">By {{ groupBy }}</caption>
            <thead><tr><th>{{ groupOptions.find(option => option.id === groupBy)?.label }}</th><th class="r">Sessions</th><th class="r opt">Input</th><th class="r cost">List estimate</th><th v-if="showRating" class="r rating">Rating</th></tr></thead>
            <tbody>
              <tr v-for="group in groupRows" :key="group.label + (group.key ?? '')">
                <td class="name">
                  <span class="ellipsis" :title="groupLabel(group)">{{ groupLabel(group) }}</span>
                  <span v-if="groupBy === 'subscription' && group.billing_mode" class="sub">{{ billingLabel[group.billing_mode] }}</span>
                </td>
                <td class="r num">{{ formatCount(group.sessions) }}</td>
                <td class="r num opt" :data-tip="exactTokens(group, 'input')">
                  <template v-if="tokens(group, 'input')">{{ tokens(group, 'input') }}</template>
                  <span v-else class="unknown"><span aria-hidden="true">–</span><span class="sr-only">Unknown</span></span>
                </td>
                <td class="r num">
                  <template v-if="group.estimated_cost_usd !== null">{{ formatUSD(group.estimated_cost_usd) }}</template>
                  <span v-else class="unknown"><span aria-hidden="true">–</span><span class="sr-only">Unknown</span></span>
                  <span v-if="stateNote(group.cost_state)" class="state">{{ stateNote(group.cost_state) }}</span>
                </td>
                <td v-if="showRating" class="r num rating">{{ ratingText(group.label) }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <section class="glass-card card tickets-card" aria-labelledby="tickets-title">
          <header class="card-head">
            <h2 id="tickets-title" class="card-title">Most expensive tickets</h2>
            <span class="info" tabindex="0" :data-tip="rankingNote"><AppIcon name="info" :size="14" /><span class="sr-only">{{ rankingNote }}</span></span>
          </header>
          <p v-if="!visible.tickets.length" class="empty">No ticket in this range has a known list estimate.</p>
          <ol v-else class="tickets">
            <li v-for="ticket in visible.tickets" :key="ticket.id">
              <div class="ticket-main">
                <RouterLink class="ticket-link" :to="ticketHref(ticket)" :title="`${ticket.key} ${ticket.label}`"><span class="key">{{ ticket.key }}</span> <span class="title">{{ ticket.label }}</span></RouterLink>
                <p class="sub">{{ plural(ticket.sessions, 'session', 'sessions') }}<template v-if="ticketTokens(ticket)"> · {{ ticketTokens(ticket) }}</template></p>
              </div>
              <p class="ticket-cost num">
                <template v-if="ticket.estimated_cost_usd !== null">{{ formatUSD(ticket.estimated_cost_usd) }}</template>
                <span v-else class="unknown"><span aria-hidden="true">–</span><span class="sr-only">Unknown</span></span>
                <span v-if="stateNote(ticket.cost_state)" class="state">{{ stateNote(ticket.cost_state) }}</span>
              </p>
            </li>
          </ol>
          <p v-if="visible.tickets_cost_unknown" class="foot">{{ plural(visible.tickets_cost_unknown, 'more ticket has', 'more tickets have') }} no known price.</p>
        </section>

      <section class="glass-card card allowance" aria-labelledby="allowance-title">
        <header class="card-head">
          <h2 id="allowance-title" class="card-title">Allowance</h2>
        </header>
        <p v-if="visible.allowance.state === 'withheld'" class="empty">Allowance windows are visible to workspace admins.</p>
        <p v-else-if="visible.allowance.state === 'none'" class="empty">No allowance window is open.</p>
        <ul v-else class="windows">
          <li v-for="window in visible.allowance.windows" :key="window.window_id" class="window" :aria-label="window.label">
            <div class="window-head">
              <p class="window-name">
                <span class="ellipsis" :title="window.label">{{ window.label }}</span>
                <span class="sub">{{ window.harness }} · {{ paceLabel[window.pace_model].toLowerCase() }} pace</span>
                <span v-if="window.provisional" class="provisional" tabindex="0" :data-tip="provisionalNote">Provisional<span class="sr-only">. {{ provisionalNote }}</span></span>
              </p>
              <p class="window-total num">{{ allowanceTotal(window) }}</p>
            </div>
            <div class="meter" aria-hidden="true">
              <i v-if="window.used !== null" class="used" :style="{ width: `${share(window.used, window)}%` }" />
              <i class="reserved" :style="{ width: `${share(window.reserved, window)}%` }" />
              <b v-if="window.pace_cap !== null && window.pace_cap < window.allowance" class="cap" :style="{ left: `${share(window.pace_cap, window)}%` }" />
            </div>
            <dl class="window-facts">
              <div v-for="fact in windowFacts(window)" :key="fact.label">
                <dt><i v-if="fact.key" class="sw" :class="fact.key" aria-hidden="true" />{{ fact.label }}</dt>
                <dd class="num">
                  <template v-if="fact.value !== null">{{ fact.value }}</template>
                  <span v-else class="unknown"><span aria-hidden="true">–</span><span class="sr-only">Unknown</span></span>
                </dd>
              </div>
            </dl>
          </li>
        </ul>
      </section>
      </div>
    </template>
  </section>
</template>

<style scoped>
.usage-page { width: 100%; margin: 0 auto; padding: 22px var(--gutter) 40px; max-width: 1120px; }
.page-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; margin-bottom: 18px; }
.page-head h1 { margin-top: 6px; }
.summary { display: flex; align-items: center; gap: 6px; margin-top: 8px; font-size: 13.5px; color: var(--ink-2); }
.info { display: inline-grid; place-items: center; width: 22px; height: 22px; flex-shrink: 0; border-radius: 50%; color: var(--ink-3); cursor: help; }
.info:focus-visible, .provisional:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .info:hover { color: var(--ink-2); background: var(--row-hover); } }
.context-link { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border-radius: 999px; color: var(--teal-ink); font-size: 13px; font-weight: 600; white-space: nowrap; }
.toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 12px; margin-bottom: 16px; }
.project .field { min-width: 12rem; height: 32px; }
.range { margin-left: auto; color: var(--ink-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.range span { color: var(--ink-3); }
.state-line { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; color: var(--ink-2); }
.state-line .skeleton { width: 140px; }
.notice { display: flex; align-items: flex-start; gap: 8px; margin-bottom: 12px; padding: 12px 14px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); font-size: 13.5px; }
.notice svg { flex-shrink: 0; margin-top: 1px; }
.card { padding: 16px 18px; margin-bottom: 14px; min-width: 0; }
.card-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px 12px; margin-bottom: 12px; }
.card-title { font: 600 15px/1.3 var(--font); letter-spacing: 0; }
.unit { font-size: 12px; color: var(--ink-3); }
.pair { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); grid-template-areas: 'breakdown tickets' 'allowance tickets'; grid-template-rows: auto 1fr; align-items: start; column-gap: 14px; }
.pair .card { margin-bottom: 14px; }
.breakdown { grid-area: breakdown; }
.tickets-card { grid-area: tickets; }
.allowance { grid-area: allowance; }
.facts { display: grid; grid-template-columns: 1.3fr repeat(3, minmax(0, 1fr)); gap: 14px 20px; margin: 14px 0 0; }
.facts div { min-width: 0; }
.facts dt { font-size: 12px; color: var(--ink-3); }
.facts dd { margin: 4px 0 0; font: 500 22px/1.2 var(--font); letter-spacing: -.01em; font-variant-numeric: tabular-nums; }
.facts .lead dd { font-size: 26px; font-weight: 600; }
.coverage { display: block; margin-top: 2px; font: 400 12px/1.3 var(--font); color: var(--ink-3); letter-spacing: 0; }
.gaps { display: flex; align-items: flex-start; gap: 6px; margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink-2); }
.gaps svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.empty { font-size: 13px; color: var(--ink-2); }
.unknown { color: var(--ink-3); }
.state { margin-left: 6px; font-size: 11.5px; font-weight: 400; color: var(--ink-3); }
.grid { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 13.5px; }
.grid th { height: 30px; padding: 0 8px; text-align: left; font: 500 11.5px/1 var(--font); color: var(--ink-3); border-bottom: 1px solid var(--line-2); white-space: nowrap; }
.grid th:nth-child(2) { width: 76px; }
.grid th:nth-child(3) { width: 72px; }
.grid th:last-child { width: 128px; }
.grid th.cost { width: 128px; }
.grid th.rating { width: 64px; }
.grid td { padding: 9px 8px; border-bottom: 1px solid var(--line); vertical-align: top; }
.grid tr:last-child td { border-bottom: 0; }
.grid tbody tr:hover td { background: var(--row-hover); }
.grid th:first-child, .grid td:first-child { padding-left: 0; }
.grid th:last-child, .grid td:last-child { padding-right: 0; }
.r { text-align: right !important; }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.name { min-width: 0; }
.ellipsis { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sub { font-size: 12px; color: var(--ink-3); }
.name .sub { display: block; margin-top: 2px; }
.tickets { margin: 0; padding: 0; list-style: none; }
.tickets li { display: flex; align-items: flex-start; gap: 12px; padding: 9px 0; border-bottom: 1px solid var(--line); }
.tickets li:last-child { border-bottom: 0; }
.ticket-main { flex: 1; min-width: 0; }
.ticket-link { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; overflow: hidden; min-width: 0; color: var(--ink); font-size: 13.5px; font-weight: 600; line-height: 1.4; }
.key { margin-right: 4px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.tickets .sub { margin-top: 3px; }
.ticket-cost { flex-shrink: 0; font-size: 13.5px; }
.foot { margin-top: 8px; font-size: 12.5px; color: var(--ink-3); }
.windows { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr)); gap: 12px 24px; margin: 0; padding: 0; list-style: none; }
.window { min-width: 0; padding: 12px 0 4px; border-top: 1px solid var(--line); }
.window-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.window-name { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 8px; min-width: 0; font-size: 13.5px; font-weight: 600; }
.window-name .sub { font-weight: 400; flex-shrink: 0; white-space: nowrap; }
.window-name .provisional { margin-left: 0; flex-shrink: 0; }
.window-total { flex-shrink: 0; font-size: 13px; color: var(--ink-2); }
.meter { position: relative; display: flex; gap: 2px; height: 8px; margin: 10px 0; border-radius: 999px; background: var(--track); }
.meter i { display: block; height: 100%; border-radius: 999px; }
.meter .used, .sw.used { background: var(--teal); }
.meter .reserved, .sw.reserved { background: repeating-linear-gradient(135deg, var(--teal) 0 2px, color-mix(in srgb, var(--teal) 35%, transparent) 2px 4px); }
.meter .cap { position: absolute; top: -3px; bottom: -3px; width: 2px; margin-left: -1px; border-radius: 1px; background: var(--ink-2); }
.sw { display: inline-block; width: 8px; height: 8px; margin-right: 5px; border-radius: 2px; vertical-align: 0; }
.sw.cap { width: 2px; height: 10px; margin-right: 6px; background: var(--ink-2); vertical-align: -1px; }
.window-facts { display: flex; flex-wrap: wrap; gap: 8px 18px; margin: 0; }
.window-facts dt { font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.window-facts dd { margin: 2px 0 0; font-size: 13px; }
.provisional { display: inline-flex; align-items: center; height: 18px; margin-left: 6px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 11px; font-weight: 600; color: var(--ink-2); cursor: help; }
@media (hover: hover) { .context-link:hover, .ticket-link:hover { color: var(--teal-ink); } }
.context-link:focus-visible, .ticket-link:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 960px) {
  .pair { grid-template-columns: minmax(0, 1fr); grid-template-areas: 'breakdown' 'tickets' 'allowance'; grid-template-rows: auto; }
  .facts { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .facts .lead { grid-column: 1 / -1; }
}
@media (max-width: 600px) {
  .usage-page { padding: 16px 16px 28px; }
  .page-head { align-items: flex-start; gap: 12px; }
  .summary { align-items: flex-start; }
  .toolbar .seg { order: 1; }
  .project { order: 2; flex: 1 1 100%; }
  .project .field { width: 100%; min-width: 0; height: 40px; }
  .range { order: 3; margin-left: 0; font-size: 12.5px; }
  .card { padding: 14px; }
  .facts dd { font-size: 19px; }
  .facts .lead dd { font-size: 24px; }
  .opt { display: none; }
  .window-facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px 12px; }
  .grid th:nth-child(2) { width: 64px; }
  .grid th:last-child { width: 108px; }
  .grid th.cost { width: 108px; }
  .grid th.rating { width: 52px; }
}
</style>
